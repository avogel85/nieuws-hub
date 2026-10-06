package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Water (Aardbevingen en natuurrampen, Nederland tab): the water-safety codes of
// Rijkswaterstaat's Watermanagementcentrum per river, lake and coast sector, and
// the status of the storm-surge barriers. There is no documented API: these are
// the public JSON and HTML files that waterberichtgeving.rws.nl itself loads
// (the site refreshes them every 10 minutes). If their shape changes the parsers
// fail and the panel shows "unknown" rather than a guess.

type WaterSector struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Code int    `json:"code"` // 1 groen, 2 geel, 3 oranje, 4 rood; 0 = unknown
}

type WaterBarrier struct {
	Name   string `json:"name"`
	Open   bool   `json:"open"`
	Status string `json:"status"` // as published, e.g. "Geopend"
}

type WaterStatus struct {
	Level    int            `json:"level"`              // highest sector code now
	Sectors  []WaterSector  `json:"sectors"`            // all sectors, highest code first
	Peak     int            `json:"peak"`               // highest code expected in the next 24 hours
	PeakAt   *time.Time     `json:"peak_at,omitempty"`  // first hour with that code
	Barriers []WaterBarrier `json:"barriers,omitempty"` // storm-surge barriers at the coast
	Outlook  string         `json:"outlook,omitempty"`  // Rijkswaterstaat's short text for the coast
}

func validWaterCode(c int) bool { return c >= 1 && c <= 4 }

// parseWaterSectors reads /api/v1/sectorkaart/forecast?hour=0.
func parseWaterSectors(body []byte) ([]WaterSector, error) {
	var r struct {
		Sectors []struct {
			SectorName string `json:"sectorName"`
			SectorID   string `json:"sectorId"`
			StatusCode int    `json:"statusCode"`
		} `json:"sectors"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("rijkswaterstaat sectors: %w", err)
	}
	var out []WaterSector
	for _, s := range r.Sectors {
		if s.SectorName == "" {
			continue
		}
		c := s.StatusCode
		if !validWaterCode(c) {
			c = 0
		}
		out = append(out, WaterSector{ID: s.SectorID, Name: s.SectorName, Code: c})
	}
	if len(out) < 10 { // 21 sectors in 2026; far fewer means the format changed
		return nil, fmt.Errorf("rijkswaterstaat sectors: only %d sectors", len(out))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code > out[j].Code
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// parseWaterDay reads /api/v1/sectorkaart/forecast/day: the overall code per hour for the
// next 24 hours. It returns the highest code and the first hour with it.
func parseWaterDay(body []byte, now time.Time) (int, *time.Time, error) {
	var r struct {
		Statuses []struct {
			ForecastTime time.Time `json:"forecastTime"`
			StatusCode   int       `json:"statusCode"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, nil, fmt.Errorf("rijkswaterstaat forecast: %w", err)
	}
	peak, at := 0, (*time.Time)(nil)
	for _, s := range r.Statuses {
		if !validWaterCode(s.StatusCode) || s.ForecastTime.Before(now.Add(-time.Hour)) {
			continue
		}
		if s.StatusCode > peak {
			t := s.ForecastTime
			peak, at = s.StatusCode, &t
		}
	}
	if peak == 0 {
		return 0, nil, errors.New("rijkswaterstaat forecast: no hours")
	}
	return peak, at, nil
}

var (
	barrierRowRe  = regexp.MustCompile(`(?s)<tr>(.*?)</tr>`)
	barrierCellRe = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	barrierStatRe = regexp.MustCompile(`data-status="([^"]*)"[^>]*>([^<]*)<`)
	tagRe         = regexp.MustCompile(`<[^>]+>`)
)

func cellText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(s, " "))), " ")
}

