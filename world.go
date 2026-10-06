package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Wereldwijd: big earthquakes worldwide (USGS) and active natural events (NASA
// EONET: storms, volcanoes, large wildfires, floods); and, in the sky panel, the
// next rocket launches (Launch Library 2 by The Space Devs). All three are open
// and need no key; the server fetches them on a schedule and serves everyone from
// the same copy.

type WorldQuake struct {
	Mag     float64   `json:"mag"`
	Place   string    `json:"place"`
	Time    time.Time `json:"time"`
	DepthKm float64   `json:"depth_km"`
	Tsunami bool      `json:"tsunami,omitempty"` // the tsunami flag of the Pacific/National Tsunami Warning Center
	Alert   string    `json:"alert,omitempty"`   // PAGER: green | yellow | orange | red (expected impact)
	URL     string    `json:"url,omitempty"`
}

// parseUSGS reads a USGS GeoJSON summary feed: quakes of at least minMag in the
// last hours, newest first, at most 8.
func parseUSGS(body []byte, minMag float64, hours int, now time.Time) ([]WorldQuake, error) {
	var fc struct {
		Type     string `json:"type"`
		Features []struct {
			Properties struct {
				Mag     *float64 `json:"mag"`
				Place   string   `json:"place"`
				Time    int64    `json:"time"`
				URL     string   `json:"url"`
				Tsunami int      `json:"tsunami"`
				Alert   *string  `json:"alert"`
				Type    string   `json:"type"`
			} `json:"properties"`
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(body, &fc); err != nil {
		return nil, fmt.Errorf("usgs: %w", err)
	}
	if fc.Type != "FeatureCollection" {
		return nil, errors.New("usgs: not a GeoJSON feed")
	}
	out := []WorldQuake{}
	for _, f := range fc.Features {
		p := f.Properties
		if p.Mag == nil || *p.Mag < minMag || p.Type != "earthquake" {
			continue
		}
		t := time.UnixMilli(p.Time).UTC()
		if now.Sub(t) > time.Duration(hours)*time.Hour {
			continue
		}
		q := WorldQuake{Mag: math.Round(*p.Mag*10) / 10, Place: truncate(plainText(p.Place), 100), Time: t, Tsunami: p.Tsunami == 1, URL: safeURL(p.URL, nil)}
		if len(f.Geometry.Coordinates) >= 3 {
			q.DepthKm = math.Round(f.Geometry.Coordinates[2])
		}
		if p.Alert != nil && strings.Contains("green yellow orange red", *p.Alert) {
			q.Alert = *p.Alert
		}
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > 20 {
		out = out[:20]
	}
	return out, nil
}

type WorldEvent struct {
	Kind    string    `json:"kind"` // storm | volcano | wildfire | flood | landslide
	Title   string    `json:"title"`
	Time    time.Time `json:"time"` // latest observation
	WindKmh int       `json:"wind_kmh,omitempty"`
	AreaHa  int       `json:"area_ha,omitempty"`
	URL     string    `json:"url,omitempty"`
}

var eonetKinds = map[string]string{"severeStorms": "storm", "volcanoes": "volcano", "wildfires": "wildfire", "floods": "flood", "landslides": "landslide"}

// parseEONET keeps the events that are news and were updated in the last hours:
// storms, volcanoes, floods, landslides, and wildfires of at least minFireHa
// hectares. Sea ice, small fires and the rest are left out. Storms first
// (strongest first), then by kind and date; at most 40 (the panel shows 4 per kind).
func parseEONET(body []byte, minFireHa float64, hours int, now time.Time) ([]WorldEvent, error) {
	var d struct {
		Events []struct {
			Title      string `json:"title"`
			Closed     *string
			Categories []struct {
				ID string `json:"id"`
			} `json:"categories"`
			Sources []struct {
				URL string `json:"url"`
			} `json:"sources"`
			Geometry []struct {
				Mag  *float64 `json:"magnitudeValue"`
				Unit *string  `json:"magnitudeUnit"`
				Date string   `json:"date"`
			} `json:"geometry"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("eonet: %w", err)
	}
	if d.Events == nil {
		return nil, errors.New("eonet: no events list")
	}
	out := []WorldEvent{}
	for _, e := range d.Events {
		if e.Closed != nil || len(e.Categories) == 0 || len(e.Geometry) == 0 {
			continue
		}
		kind, ok := eonetKinds[e.Categories[0].ID]
		if !ok {
			continue
		}
		g := e.Geometry[len(e.Geometry)-1]
		t, err := time.Parse(time.RFC3339, g.Date)
		if err != nil || now.Sub(t) > time.Duration(hours)*time.Hour { // not updated recently: probably over
			continue
		}
		ev := WorldEvent{Kind: kind, Title: truncate(plainText(e.Title), 90), Time: t.UTC()}
		if len(e.Sources) > 0 {
			ev.URL = safeURL(e.Sources[0].URL, nil)
		}
		unit := ""
		if g.Unit != nil {
			unit = *g.Unit
		}
		switch kind {
		case "storm":
			if g.Mag != nil && unit == "kts" {
				ev.WindKmh = int(math.Round(*g.Mag * 1.852))
			}
		case "wildfire":
			ha := 0.0
			if g.Mag != nil {
				switch unit {
				case "acres":
					ha = *g.Mag * 0.4047
				case "ha", "hectares":
					ha = *g.Mag
				}
			}
			if ha < minFireHa {
				continue
			}
			ev.AreaHa = int(math.Round(ha))
		}
		out = append(out, ev)
	}
	rank := map[string]int{"storm": 0, "volcano": 1, "flood": 2, "landslide": 3, "wildfire": 4}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank[a.Kind] != rank[b.Kind] {
			return rank[a.Kind] < rank[b.Kind]
		}
		if a.WindKmh != b.WindKmh {
			return a.WindKmh > b.WindKmh
		}
		if a.AreaHa != b.AreaHa {
			return a.AreaHa > b.AreaHa
		}
		return a.Time.After(b.Time)
	})
	if len(out) > 40 {
		out = out[:40]
	}
	return out, nil
}

type Launch struct {
	Rocket   string    `json:"rocket"`
	Mission  string    `json:"mission"`
	Provider string    `json:"provider"`
	Place    string    `json:"place"` // launch site, e.g. "Kennedy Space Center, FL, USA"
	Time     time.Time `json:"time"`
	Exact    bool      `json:"exact"`  // the time is known to the hour or better (else only the day or month)
	Status   string    `json:"status"` // go | tbd | hold | flight
}

// parseLaunches reads Launch Library 2 "upcoming": launches that have not taken
// off yet (or are in flight now), soonest first, at most 4.
func parseLaunches(body []byte, now time.Time) ([]Launch, error) {
	var d struct {
		Results []struct {
			Name   string `json:"name"`
			Net    string `json:"net"`
			Status struct {
				Abbrev string `json:"abbrev"`
			} `json:"status"`
			Precision *struct {
				Name string `json:"name"`
			} `json:"net_precision"`
			LSP *struct {
				Name   string `json:"name"`
				Abbrev string `json:"abbrev"`
			} `json:"launch_service_provider"`
			Rocket *struct {
				Configuration struct {
					Name string `json:"name"`
				} `json:"configuration"`
			} `json:"rocket"`
			Mission *struct {
				Name string `json:"name"`
			} `json:"mission"`
			Pad *struct {
				Location struct {
					Name string `json:"name"`
				} `json:"location"`
			} `json:"pad"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("launch library: %w", err)
	}
	if d.Results == nil {
		return nil, errors.New("launch library: no results")
	}
	out := []Launch{}
	for _, r := range d.Results {
		t, err := time.Parse(time.RFC3339, r.Net)
		if err != nil {
			continue
		}
		var st string
		switch strings.ToLower(r.Status.Abbrev) {
		case "go":
			st = "go"
		case "tbd", "tbc":
			st = "tbd"
		case "hold":
			st = "hold"
		case "in flight":
			st = "flight"
		default: // Success, Failure, Partial Failure: already done
			continue
		}
		if st != "flight" && t.Before(now.Add(-time.Hour)) {
			continue
		}
		l := Launch{Time: t.UTC(), Status: st}
		rocket, mission, _ := strings.Cut(r.Name, " | ")
		l.Rocket, l.Mission = truncate(plainText(rocket), 50), truncate(plainText(mission), 60)
		if r.Rocket != nil && r.Rocket.Configuration.Name != "" {
			l.Rocket = truncate(plainText(r.Rocket.Configuration.Name), 50)
		}
		if r.Mission != nil && r.Mission.Name != "" {
			l.Mission = truncate(plainText(r.Mission.Name), 60)
		}
		if r.LSP != nil {
			l.Provider = truncate(plainText(r.LSP.Name), 60)
			if len(l.Provider) > 24 && r.LSP.Abbrev != "" { // "China Aerospace Science and Technology Corporation" -> "CASC"
				l.Provider = truncate(plainText(r.LSP.Abbrev), 20)
			}
		}
		if r.Pad != nil {
			l.Place = truncate(plainText(r.Pad.Location.Name), 70)
		}
		l.Exact = r.Precision == nil || strings.EqualFold(r.Precision.Name, "Minute") || strings.EqualFold(r.Precision.Name, "Second") || strings.EqualFold(r.Precision.Name, "Hour")
		out = append(out, l)
		if len(out) == 8 {
			break
		}
	}
	return out, nil
}

func (a *App) handleWorld(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.World.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	q := a.feedEntry("usgs:world")
	if v, ok := a.threats.get("usgs:world").Data.([]WorldQuake); ok {
		q["items"] = v
	}
	ev := a.feedEntry("eonet:events")
	if v, ok := a.threats.get("eonet:events").Data.([]WorldEvent); ok {
		ev["items"] = v
	}
	// filter again at request time: the data is fetched every 30 minutes
	hours := time.Duration(cfg.World.Hours) * time.Hour
	if v, ok := q["items"].([]WorldQuake); ok {
		q["items"] = keepRecent(v, func(x WorldQuake) time.Time { return x.Time }, hours)
	}
	if v, ok := ev["items"].([]WorldEvent); ok {
		ev["items"] = keepRecent(v, func(x WorldEvent) time.Time { return x.Time }, hours)
	}
	resp := map[string]any{"enabled": true, "min_mag": cfg.World.QuakeMinMag, "hours": cfg.World.Hours, "quakes": q, "events": ev}
	if cfg.World.FireRisk {
		fr := a.feedEntry("brandweer:firerisk")
		if v, ok := a.threats.get("brandweer:firerisk").Data.([]FireRegion); ok {
			fr["regions"] = v
		}
		fr["url"] = cfg.World.FireRiskURL
		resp["fire_risk"] = fr
	}
	writeJSON(w, r, http.StatusOK, 300, resp)
}

func keepRecent[T any](list []T, at func(T) time.Time, within time.Duration) []T {
	out := []T{}
	for _, x := range list {
		if time.Since(at(x)) <= within {
			out = append(out, x)
		}
	}
	return out
}

// upcomingLaunches: launches in the coming hours, and any flight under way.
func upcomingLaunches(list []Launch, hours int, now time.Time) []Launch {
	out := []Launch{}
	for _, l := range list {
		if l.Status == "flight" || (!l.Time.Before(now.Add(-time.Hour)) && l.Time.Sub(now) <= time.Duration(hours)*time.Hour) {
			out = append(out, l)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Natuurbrandrisico: the current wildfire risk phase per safety region (25
// veiligheidsregio's), as Brandweer Nederland publishes it on
// brandweer.nl/natuurbrandrisico. There is no open API; the list "Alle locaties
// & huidige fase" is read from the public page once an hour (robots.txt allows
// it, crawl delay 10 s). Phase 1: normal; phase 2: increased risk (longer dry,
// measures such as no open fire); 0: unknown.

type FireRegion struct {
	Region string `json:"region"`
	Phase  int    `json:"phase"` // 0 = unknown
}

var (
	fireRiskRe  = regexp.MustCompile(`(?s)<div class="risk">\s*([^<]{2,60}?)\s*<div class="risk-phase">\s*([^<]{1,30}?)\s*</div>`)
	firePhaseRe = regexp.MustCompile(`(?i)^fase\s*(\d)$`)
)

func parseFireRisk(body []byte) ([]FireRegion, error) {
	out := []FireRegion{}
	seen := map[string]bool{}
	for _, m := range fireRiskRe.FindAllSubmatch(body, 40) {
		name := plainText(string(m[1]))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		r := FireRegion{Region: truncate(name, 40)}
		if p := firePhaseRe.FindStringSubmatch(strings.TrimSpace(plainText(string(m[2])))); p != nil {
			r.Phase = int(p[1][0] - '0')
		}
		out = append(out, r)
	}
	if len(out) < 20 { // the page has 25 regions; fewer means its layout changed
		return nil, fmt.Errorf("natuurbrandrisico: %d regions found on the page (expected 25)", len(out))
	}
	return out, nil
}
