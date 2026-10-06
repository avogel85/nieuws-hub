package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// Onweer (part of the weather): the thunderstorm risk for the next 24 hours from
// Open-Meteo's lightning potential index (LPI, from the ICON-D2 model), CAPE and
// the thunderstorm weather codes. A forecast, not live lightning.

// WxUV: the highest UV index in the next 24 hours (in the evening: tomorrow's),
// with the WHO category: 0 laag (0-2), 1 matig (3-5), 2 hoog (6-7), 3 zeer hoog (8-10), 4 extreem (11+).
type WxUV struct {
	Max   float64 `json:"max"`
	Peak  int64   `json:"peak"` // unix time of the highest hour
	Level int     `json:"level"`
}

func uvLevel(v float64) int {
	switch r := math.Round(v); {
	case r >= 11:
		return 4
	case r >= 8:
		return 3
	case r >= 6:
		return 2
	case r >= 3:
		return 1
	}
	return 0
}

func computeUV(times []int64, uv []*float64) *WxUV {
	var best *WxUV
	for i, t := range times {
		if i >= len(uv) || uv[i] == nil {
			continue
		}
		if best == nil || *uv[i] > best.Max {
			best = &WxUV{Max: round1(*uv[i]), Peak: t}
		}
	}
	if best != nil {
		best.Level = uvLevel(best.Max)
	}
	return best
}

type WxThunder struct {
	Level int   `json:"level"`          // 0 none, 1 small, 2 moderate, 3 high
	Peak  int64 `json:"peak,omitempty"` // unix time of the highest risk
}

// thunderRisk scores one hour: LPI above ~1 J/kg means lightning is likely in
// the model, CAPE says how much energy showers have, codes 95-99 are thunderstorms.
func thunderRisk(lpi, cape float64, code int) int {
	lvl := 0
	switch {
	case lpi >= 5:
		lvl = 3
	case lpi >= 1:
		lvl = 2
	case lpi >= 0.2:
		lvl = 1
	}
	if code >= 95 {
		lvl = max(lvl, 2)
		if code >= 96 {
			lvl = 3 // with hail
		}
	}
	if cape >= 2000 {
		lvl = max(lvl, 2)
	} else if cape >= 1000 {
		lvl = max(lvl, 1)
	}
	return lvl
}

func computeThunder(times []int64, lpi, cape []*float64, codes []int) WxThunder {
	var t WxThunder
	for i, ts := range times {
		l, c := 0.0, 0.0
		if i < len(lpi) && lpi[i] != nil {
			l = *lpi[i]
		}
		if i < len(cape) && cape[i] != nil {
			c = *cape[i]
		}
		if r := thunderRisk(l, c, at(codes, i)); r > t.Level {
			t.Level, t.Peak = r, ts
		}
	}
	return t
}

// ---------------------------------------------------------------------------
// Teken en muggen: an estimate of tick and mosquito activity from the weather,
// for today and the next two days. There is no open source with measurements
// (Tekenradar's activity map needs an account), so this is an indication:
// ticks (Ixodes ricinus) become active above about 7 °C and like humid air;
// house mosquitoes (Culex) fly on warm, humid, calm evenings.

type InsectDay struct {
	Date     string `json:"date"`
	Ticks    int    `json:"ticks"`    // 0 none, 1 low, 2 moderate, 3 high
	Mosquito int    `json:"mosquito"` // 0 none, 1 low, 2 moderate, 3 high
}

type InsectData struct {
	Days []InsectDay `json:"days"`
}

func tickLevel(temp, rh float64, month time.Month) int {
	lvl := 0
	switch {
	case temp < 5:
		return 0
	case temp < 8:
		lvl = 1
	case rh >= 70 && temp >= 12 && temp <= 25:
		lvl = 3
	case rh >= 70 || (temp <= 25 && rh >= 50):
		lvl = 2
	default:
		lvl = 1 // hot and dry: ticks stay low in the vegetation
	}
	if month == time.December || month == time.January || month == time.February {
		lvl = max(lvl-1, 0)
	}
	return lvl
}

func mosquitoLevel(temp, rh, wind float64) int {
	lvl := 0
	switch {
	case temp < 12:
		return 0
	case temp < 16:
		lvl = 1
	default:
		lvl = 2
		if temp >= 18 && rh >= 70 && wind < 15 {
			lvl = 3
		}
	}
	if wind >= 25 {
		lvl = max(lvl-1, 0)
	}
	return lvl
}

