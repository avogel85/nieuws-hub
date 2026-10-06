package main

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// AMBER Alert and Vermist Kind Alert from Burgernet (the Dutch police), via the
// open API "Landactiehost" (no key; see Technische koppelingen Burgernet/AMBER
// Alert v1.1). AlertLevel 10 is a national AMBER Alert; 5 is a regional Vermist
// Kind Alert with a circle (centre and radius) as its area. Type is Alert, Update
// or Cancel; a Cancel closes the action. The feed is an empty list when nothing
// is active, which is almost always.

type AmberAlert struct {
	ID       string    `json:"id"`
	National bool      `json:"national"`        // AMBER Alert (level 10); false = Vermist Kind Alert (level 5)
	Title    string    `json:"title"`           // name (age)
	Text     string    `json:"text"`            // description: last seen, appearance, vehicle
	Kind     string    `json:"kind,omitempty"`  // Vermist | Ontvoerd
	URL      string    `json:"url,omitempty"`   // more information (politie.nl / burgernet.nl)
	Image    string    `json:"image,omitempty"` // photo, rewritten to the image proxy in the handler
	Area     string    `json:"area,omitempty"`  // municipality at the centre of the circle
	Sent     time.Time `json:"sent"`
	Near     bool      `json:"near,omitempty"` // the visitor's place lies in the area (always true for AMBER)
	lat, lon float64
	radiusKm float64
}

func parseAmber(body []byte) ([]AmberAlert, error) {
	var raw []struct {
		AlertID    string `json:"AlertId"`
		Sent       string `json:"Sent"`
		Type       string `json:"Type"`
		AlertLevel string `json:"AlertLevel"`
		Message    struct {
			Title    *string `json:"Title"`
			Desc     *string `json:"Description"`
			DescExt  *string `json:"DescriptionExt"`
			Readmore *string `json:"Readmore_URL"`
			Media    struct {
				Image *string `json:"Image"`
			} `json:"Media"`
		} `json:"Message"`
		Area struct {
			Description *string `json:"Description"`
			Circle      *string `json:"Circle"`
		} `json:"Area"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	out := []AmberAlert{}
	for _, r := range raw {
		if !strings.EqualFold(r.Type, "Alert") && !strings.EqualFold(r.Type, "Update") {
			continue // Cancel: the action is closed
		}
		if _, err := strconv.ParseUint(r.AlertID, 10, 64); err != nil {
			continue
		}
		a := AmberAlert{ID: r.AlertID, National: r.AlertLevel == "10", Title: truncate(plainText(str(r.Message.Title)), 80),
			Text: truncate(plainText(str(r.Message.Desc)), 600), Kind: truncate(plainText(str(r.Message.DescExt)), 20),
			Area: truncate(plainText(str(r.Area.Description)), 60)}
		if s, err := strconv.ParseInt(r.Sent, 10, 64); err == nil {
			a.Sent = time.Unix(s, 0).UTC()
		}
		if u := safeURL(str(r.Message.Readmore), nil); u != "" {
			a.URL = u
		}
		if u := str(r.Message.Media.Image); strings.HasPrefix(u, "https://") {
			a.Image = u
		}
		// Circle: "lat,lon radius-in-metres"
		if c := strings.Fields(str(r.Area.Circle)); len(c) == 2 {
			ll := strings.Split(c[0], ",")
			rad, err := strconv.ParseFloat(c[1], 64)
			if len(ll) == 2 && err == nil {
				a.lat, _ = strconv.ParseFloat(ll[0], 64)
				a.lon, _ = strconv.ParseFloat(ll[1], 64)
				a.radiusKm = rad / 1000
			}
		}
		if a.Title == "" && a.Text == "" {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// covers: a national AMBER Alert covers everyone; a Vermist Kind Alert its circle.
func (a AmberAlert) covers(lat, lon float64) bool {
	if a.National {
		return true
	}
	if a.radiusKm <= 0 {
		return false
	}
	return haversineKm(lat, lon, a.lat, a.lon) <= a.radiusKm
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	dLat, dLon := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

func (a *App) handleAmber(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Amber.Enabled {
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
	e := a.feedEntry("burgernet:amber")
	e["enabled"] = true
	if v, ok := a.threats.get("burgernet:amber").Data.([]AmberAlert); ok {
		list := []AmberAlert{}
		for _, al := range v {
			al.Near = al.covers(lat, lon)
			if al.Image != "" {
				if cfg.Features.ProxyImages { // the photo comes from this server, never directly from Burgernet
					al.Image = "api/img?u=" + base64.RawURLEncoding.EncodeToString([]byte(al.Image)) + "&s=" + a.images.sign(al.Image)
				} else {
					al.Image = ""
				}
			}
			list = append(list, al)
		}
		e["alerts"] = list
	}
	writeJSON(w, r, http.StatusOK, 30, e)
}
