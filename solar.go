package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

// ---------------------------------------------------------------------------
// Zonnestroom: expected solar yield for today and tomorrow from the Open-Meteo
// forecast of the sunlight on a tilted plane (global_tilted_irradiance, W/m²).
// Per kWp: kWh = irradiation (kWh/m²) × performance ratio; the ratio covers
// inverter, heat and cable losses. The visitor's kWp only scales the result, so
// the server caches per place, tilt and direction, not per installation.

const solarPR = 0.8

type SolarDay struct {
	Date     string  `json:"date"`       // YYYY-MM-DD, Dutch time
	KWhPerKW float64 `json:"kwh_per_kw"` // expected kWh per installed kWp
	BestFrom int64   `json:"best_from"`  // start of the sunniest 3 hours (unix)
	BestTo   int64   `json:"best_to"`
}

func parseSolar(body []byte) ([]SolarDay, error) {
	var om struct {
		Hourly struct {
			Time []int64    `json:"time"`
			GTI  []*float64 `json:"global_tilted_irradiance"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(body, &om); err != nil {
		return nil, fmt.Errorf("open-meteo: %w", err)
	}
	if len(om.Hourly.Time) == 0 || len(om.Hourly.GTI) != len(om.Hourly.Time) {
		return nil, errors.New("open-meteo: no irradiance in the response")
	}
	var days []SolarDay
	idx := map[string]int{}
	var hours [][]int // per day: indexes into Time
	for i, t := range om.Hourly.Time {
		d := time.Unix(t, 0).In(amsterdam).Format("2006-01-02")
		n, ok := idx[d]
		if !ok {
			n = len(days)
			idx[d] = n
			days = append(days, SolarDay{Date: d})
			hours = append(hours, nil)
		}
		hours[n] = append(hours[n], i)
	}
	val := func(i int) float64 {
		if v := om.Hourly.GTI[i]; v != nil && *v > 0 {
			return *v
		}
		return 0
	}
	for n := range days {
		sum, best := 0.0, -1.0
		hs := hours[n]
		for k, i := range hs {
			sum += val(i) // one hour at this mean power = Wh/m²
			if k+2 < len(hs) {
				if w := val(i) + val(hs[k+1]) + val(hs[k+2]); w > best {
					best = w
					// Open-Meteo's value for hour t is the mean of the hour before t
					days[n].BestFrom, days[n].BestTo = om.Hourly.Time[i]-3600, om.Hourly.Time[hs[k+2]]
				}
			}
		}
		days[n].KWhPerKW = math.Round(sum/1000*solarPR*100) / 100
		if best <= 0 {
			days[n].BestFrom, days[n].BestTo = 0, 0
		}
	}
	if len(days) > 2 {
		days = days[:2]
	}
	return days, nil
}

func (a *App) handleSolar(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Solar.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	q := r.URL.Query()
	lat, lon := cfg.Weather.Location.Lat, cfg.Weather.Location.Lon
	if q.Get("lat") != "" || q.Get("lon") != "" {
		var ok1, ok2 bool
		lat, ok1 = parseCoord(q.Get("lat"), 90)
		lon, ok2 = parseCoord(q.Get("lon"), 180)
		if !ok1 || !ok2 {
			writeError(w, r, http.StatusBadRequest, "lat/lon ongeldig")
			return
		}
	}
	tilt, az := cfg.Solar.Tilt, cfg.Solar.Azimuth
	if v := q.Get("tilt"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 90 {
			writeError(w, r, http.StatusBadRequest, "tilt ongeldig (0-90)")
			return
		}
		tilt = n
	}
	if v := q.Get("az"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < -180 || n > 180 {
			writeError(w, r, http.StatusBadRequest, "az ongeldig (-180-180)")
			return
		}
		az = n
	}
	lat, lon = math.Round(lat*10)/10, math.Round(lon*10)/10
	tilt, az = tilt/5*5, az/15*15 // coarse steps keep the cache small; the difference is a few percent
	ip := a.clientIP(r)
	ctx := context.WithoutCancel(r.Context())
	days, at, stale, err := a.solar.get(fmt.Sprintf("%.1f,%.1f,%d,%d", lat, lon, tilt, az), time.Hour, func() ([]SolarDay, error) {
		if !a.wx.limiter.allow(ip) {
			return nil, errRateLimited
		}
		u := fmt.Sprintf("%s?latitude=%.1f&longitude=%.1f&hourly=global_tilted_irradiance&tilt=%d&azimuth=%d&timezone=Europe%%2FAmsterdam&timeformat=unixtime&forecast_days=2",
			cfg.Solar.URL, lat, lon, tilt, az)
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
		if err != nil {
			return nil, fmt.Errorf("open-meteo: %w", err)
		}
		return parseSolar(resp.Body)
	})
	switch {
	case errors.Is(err, errRateLimited):
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
		return
	case err != nil:
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": true, "error": shortErr(err).Error()})
		return
	}
	e := map[string]any{"enabled": true, "days": days, "tilt": tilt, "az": az, "kwp": cfg.Solar.KWp, "pr": solarPR,
		"fetched_at": at.UTC().Truncate(time.Second)}
	if stale {
		e["error"] = "verouderd"
	}
	writeJSON(w, r, http.StatusOK, 900, e)
}