// parseInsects averages daytime (10-18 h) for ticks and the evening (19-23 h) for mosquitoes.
func parseInsects(body []byte, now time.Time) (InsectData, error) {
	var r struct {
		Hourly struct {
			Time []string   `json:"time"`
			Temp []*float64 `json:"temperature_2m"`
			RH   []*float64 `json:"relative_humidity_2m"`
			Wind []*float64 `json:"wind_speed_10m"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return InsectData{}, err
	}
	type acc struct{ t, rh, n, et, erh, ew, en float64 }
	days := map[string]*acc{}
	var order []string
	for i, ts := range r.Hourly.Time {
		t, err := time.ParseInLocation("2006-01-02T15:04", ts, amsterdam)
		if err != nil || i >= len(r.Hourly.Temp) || r.Hourly.Temp[i] == nil || i >= len(r.Hourly.RH) || r.Hourly.RH[i] == nil {
			continue
		}
		day := t.Format("2006-01-02")
		a := days[day]
		if a == nil {
			a = &acc{}
			days[day] = a
			order = append(order, day)
		}
		temp, rh, wind := *r.Hourly.Temp[i], *r.Hourly.RH[i], 0.0
		if i < len(r.Hourly.Wind) && r.Hourly.Wind[i] != nil {
			wind = *r.Hourly.Wind[i]
		}
		if h := t.Hour(); h >= 10 && h <= 18 {
			a.t, a.rh, a.n = a.t+temp, a.rh+rh, a.n+1
		} else if h >= 19 && h <= 23 {
			a.et, a.erh, a.ew, a.en = a.et+temp, a.erh+rh, a.ew+wind, a.en+1
		}
	}
	today := now.In(amsterdam).Format("2006-01-02")
	d := InsectData{Days: []InsectDay{}}
	for _, day := range order {
		a := days[day]
		if day < today || a.n == 0 || a.en == 0 || len(d.Days) == 3 {
			continue
		}
		t, _ := time.Parse("2006-01-02", day)
		d.Days = append(d.Days, InsectDay{Date: day, Ticks: tickLevel(a.t/a.n, a.rh/a.n, t.Month()), Mosquito: mosquitoLevel(a.et/a.en, a.erh/a.en, a.ew/a.en)})
	}
	if len(d.Days) == 0 {
		return d, errors.New("open-meteo: no forecast for today")
	}
	return d, nil
}

func (a *App) handleInsects(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Insects.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	lat, lon := cfg.Weather.Location.Lat, cfg.Weather.Location.Lon
	if q := r.URL.Query(); q.Get("lat") != "" || q.Get("lon") != "" {
		var ok1, ok2 bool
		lat, ok1 = parseCoord(q.Get("lat"), 90)
		lon, ok2 = parseCoord(q.Get("lon"), 180)
		if !ok1 || !ok2 {
			writeError(w, r, http.StatusBadRequest, "lat/lon ongeldig")
			return
		}
	}
	lat, lon = math.Round(lat*10)/10, math.Round(lon*10)/10
	ip := a.clientIP(r)
	ctx := context.WithoutCancel(r.Context())
	d, at, stale, err := a.insects.get(fmt.Sprintf("%.1f,%.1f", lat, lon), time.Hour, func() (InsectData, error) {
		if !a.wx.limiter.allow(ip) {
			return InsectData{}, errRateLimited
		}
		u := fmt.Sprintf("%s?latitude=%.1f&longitude=%.1f&hourly=temperature_2m,relative_humidity_2m,wind_speed_10m&timezone=Europe%%2FAmsterdam&forecast_days=4", cfg.Insects.URL, lat, lon)
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
		if err != nil {
			return InsectData{}, fmt.Errorf("open-meteo: %w", err)
		}
		return parseInsects(resp.Body, time.Now())
	})
	switch {
	case errors.Is(err, errRateLimited):
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
		return
	case err != nil:
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": true, "error": shortErr(err).Error()})
		return
	}
	e := map[string]any{"enabled": true, "data": d, "fetched_at": at.UTC().Truncate(time.Second)}
	if stale {
		e["error"] = "verouderd"
	}
	writeJSON(w, r, http.StatusOK, 900, e)
}
