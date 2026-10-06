package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// NL-Alert: the public-warning messages that the government sends to phones in
// an area. Source: the JSON API behind actueel.nl-alert.nl (no key). Each alert
// carries its broadcast area as polygons, used to tell whether it reached the
// visitor's place.

type NLAlert struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`              // Dutch part of the message
	TextEN    string    `json:"text_en,omitempty"` // English part, after "***"
	Start     time.Time `json:"start"`
	Stop      time.Time `json:"stop,omitzero"`
	Withdrawn bool      `json:"withdrawn,omitempty"`
	Near      bool      `json:"near,omitempty"` // the visitor's place lies inside the area
	areas     [][][2]float64
}

var nlAlertIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func parseNLAlerts(body []byte) ([]NLAlert, error) {
	var r struct {
		Data *[]struct {
			ID      string   `json:"id"`
			Message string   `json:"message"`
			Type    string   `json:"type"`
			StartAt string   `json:"start_at"`
			StopAt  string   `json:"stop_at"`
			Area    []string `json:"area"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Data == nil {
		return nil, errors.New("nl-alert: no data field")
	}
	out := []NLAlert{}
	for _, a := range *r.Data {
		start, err := time.Parse(time.RFC3339, a.StartAt)
		if err != nil || !nlAlertIDRe.MatchString(a.ID) || (a.Type != "" && a.Type != "alert") {
			continue
		}
		nl, en, _ := strings.Cut(plainText(a.Message), "***")
		nl, en = strings.TrimSpace(nl), strings.TrimSpace(en)
		en = strings.TrimSpace(strings.TrimPrefix(en, "Dutch Public Warning System."))
		al := NLAlert{ID: a.ID, Text: truncate(nl, 600), TextEN: truncate(en, 600), Start: start.UTC(),
			Withdrawn: strings.HasPrefix(strings.ToLower(nl), "nl-alert ingetrokken")}
		if stop, err := time.Parse(time.RFC3339, a.StopAt); err == nil {
			al.Stop = stop.UTC()
		}
		for _, poly := range a.Area {
			var pts [][2]float64
			for _, p := range strings.Fields(poly) {
				la, lo, ok := strings.Cut(p, ",")
				lat, err1 := strconv.ParseFloat(la, 64)
				lon, err2 := strconv.ParseFloat(lo, 64)
				if ok && err1 == nil && err2 == nil {
					pts = append(pts, [2]float64{lat, lon})
				}
			}
			if len(pts) >= 3 {
				al.areas = append(al.areas, pts)
			}
		}
		out = append(out, al)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out, nil
}

// inArea reports whether the point lies inside one of the alert's polygons (ray casting).
func (al NLAlert) inArea(lat, lon float64) bool {
	for _, poly := range al.areas {
		in := false
		for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
			yi, xi, yj, xj := poly[i][0], poly[i][1], poly[j][0], poly[j][1]
			if (yi > lat) != (yj > lat) && lon < (xj-xi)*(lat-yi)/(yj-yi)+xi {
				in = !in
			}
		}
		if in {
			return true
		}
	}
	return false
}

func (al NLAlert) active(now time.Time) bool {
	return !al.Withdrawn && !al.Start.After(now) && (al.Stop.IsZero() || al.Stop.After(now))
}

func (a *App) handleNLAlert(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.NLAlert.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	lat, lon := cfg.Weather.Location.Lat, cfg.Weather.Location.Lon
	if q := r.URL.Query(); q.Get("lat") != "" || q.Get("lon") != "" {
		la, err1 := strconv.ParseFloat(q.Get("lat"), 64)
		lo, err2 := strconv.ParseFloat(q.Get("lon"), 64)
		if err1 != nil || err2 != nil || la < -90 || la > 90 || lo < -180 || lo > 180 {
			writeError(w, r, http.StatusBadRequest, "lat/lon ongeldig")
			return
		}
		lat, lon = la, lo
	}
	e := a.feedEntry("nlalert")
	e["enabled"] = true
	if v, ok := a.threats.get("nlalert").Data.([]NLAlert); ok {
		now := time.Now()
		list := make([]NLAlert, 0, len(v))
		active := 0
		for _, al := range v {
			if now.Sub(al.Start) > 14*24*time.Hour {
				continue
			}
			al.Near = al.inArea(lat, lon)
			if al.active(now) {
				active++
			}
			list = append(list, al)
		}
		e["alerts"], e["active"] = list, active
	}
	writeJSON(w, r, http.StatusOK, 60, e)
}