// parseBarriers reads the table "Status stormvloedkeringen": one row per barrier with its
// name, the base station and a <p data-status="open">Geopend</p> cell.
func parseBarriers(body []byte) ([]WaterBarrier, error) {
	var out []WaterBarrier
	for _, row := range barrierRowRe.FindAllStringSubmatch(string(body), -1) {
		cells := barrierCellRe.FindAllStringSubmatch(row[1], -1)
		if len(cells) < 3 {
			continue // the header row has <th> cells
		}
		name := cellText(cells[0][1])
		m := barrierStatRe.FindStringSubmatch(cells[len(cells)-1][1])
		if name == "" || m == nil {
			continue
		}
		status := strings.TrimSpace(html.UnescapeString(m[2]))
		if status == "" {
			status = m[1]
		}
		out = append(out, WaterBarrier{Name: name, Open: strings.EqualFold(m[1], "open"), Status: status})
	}
	if len(out) == 0 {
		return nil, errors.New("rijkswaterstaat barriers: no rows")
	}
	return out, nil
}

// parseWaterText reads a short text product (the paragraphs of div.fews-product).
func parseWaterText(body []byte) string {
	s := string(body)
	if i := strings.Index(s, `class="fews-product"`); i >= 0 {
		if j := strings.IndexByte(s[i:], '>'); j >= 0 {
			s = s[i+j+1:]
		}
	}
	t := cellText(s)
	if len([]rune(t)) > 400 {
		t = string([]rune(t)[:399]) + "…"
	}
	return t
}

// runWater fetches the four files; the sector codes are required, the rest optional.
func (a *App) runWater(ctx context.Context) error {
	base := strings.TrimRight(a.config().World.WaterURL, "/")
	get := func(path, accept string) ([]byte, error) {
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + path, Accept: accept})
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
	fail := func(err error) error {
		a.threats.fail("rws:water", err)
		return err
	}
	b, err := get("/api/v1/sectorkaart/forecast?hour=0", "application/json")
	if err != nil {
		return fail(err)
	}
	st := WaterStatus{}
	if st.Sectors, err = parseWaterSectors(b); err != nil {
		return fail(err)
	}
	for _, s := range st.Sectors {
		st.Level = max(st.Level, s.Code)
	}
	if b, err := get("/api/v1/sectorkaart/forecast/day", "application/json"); err == nil {
		if p, at, err := parseWaterDay(b, time.Now()); err == nil {
			st.Peak, st.PeakAt = max(p, st.Level), at
		}
	}
	if b, err := get("/data/orbit/noordzee_tabel_status_keringen/index.html", "text/html"); err == nil {
		st.Barriers, _ = parseBarriers(b)
	}
	if b, err := get("/data/orbit/noordzee_tekst_waterbeeld/index.html", "text/html"); err == nil {
		st.Outlook = parseWaterText(b)
	}
	a.threats.ok("rws:water", st, "", "")
	return nil
}

// waterAlarm: what the top bar and the push topic react to (code oranje or rood, or a
// closed storm-surge barrier). Empty strings when there is nothing to report.
func waterAlarm(st WaterStatus) (sectors, barriers []string) {
	for _, s := range st.Sectors {
		if s.Code >= 3 {
			sectors = append(sectors, s.Name)
		}
	}
	for _, b := range st.Barriers {
		if !b.Open {
			barriers = append(barriers, b.Name)
		}
	}
	return
}

// ---------------------------------------------------------------------------
// Ruimteweer (Vanavond aan de hemel): the NOAA space-weather scales (R radio blackouts,
// S solar radiation storms, G geomagnetic storms) now and for the next three days, and
// the strongest solar flare of the last 24 hours, from NOAA SWPC (public domain).

type SpaceScale struct {
	Date  string `json:"date"`
	Now   bool   `json:"now,omitempty"` // the observed values of today; otherwise a forecast
	R     int    `json:"r"`             // 0-5 (observed) or -1 (forecast: see the probabilities)
	S     int    `json:"s"`
	G     int    `json:"g"`
	RProb int    `json:"r_prob,omitempty"` // forecast: % chance of R1-R2
	RMaj  int    `json:"r_major,omitempty"`
	SProb int    `json:"s_prob,omitempty"`
}

