package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
)

// ---------------------------------------------------------------------------
// Straling: the gamma dose rate of the RIVM Nationaal Meetnet Radioactiviteit
// (~150 stations), via EURDEP as published by the German Bundesamt für
// Strahlenschutz (open WFS, no key, no fees). Hourly averages in µSv/h, a few
// hours delayed. Normal Dutch background is about 0.05-0.15 µSv/h; rain can
// briefly raise it (radon washout). "Raised" needs several stations above the
// configured level, so one wet station does not trigger a notice.

const radKey = "eurdep:nl"

type RadStation struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Value float64   `json:"value"` // µSv/h
	Time  time.Time `json:"time"`  // end of the hourly measurement
	lat   float64
	lon   float64
}

type RadSummary struct {
	Nearest  *RadStation `json:"nearest,omitempty"`
	Km       float64     `json:"km,omitempty"`
	Min      float64     `json:"min"`
	Median   float64     `json:"median"`
	Max      float64     `json:"max"`
	MaxName  string      `json:"max_name"`
	Stations int         `json:"stations"`
	Raised   bool        `json:"raised"`
	Above    int         `json:"above"` // stations at or above the alert level
	Level    float64     `json:"level"` // the alert level in µSv/h
	Time     time.Time   `json:"time"`  // newest measurement
}

func radURL(base string) string {
	q := url.Values{"service": {"WFS"}, "version": {"1.1.0"}, "request": {"GetFeature"}, "typeName": {"opendata:eurdep_latestValue"},
		"outputFormat": {"application/json"}, "CQL_FILTER": {"id LIKE 'NL%' AND analyzed_range_in_h=6"}}
	return base + "?" + q.Encode()
}

// stationName: "WIERINGERWERF" -> "Wieringerwerf", "DEN HAAG-ZUID" -> "Den Haag-Zuid".
func stationName(s string) string {
	r := []rune(strings.ToLower(strings.TrimSpace(s)))
	for i := range r {
		if i == 0 || r[i-1] == ' ' || r[i-1] == '-' || r[i-1] == '(' {
			r[i] = unicode.ToUpper(r[i])
		}
	}
	return string(r)
}

func parseRadiation(body []byte) ([]RadStation, error) {
	var fc struct {
		Features []struct {
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties struct {
				ID      string  `json:"id"`
				Name    string  `json:"name"`
				Status  int     `json:"site_status"`
				End     string  `json:"end_measure"`
				Value   float64 `json:"value"`
				Unit    string  `json:"unit"`
				Nuclide string  `json:"nuclide"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(body, &fc); err != nil {
		return nil, fmt.Errorf("eurdep: %w", err)
	}
	seen := map[string]bool{}
	var out []RadStation
	for _, f := range fc.Features {
		p := f.Properties
		if !strings.HasPrefix(p.ID, "NL") || seen[p.ID] || p.Status != 1 || p.Unit != "µSv/h" || p.Nuclide != "Gamma-ODL-Brutto" ||
			p.Value <= 0 || p.Value > 1000 || len(f.Geometry.Coordinates) != 2 {
			continue
		}
		t, err := time.Parse(time.RFC3339, p.End)
		if err != nil {
			continue
		}
		seen[p.ID] = true
		out = append(out, RadStation{ID: p.ID, Name: truncate(stationName(plainText(p.Name)), 40), Value: p.Value, Time: t.UTC(),
			lon: f.Geometry.Coordinates[0], lat: f.Geometry.Coordinates[1]})
	}
	if len(out) == 0 {
		return nil, errors.New("eurdep: no Dutch stations in the response")
	}
	return out, nil
}

// summarizeRadiation: the national range, the station nearest to lat/lon (only
// with fresh data, < 12 h) and whether at least minStations are at or above level.
func summarizeRadiation(list []RadStation, lat, lon, level float64, minStations int, now time.Time) RadSummary {
	s := RadSummary{Level: level}
	var vals []float64
	best := math.Inf(1)
	for i := range list {
		st := &list[i]
		if now.Sub(st.Time) > 12*time.Hour {
			continue
		}
		vals = append(vals, st.Value)
		if st.Value > s.Max {
			s.Max, s.MaxName = st.Value, st.Name
		}
		if st.Value >= level {
			s.Above++
		}
		if st.Time.After(s.Time) {
			s.Time = st.Time
		}
		if d := haversineKm(lat, lon, st.lat, st.lon); d < best {
			best, s.Nearest = d, st
		}
	}
	if len(vals) == 0 {
		return s
	}
	slices.Sort(vals)
	s.Stations, s.Min, s.Median = len(vals), vals[0], vals[len(vals)/2]
	s.Km = math.Round(best)
	s.Raised = s.Above >= minStations
	return s
}

func (a *App) radiationSummary(lat, lon float64) (RadSummary, bool) {
	cfg := a.config().Radiation
	list, ok := a.threats.get(radKey).Data.([]RadStation)
	if !ok {
		return RadSummary{}, false
	}
	return summarizeRadiation(list, lat, lon, cfg.AlertUSv, cfg.AlertStations, time.Now()), true
}

func (a *App) handleRadiation(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Radiation.Enabled {
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
	e := a.feedEntry(radKey)
	e["enabled"] = true
	if s, ok := a.radiationSummary(lat, lon); ok && s.Stations > 0 {
		e["data"] = s
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}