// ---------------------------------------------------------------------------
// Brandstofprijzen: the national average recommended pump price (GLA) per litre
// that UnitedConsumers publishes daily. There is no open API: the price table is
// read from the public page (personal use; see the README). Official CBS pump
// prices would be the open alternative, but its OData hosts are not reachable
// from every network.

type FuelPrice struct {
	Fuel   string  `json:"fuel"`   // euro95 | diesel | lpg
	Name   string  `json:"name"`   // Euro95 (E10), Diesel, LPG
	Price  float64 `json:"price"`  // euro per litre
	Change float64 `json:"change"` // cents compared with yesterday
}

type FuelData struct {
	Date   string      `json:"date,omitempty"` // YYYY-MM-DD of the overview
	Prices []FuelPrice `json:"prices"`
}

var (
	fuelProducts = []struct{ slug, name string }{{"euro95", "Euro95 (E10)"}, {"diesel", "Diesel"}, {"lpg", "LPG"}}
	fuelPriceRe  = regexp.MustCompile(`€(?:\s|&nbsp;|&#160;|\x{a0})*(\d),(\d{3})`)
	fuelChangeRe = regexp.MustCompile(`(?s)Verschil.*?</span>\s*([+\-−]?\s?\d{1,3},\d)\s*</div>`)
	fuelArrowRe  = regexp.MustCompile(`pijl-(omhoog|omlaag|linksrechts)`)
	fuelDateRe   = regexp.MustCompile(`Datum overzicht (?:<!-- -->)?\s*(\d{1,2}) ([a-z]+) (\d{4})`)
	dutchMonths  = map[string]time.Month{"januari": 1, "februari": 2, "maart": 3, "april": 4, "mei": 5, "juni": 6, "juli": 7,
		"augustus": 8, "september": 9, "oktober": 10, "november": 11, "december": 12}
)