type SpaceFlare struct {
	Class string    `json:"class"` // e.g. "M1.2"
	At    time.Time `json:"at"`
}

type SpaceWeather struct {
	Days  []SpaceScale `json:"days"`
	Flare *SpaceFlare  `json:"flare,omitempty"` // strongest of the last 24 hours
}

func atoiOr(s *string, def int) int {
	if s == nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(*s))
	if err != nil || n < 0 || n > 100 {
		return def
	}
	return n
}

// parseSpaceScales reads products/noaa-scales.json: "0" is today (observed), "1"-"3" the
// forecast; "-1" (yesterday) is skipped.
func parseSpaceScales(body []byte) ([]SpaceScale, error) {
	type val struct {
		Scale     *string `json:"Scale"`
		Prob      *string `json:"Prob"`
		MinorProb *string `json:"MinorProb"`
		MajorProb *string `json:"MajorProb"`
	}
	var r map[string]struct {
		DateStamp string `json:"DateStamp"`
		R, S, G   val
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("noaa scales: %w", err)
	}
	var out []SpaceScale
	for i := 0; i <= 3; i++ {
		d, ok := r[strconv.Itoa(i)]
		if !ok || d.DateStamp == "" {
			continue
		}
		sc := SpaceScale{Date: d.DateStamp, Now: i == 0, R: atoiOr(d.R.Scale, -1), S: atoiOr(d.S.Scale, -1), G: atoiOr(d.G.Scale, -1),
			RProb: atoiOr(d.R.MinorProb, 0), RMaj: atoiOr(d.R.MajorProb, 0), SProb: atoiOr(d.S.Prob, 0)}
		for _, p := range []*int{&sc.R, &sc.S, &sc.G} {
			if *p > 5 {
				*p = -1
			}
		}
		out = append(out, sc)
	}
	if len(out) == 0 || !out[0].Now {
		return nil, errors.New("noaa scales: no data for today")
	}
	return out, nil
}

// flareRank orders GOES classes: A < B < C < M < X, then by the number (X10 > X9.3).
func flareRank(c string) float64 {
	if len(c) < 2 {
		return -1
	}
	i := strings.IndexByte("ABCMX", c[0])
	n, err := strconv.ParseFloat(c[1:], 64)
	if i < 0 || err != nil {
		return -1
	}
	return float64(i)*1000 + n
}

// parseFlares reads json/goes/primary/xray-flares-7-day.json and returns the strongest flare
// that peaked in the last 24 hours (nil when there was none).
func parseFlares(body []byte, now time.Time) (*SpaceFlare, error) {
	var list []struct {
		MaxClass string    `json:"max_class"`
		MaxTime  time.Time `json:"max_time"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("noaa flares: %w", err)
	}
	var best *SpaceFlare
	for _, f := range list {
		if now.Sub(f.MaxTime) > 24*time.Hour || f.MaxTime.After(now.Add(time.Hour)) || flareRank(f.MaxClass) < 0 {
			continue
		}
		if best == nil || flareRank(f.MaxClass) > flareRank(best.Class) {
			best = &SpaceFlare{Class: f.MaxClass, At: f.MaxTime}
		}
	}
	return best, nil
}

func (a *App) runSpaceWeather(ctx context.Context) error {
	base := strings.TrimRight(a.config().Sky.SpaceWeatherURL, "/")
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "/products/noaa-scales.json", Accept: "application/json"})
	var sw SpaceWeather
	if err == nil {
		sw.Days, err = parseSpaceScales(resp.Body)
	}
	if err != nil {
		a.threats.fail("noaa:space", err)
		return err
	}
	if resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "/json/goes/primary/xray-flares-7-day.json", Accept: "application/json"}); err == nil {
		sw.Flare, _ = parseFlares(resp.Body, time.Now())
	}
	a.threats.ok("noaa:space", sw, "", "")
	return nil
}