func parseFuelPrices(body []byte) (FuelData, error) {
	s := string(body)
	d := FuelData{Prices: []FuelPrice{}}
	for _, p := range fuelProducts {
		i := strings.Index(s, `href="/tanken/brandstofprijzen/product/`+p.slug+`"`)
		if i < 0 {
			continue
		}
		seg := s[i:min(len(s), i+3000)]
		if next := strings.Index(seg[10:], `href="/tanken/brandstofprijzen/product/`); next > 0 {
			seg = seg[:next+10] // stay within this product's row
		}
		m := fuelPriceRe.FindStringSubmatchIndex(seg)
		if m == nil {
			continue
		}
		price, _ := strconv.ParseFloat(seg[m[2]:m[3]]+"."+seg[m[4]:m[5]], 64)
		if price < 0.3 || price > 6 {
			continue
		}
		fp := FuelPrice{Fuel: p.slug, Name: p.name, Price: price}
		rest := seg[m[1]:]
		if c := fuelChangeRe.FindStringSubmatch(rest); c != nil {
			v := strings.NewReplacer("−", "-", " ", "", ",", ".").Replace(c[1])
			if ch, err := strconv.ParseFloat(v, 64); err == nil {
				if a := fuelArrowRe.FindStringSubmatch(rest); a != nil && a[1] == "omlaag" && ch > 0 {
					ch = -ch
				}
				fp.Change = ch
			}
		}
		d.Prices = append(d.Prices, fp)
	}
	if len(d.Prices) == 0 {
		return d, errors.New("fuel: no prices found on the page")
	}
	if m := fuelDateRe.FindStringSubmatch(s); m != nil {
		day, _ := strconv.Atoi(m[1])
		year, _ := strconv.Atoi(m[3])
		if mon, ok := dutchMonths[m[2]]; ok && day >= 1 && day <= 31 {
			d.Date = time.Date(year, mon, day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		}
	}
	return d, nil
}

func (a *App) handleFuel(w http.ResponseWriter, r *http.Request) {
	if !a.config().Fuel.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("fuel:gla")
	e["enabled"] = true
	if v, ok := a.threats.get("fuel:gla").Data.(FuelData); ok {
		e["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 600, e)
}

// ---------------------------------------------------------------------------
// Afvalkalender: the next waste collection days. Like the places for weather and
// air quality, every visitor can set an own address (postcode and house number,
// kept in the browser); the server finds the municipal waste calendar that knows
// it. Supported: the REST API that many municipalities use for their calendar
// ("opzet", e.g. huisvuilkalender.denhaag.nl; the same API Home Assistant's
// afvalbeheer integration reads). The server's own default address can also come
// from an iCalendar link or from Home Assistant sensors.

type WastePickup struct {
	Type string `json:"type"`
	Date string `json:"date"` // YYYY-MM-DD
}

var (
	postcodeRe = regexp.MustCompile(`^[1-9][0-9]{3}[A-Z]{2}$`)
	bagIDRe    = regexp.MustCompile(`^[0-9]{1,20}$`)
)

func normPostcode(s string) string { return strings.ToUpper(strings.ReplaceAll(s, " ", "")) }

func hasWasteDefault(c *Config) bool {
	w := c.Waste
	switch w.Provider {
	case "ics":
		return w.ICSURL != ""
	case "home_assistant":
		return w.HomeAssistant.URL != ""
	default: // auto, opzet (1.10.0) or a provider id
		return w.Postcode != ""
	}
}

func (a *App) runWaste(ctx context.Context) error {
	const key = "waste:calendar"
	cfg := a.config().Waste
	var list []WastePickup
	var err error
	switch cfg.Provider {
	case "ics":
		var resp *FetchResp
		if resp, err = a.fetcher.Do(ctx, FetchReq{URL: cfg.ICSURL, Accept: "text/calendar, */*;q=0.5"}); err == nil {
			list, err = parseWasteICS(resp.Body, time.Now())
		}
	case "home_assistant":
		list, err = a.fetchHAWaste(ctx, cfg.HomeAssistant.URL, cfg.HomeAssistant.Token, cfg.HomeAssistant.Entities)
	default: // auto (or opzet) searches the providers; otherwise a provider id
		w := wasteAddr{Postcode: normPostcode(cfg.Postcode), Number: cfg.Number, Suffix: cfg.Suffix}
		if cfg.Provider != "auto" && cfg.Provider != "opzet" {
			w.Provider = cfg.Provider
		}
		var res WasteResult
		res, err = a.wasteForAddress(ctx, w)
		list = res.Pickups
	}
	if err != nil {
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	a.threats.ok(key, sortPickups(list, time.Now()), "", "")
	return nil
}

// sortPickups keeps today and later, soonest first, at most 12.
func sortPickups(list []WastePickup, now time.Time) []WastePickup {
	today := now.In(amsterdam).Format("2006-01-02")
	out := []WastePickup{}
	for _, p := range list {
		if p.Date >= today {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Type < out[j].Type
	})
	// only the next collection per waste type, not the repeating cycle
	next := out[:0]
	seen := map[string]bool{}
	for _, p := range out {
		if !seen[strings.ToLower(p.Type)] {
			seen[strings.ToLower(p.Type)] = true
			next = append(next, p)
		}
	}
	if len(next) > 12 {
		next = next[:12]
	}
	return next
}

func parseOpzetStreams(body []byte) ([]WastePickup, error) {
	var streams []struct {
		Title     string  `json:"title"`
		MenuTitle string  `json:"menu_title"`
		Date      *string `json:"ophaaldatum"`
	}
	if err := json.Unmarshal(body, &streams); err != nil {
		return nil, err
	}
	out := []WastePickup{}
	for _, s := range streams {
		if s.Date == nil {
			continue
		}
		d, err := time.Parse("2006-01-02", *s.Date)
		if err != nil {
			continue
		}
		name := firstNonEmpty(s.MenuTitle, s.Title)
		out = append(out, WastePickup{Type: truncate(plainText(name), 40), Date: d.Format("2006-01-02")})
	}
	return out, nil
}

// parseWasteICS reads VEVENTs (DTSTART + SUMMARY) from an iCalendar file.
func parseWasteICS(body []byte, now time.Time) ([]WastePickup, error) {
	s := strings.ReplaceAll(string(body), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n ", "") // unfold continuation lines
	s = strings.ReplaceAll(s, "\n\t", "")
	if !strings.Contains(s, "BEGIN:VCALENDAR") {
		return nil, errors.New("waste: not an iCalendar file")
	}
	out := []WastePickup{}
	var date, summary string
	in := false
	limit := now.AddDate(0, 3, 0).Format("2006-01-02")
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "BEGIN:VEVENT":
			in, date, summary = true, "", ""
		case line == "END:VEVENT":
			if in && date != "" && summary != "" && date <= limit {
				out = append(out, WastePickup{Type: truncate(summary, 40), Date: date})
			}
			in = false
		case in && strings.HasPrefix(line, "DTSTART"):
			_, v, _ := strings.Cut(line, ":")
			if len(v) >= 8 {
				if t, err := time.Parse("20060102", v[:8]); err == nil {
					date = t.Format("2006-01-02")
				}
			}
		case in && strings.HasPrefix(line, "SUMMARY"):
			_, v, _ := strings.Cut(line, ":")
			summary = plainText(strings.NewReplacer(`\,`, ",", `\;`, ";", `\n`, " ").Replace(v))
		}
	}
	return out, nil
}

func (a *App) fetchHAWaste(ctx context.Context, base, token string, entities []string) ([]WastePickup, error) {
	base = strings.TrimSuffix(base, "/")
	out := []WastePickup{}
	var errs []string
	for _, ent := range entities {
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "/api/states/" + url.PathEscape(ent), Accept: "application/json",
			Header: map[string]string{"Authorization": "Bearer " + token}})
		if err != nil {
			errs = append(errs, ent+": "+err.Error())
			continue
		}
		if p, ok := parseHAWasteState(resp.Body, time.Now()); ok {
			out = append(out, p)
		}
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.New("home assistant: " + strings.Join(errs, "; "))
	}
	return out, nil
}

// parseHAWasteState understands the sensors of the common waste integrations:
// a date as state (2026-10-02 or 02-10-2026), or a sort date / days-until attribute.
func parseHAWasteState(body []byte, now time.Time) (WastePickup, bool) {
	var st struct {
		EntityID   string         `json:"entity_id"`
		State      string         `json:"state"`
		Attributes map[string]any `json:"attributes"`
	}
	if json.Unmarshal(body, &st) != nil {
		return WastePickup{}, false
	}
	name, _ := st.Attributes["friendly_name"].(string)
	if name == "" {
		name = st.EntityID
	}
	p := WastePickup{Type: truncate(plainText(name), 40)}
	today := now.In(amsterdam)
	if t, err := time.Parse(time.RFC3339, st.State); err == nil {
		p.Date = t.In(amsterdam).Format("2006-01-02")
		return p, true
	}
	for _, layout := range []string{"2006-01-02", "02-01-2006"} {
		if len(st.State) >= 10 {
			if t, err := time.Parse(layout, st.State[:10]); err == nil {
				p.Date = t.Format("2006-01-02")
				return p, true
			}
		}
	}
	for _, k := range []string{"Sort-date", "sort_date", "sort-date"} {
		if f, ok := st.Attributes[k].(float64); ok && f > 20000101 {
			if t, err := time.Parse("20060102", strconv.Itoa(int(f))); err == nil {
				p.Date = t.Format("2006-01-02")
				return p, true
			}
		}
	}
	for _, k := range []string{"days_until_collection_date", "days_until", "Days-until", "days-until"} {
		var n float64
		switch v := st.Attributes[k].(type) {
		case float64:
			n = v
		case string:
			n, _ = strconv.ParseFloat(v, 64)
		default:
			continue
		}
		if n >= 0 && n < 400 {
			p.Date = today.AddDate(0, 0, int(n)).Format("2006-01-02")
			return p, true
		}
	}
	switch strings.ToLower(st.State) {
	case "vandaag", "today":
		p.Date = today.Format("2006-01-02")
		return p, true
	case "morgen", "tomorrow":
		p.Date = today.AddDate(0, 0, 1).Format("2006-01-02")
		return p, true
	}
	return p, false
}

func (a *App) handleWaste(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Waste.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	q := r.URL.Query()
	if q.Get("postcode") != "" || q.Get("number") != "" {
		addr, ok := parseWasteAddr(q.Get("postcode"), q.Get("number"), q.Get("suffix"))
		if pr := q.Get("provider"); pr != "" {
			addr.Provider = pr
			ok = ok && wasteProviderIDRe.MatchString(pr) && slices.ContainsFunc(enabledWasteProviders(cfg), func(p wasteProvider) bool { return p.ID == pr })
		}
		if !ok {
			writeError(w, r, http.StatusBadRequest, "postcode of huisnummer ongeldig")
			return
		}
		ip := a.clientIP(r)
		ctx := context.WithoutCancel(r.Context())
		res, at, stale, err := a.waste.get(addr.key(), cfg.Waste.Interval.D(), func() (WasteResult, error) {
			if !a.wx.limiter.allow(ip) {
				return WasteResult{}, errRateLimited
			}
			return a.wasteForAddress(ctx, addr)
		})
		switch {
		case errors.Is(err, errRateLimited):
			writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
			return
		case errors.Is(err, errWasteNotFound):
			writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": true, "own": true, "not_found": true})
			return
		case err != nil:
			writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": true, "own": true, "error": shortErr(err).Error()})
			return
		}
		e := map[string]any{"enabled": true, "own": true, "pickups": sortPickups(res.Pickups, time.Now()), "provider": res.Provider, "calendar": res.Name,
			"home": res.Home, "fetched_at": at.UTC().Truncate(time.Second)}
		if stale {
			e["error"] = "verouderd: de afvalkalender is nu niet bereikbaar"
		}
		writeJSON(w, r, http.StatusOK, 300, e)
		return
	}
	if !hasWasteDefault(cfg) {
		writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": true, "needs_address": true})
		return
	}
	e := a.feedEntry("waste:calendar")
	e["enabled"], e["provider"] = true, cfg.Waste.Provider
	if v, ok := a.threats.get("waste:calendar").Data.([]WastePickup); ok {
		e["pickups"] = sortPickups(v, time.Now())
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}

// ---------------------------------------------------------------------------
// Trending: words and word pairs that suddenly appear in the headlines of many
// sources. For every term: the number of distinct sources that used it in the
// last 3 hours, compared with its usual rate over the 2 days before.

type TrendTerm struct {
	Term    string `json:"term"`
	Sources int    `json:"sources"`
}

// trendStop are words too generic to be a topic on their own.
var trendStop = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`nieuws live video update updates foto fotos kijk lees vandaag gisteren morgen week weken
		maand maanden dag dagen uur minuten keer man vrouw vrouwen mannen kind kinderen mensen jongen meisje procent euro miljoen
		miljard duizend honderd twee drie vier vijf zes zeven acht negen tien waarom wordt eerst laatste grote groot kleine klein
		goed beter slecht minder zegt zeggen gaat blijft krijgt krijgen maakt maken staat staan ligt zien ziet laat laten
		maandag dinsdag woensdag donderdag vrijdag zaterdag zondag januari februari maart april mei juni juli augustus september
		oktober november december news first last year years week weeks day days people man woman live watch video photos
		report says could would should may might million billion percent monday tuesday wednesday thursday friday saturday
		sunday best worst big small top latest review deal deals one two three four five six seven eight nine ten`) {
		m[w] = true
	}
	return m
}()

// trendCache keeps the terms of all sources (recomputed at most every 5 min), the
// visitor-specific selection per chosen set of sources (at most 100 sets), and
// every term served in the last hour, for the Wikipedia lookup.
type trendCache struct {
	mu     sync.Mutex
	at     time.Time
	all    []TrendTerm
	sets   map[string]trendSet
	recent map[string]time.Time // lowercase term -> last served
}

type trendSet struct {
	at    time.Time
	terms []TrendTerm
}

func trendWords(title string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' }) {
		w = strings.Trim(w, "-")
		if utf8.RuneCountInString(w) < 3 || stopwords[w] || trendStop[w] || strings.IndexFunc(w, unicode.IsLetter) < 0 {
			out = append(out, "") // keeps pairs from spanning a removed word
			continue
		}
		out = append(out, w)
	}
	return out
}

// computeTrending returns up to n terms. Recent use counts distinct sources, the
// baseline counts articles (a term that is always in the news has many); each
// story appears once: a term found mostly in the same articles as an earlier
// choice is skipped.
func computeTrending(items []Item, now time.Time, n int) []TrendTerm {
	const recentWin, baseWin = 3 * time.Hour, 48 * time.Hour
	recent := map[string]map[string]bool{}            // term -> sources, last 3 h
	arts := map[string]map[int]bool{}                 // term -> recent article indexes
	base := map[string]int{}                          // term -> articles in the 48 h before
	display := map[string]map[string]int{}            // term -> original spellings
	mid, midCap := map[string]int{}, map[string]int{} // word -> uses after the first word, and how many capitalised
	for idx, it := range items {
		age := now.Sub(it.Published)
		if age < 0 || age > recentWin+baseWin {
			continue
		}
		words := trendWords(it.Title)
		orig := strings.FieldsFunc(it.Title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' })
		terms := map[string]bool{}
		for i, w := range words {
			if w == "" {
				continue
			}
			terms[w] = true
			if i+1 < len(words) && words[i+1] != "" {
				terms[w+" "+words[i+1]] = true
			}
		}
		if age <= recentWin {
			for i, w := range words {
				if i > 0 && w != "" && i < len(orig) {
					mid[w]++
					if r, _ := utf8.DecodeRuneInString(orig[i]); unicode.IsUpper(r) {
						midCap[w]++
					}
				}
			}
		}
		for term := range terms {
			if age > recentWin {
				base[term]++
				continue
			}
			if recent[term] == nil {
				recent[term], arts[term], display[term] = map[string]bool{}, map[int]bool{}, map[string]int{}
			}
			recent[term][it.Source] = true
			arts[term][idx] = true
			display[term][spelling(term, orig)]++
		}
	}
	type scored struct {
		term, show string
		srcs, caps int
		score      float64
	}
	var list []scored
	for term, srcs := range recent {
		k := len(srcs)
		if k < 3 {
			continue
		}
		expected := float64(base[term]) * float64(recentWin) / float64(baseWin)
		score := float64(k) / (1 + expected)
		if strings.Contains(term, " ") {
			score *= 1.3
		}
		if score < 2 {
			continue
		}
		// a topic needs a name (a word capitalised mid-headline: Oranjemars, Israël),
		// unless very many sources use it; this drops "boete", "incident", "politie"
		named := false
		for _, w := range strings.Fields(term) {
			if mid[w] > 0 && float64(midCap[w]) >= 0.6*float64(mid[w]) {
				named = true
			}
		}
		if !named && (k < 10 || score < 5) {
			continue
		}
		show, bn := term, 0
		for sp, c := range display[term] {
			if c > bn || (c == bn && sp < show) {
				show, bn = sp, c
			}
		}
		caps := 0
		for _, w := range strings.Fields(show) {
			if r, _ := utf8.DecodeRuneInString(w); unicode.IsUpper(r) {
				caps++
			}
		}
		list = append(list, scored{term, show, k, caps, score})
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.caps != b.caps { // names first: "Storm Ciarán" before "Ciarán raast"
			return a.caps > b.caps
		}
		return a.term < b.term
	})
	out := []TrendTerm{}
	var chosen []map[int]bool
	usedWord := map[string]bool{}
	for _, s := range list {
		dup := false
		for _, w := range strings.Fields(s.term) {
			dup = dup || usedWord[w]
		}
		for _, c := range chosen {
			shared := 0
			for i := range arts[s.term] {
				if c[i] {
					shared++
				}
			}
			if float64(shared) >= 0.6*float64(len(arts[s.term])) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		chosen = append(chosen, arts[s.term])
		for _, w := range strings.Fields(s.term) {
			usedWord[w] = true
		}
		out = append(out, TrendTerm{Term: s.show, Sources: s.srcs})
		if len(out) == n {
			break
		}
	}
	return out
}

// spelling finds the term's original capitalisation in the title's words.
func spelling(term string, orig []string) string {
	parts := strings.Fields(term)
	for i := range orig {
		if i+len(parts) > len(orig) {
			break
		}
		ok := true
		for j, p := range parts {
			if strings.ToLower(strings.Trim(orig[i+j], "-")) != p {
				ok = false
				break
			}
		}
		if ok {
			words := make([]string, len(parts))
			for j := range parts {
				words[j] = strings.Trim(orig[i+j], "-")
			}
			return strings.Join(words, " ")
		}
	}
	return term
}

// trending detects the terms across all sources (a strong signal: many outlets at
// once), then keeps those that occur in the visitor's own sources in the last 24
// hours, so every chip leads to articles the visitor can see.
func (a *App) trending(now time.Time, ids []string) []TrendTerm {
	key := strings.Join(slices.Sorted(slices.Values(ids)), ",")
	a.trend.mu.Lock()
	defer a.trend.mu.Unlock()
	if a.trend.sets == nil {
		a.trend.sets, a.trend.recent = map[string]trendSet{}, map[string]time.Time{}
	}
	if a.trend.all == nil || now.Sub(a.trend.at) >= 5*time.Minute {
		var every []string
		for _, s := range a.config().Sources {
			if s.IsEnabled() {
				every = append(every, s.ID)
			}
		}
		a.trend.all, a.trend.at = computeTrending(itemsOf(a.news, every), now, 30), now
		clear(a.trend.sets)
	}
	st, ok := a.trend.sets[key]
	if !ok {
		var hay []string
		for _, it := range itemsOf(a.news, ids) {
			if now.Sub(it.Published) <= 24*time.Hour {
				hay = append(hay, strings.ToLower(it.Title+" "+it.Summary))
			}
		}
		st = trendSet{at: now, terms: []TrendTerm{}}
		for _, t := range a.trend.all {
			low := strings.ToLower(t.Term)
			if slices.ContainsFunc(hay, func(h string) bool { return strings.Contains(h, low) }) {
				st.terms = append(st.terms, t)
				if len(st.terms) == 8 {
					break
				}
			}
		}
		if len(a.trend.sets) >= 100 {
			clear(a.trend.sets)
		}
		a.trend.sets[key] = st
	}
	for _, t := range st.terms {
		a.trend.recent[strings.ToLower(t.Term)] = now
	}
	for k, t := range a.trend.recent {
		if now.Sub(t) > time.Hour {
			delete(a.trend.recent, k)
		}
	}
	return st.terms
}

func itemsOf(c *NewsCache, ids []string) []Item {
	lists, _ := c.collect(ids)
	var all []Item
	for _, l := range lists {
		all = append(all, l...)
	}
	return all
}

// trendedRecently: the term was a trending chip for some visitor in the last hour.
func (a *App) trendedRecently(term string, now time.Time) (string, bool) {
	a.trend.mu.Lock()
	defer a.trend.mu.Unlock()
	t, ok := a.trend.recent[strings.ToLower(strings.TrimSpace(term))]
	return strings.TrimSpace(term), ok && now.Sub(t) <= time.Hour
}

func (a *App) handleTrending(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Trending.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": true, "terms": a.trending(time.Now(), requestedSources(cfg, r.URL.Query().Get("sources"))), "window_hours": 3})
}
