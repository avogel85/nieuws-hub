package main

// panels.go — data for the side panels: weather (Open-Meteo, Buienradar, MeteoAlarm),
// threat intelligence (SANS ISC, abuse.ch Feodo, CISA KEV, ip-api), security advisories (NCSC),
// top-bar alerts (NCTV, KNMI), traffic (NDW) and outages of online services.

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"container/list"
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ---------------------------------------------------------------------------
// ttlCache: in-memory cache with per-key single flight and stale fallback.

var errRateLimited = errors.New("rate limited")

const maxStale = 6 * time.Hour

type ttlEntry[V any] struct {
	mu  sync.Mutex
	val V
	at  time.Time
	ok  bool
}

type ttlCache[V any] struct {
	mu  sync.Mutex
	m   map[string]*ttlEntry[V]
	max int
}

func newTTLCache[V any](max int) *ttlCache[V] {
	return &ttlCache[V]{m: map[string]*ttlEntry[V]{}, max: max}
}

// get returns a cached value younger than ttl, or calls fetch. Concurrent callers
// for the same key share one fetch. If fetch fails, a value up to 6 h old is
// returned with stale=true.
func (c *ttlCache[V]) get(key string, ttl time.Duration, fetch func() (V, error)) (v V, at time.Time, stale bool, err error) {
	c.mu.Lock()
	e := c.m[key]
	if e == nil {
		if len(c.m) >= c.max {
			c.evictOldest()
		}
		e = &ttlEntry[V]{}
		c.m[key] = e
	}
	c.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ok && time.Since(e.at) < ttl {
		return e.val, e.at, false, nil
	}
	nv, err := fetch()
	if err == nil {
		e.val, e.at, e.ok = nv, time.Now(), true
		return nv, e.at, false, nil
	}
	if e.ok && time.Since(e.at) < maxStale && !errors.Is(err, errRateLimited) {
		return e.val, e.at, true, nil
	}
	return v, time.Time{}, false, err
}

// has reports whether key was ever requested (and not evicted since).
func (c *ttlCache[V]) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[key]
	return ok
}

func (c *ttlCache[V]) evictOldest() {
	var oldest string
	var at time.Time
	for k, e := range c.m {
		if oldest == "" || e.at.Before(at) {
			oldest, at = k, e.at
		}
	}
	delete(c.m, oldest)
}

// ---------------------------------------------------------------------------
// Per-IP token bucket, applied only to requests that cause an upstream fetch.

type rateLimiter struct {
	mu        sync.Mutex
	perMin    float64
	burst     float64
	buckets   map[netip.Addr]*bucket
	lastPrune time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMin, burst float64) *rateLimiter {
	return &rateLimiter{perMin: perMin, burst: burst, buckets: map[netip.Addr]*bucket{}}
}

func (l *rateLimiter) allow(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	// Prune idle buckets at most once a minute: scanning on every request would
	// make a flood of distinct addresses cost O(n) per request.
	if len(l.buckets) > 10000 && now.Sub(l.lastPrune) > time.Minute {
		l.lastPrune = now
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	if len(l.buckets) > 50000 { // flood of distinct addresses: start over rather than grow without bound
		l.buckets = map[netip.Addr]*bucket{}
	}
	b := l.buckets[ip]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Minutes()*l.perMin)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ---------------------------------------------------------------------------
// Weather types (JSON sent to the browser)

type WxCurrent struct {
	Time     int64   `json:"time"`
	Temp     float64 `json:"temp"`
	Feels    float64 `json:"feels"`
	Humidity int     `json:"humidity"`
	Precip   float64 `json:"precip"`
	Code     int     `json:"code"`
	Desc     string  `json:"desc"`
	Icon     string  `json:"icon"`
	IsDay    bool    `json:"is_day"`
	WindKmh  float64 `json:"wind_kmh"`
	GustKmh  float64 `json:"gust_kmh"`
	WindDir  string  `json:"wind_dir"`
	Beaufort int     `json:"bft"`
	BftName  string  `json:"bft_name"`
}

type WxHour struct {
	Time       int64   `json:"time"`
	Temp       float64 `json:"temp"`
	PrecipProb *int    `json:"precip_prob,omitempty"`
	Precip     float64 `json:"precip"`
	Code       int     `json:"code"`
	Desc       string  `json:"desc"`
	Icon       string  `json:"icon"`
	Beaufort   int     `json:"bft"`
}

type WxDay struct {
	Date       int64   `json:"date"`
	Code       int     `json:"code"`
	Desc       string  `json:"desc"`
	Icon       string  `json:"icon"`
	Min        float64 `json:"min"`
	Max        float64 `json:"max"`
	PrecipSum  float64 `json:"precip_sum"`
	PrecipProb *int    `json:"precip_prob,omitempty"`
	Beaufort   int     `json:"bft"`
	WindDir    string  `json:"wind_dir,omitempty"`
	Sunrise    int64   `json:"sunrise"`
	Sunset     int64   `json:"sunset"`
	UV         float64 `json:"uv"`
}

type WxForecast struct {
	Current WxCurrent `json:"current"`
	Hourly  []WxHour  `json:"hourly"`
	Daily   []WxDay   `json:"daily"`
	Thunder WxThunder `json:"thunder"`      // next 24 hours
	UV      *WxUV     `json:"uv,omitempty"` // highest UV in the next 24 hours
}

type RainPoint struct {
	Time string  `json:"time"` // "HH:MM", Dutch local time
	MMH  float64 `json:"mmh"`
}

type WxRain struct {
	Points  []RainPoint `json:"points"`
	Summary string      `json:"summary"`
	MaxMMH  float64     `json:"max_mmh"`
}

type WxWarning struct {
	Level   string    `json:"level"` // yellow | orange | red
	Type    string    `json:"type"`  // Dutch, e.g. "Wind"
	Area    string    `json:"area"`
	Country string    `json:"country"`
	Onset   time.Time `json:"onset"`
	Expires time.Time `json:"expires"`
	URL     string    `json:"url,omitempty"`
	Here    bool      `json:"here"` // matches the requested region
}

type WeatherResp struct {
	Location struct {
		Name    string  `json:"name,omitempty"`
		Region  string  `json:"region,omitempty"`
		Country string  `json:"country,omitempty"`
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
	} `json:"location"`
	WxForecast
	Rain     *WxRain           `json:"rain,omitempty"`
	Warnings []WxWarning       `json:"warnings"`
	Updated  time.Time         `json:"updated"`
	Stale    bool              `json:"stale,omitempty"`
	Errors   map[string]string `json:"errors,omitempty"`
}

// ---------------------------------------------------------------------------
// Dutch descriptions, icons, Beaufort

// wmoDesc maps WMO weather codes (Open-Meteo) to a Dutch description and icon key.
func wmoDesc(code int, day bool) (string, string) {
	switch code {
	case 0:
		if day {
			return "Zonnig", "sun"
		}
		return "Helder", "moon"
	case 1:
		if day {
			return "Overwegend zonnig", "partly-day"
		}
		return "Overwegend helder", "partly-night"
	case 2:
		if day {
			return "Half bewolkt", "partly-day"
		}
		return "Half bewolkt", "partly-night"
	case 3:
		return "Bewolkt", "cloud"
	case 45:
		return "Mist", "fog"
	case 48:
		return "Mist met rijp", "fog"
	case 51:
		return "Lichte motregen", "drizzle"
	case 53:
		return "Motregen", "drizzle"
	case 55:
		return "Dichte motregen", "drizzle"
	case 56:
		return "Lichte motregen met ijzel", "sleet"
	case 57:
		return "Motregen met ijzel", "sleet"
	case 61:
		return "Lichte regen", "rain"
	case 63:
		return "Regen", "rain"
	case 65:
		return "Zware regen", "rain"
	case 66:
		return "Lichte ijzel", "sleet"
	case 67:
		return "IJzel", "sleet"
	case 71:
		return "Lichte sneeuw", "snow"
	case 73:
		return "Sneeuw", "snow"
	case 75:
		return "Zware sneeuw", "snow"
	case 77:
		return "Korrelsneeuw", "snow"
	case 80:
		return "Lichte regenbuien", "showers"
	case 81:
		return "Regenbuien", "showers"
	case 82:
		return "Zware regenbuien", "showers"
	case 85:
		return "Lichte sneeuwbuien", "snow"
	case 86:
		return "Zware sneeuwbuien", "snow"
	case 95:
		return "Onweer", "thunder"
	case 96:
		return "Onweer met hagel", "thunder"
	case 99:
		return "Zwaar onweer met hagel", "thunder"
	}
	return "Onbekend", "cloud"
}

// Beaufort upper bounds in m/s (KNMI table).
var bftLimits = []float64{0.3, 1.6, 3.4, 5.5, 8.0, 10.8, 13.9, 17.2, 20.8, 24.5, 28.5, 32.7}

func beaufort(kmh float64) int {
	ms := kmh / 3.6
	for i, lim := range bftLimits {
		if ms < lim {
			return i
		}
	}
	return 12
}

var bftNames = []string{"windstil", "zwak", "zwak", "matig", "matig", "vrij krachtig", "krachtig", "hard",
	"stormachtig", "storm", "zware storm", "zeer zware storm", "orkaan"}

var compassNL = []string{"N", "NNO", "NO", "ONO", "O", "OZO", "ZO", "ZZO", "Z", "ZZW", "ZW", "WZW", "W", "WNW", "NW", "NNW"}

// windDir converts degrees (direction the wind comes from) to a Dutch compass point.
func windDir(deg float64) string {
	i := int(math.Mod(deg+11.25+360, 360) / 22.5)
	return compassNL[i%16]
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// ---------------------------------------------------------------------------
// Open-Meteo forecast

type omResp struct {
	Current struct {
		Time     int64   `json:"time"`
		Temp     float64 `json:"temperature_2m"`
		Feels    float64 `json:"apparent_temperature"`
		Humidity float64 `json:"relative_humidity_2m"`
		Precip   float64 `json:"precipitation"`
		Code     int     `json:"weather_code"`
		Wind     float64 `json:"wind_speed_10m"`
		Dir      float64 `json:"wind_direction_10m"`
		Gust     float64 `json:"wind_gusts_10m"`
		IsDay    int     `json:"is_day"`
	} `json:"current"`
	Hourly struct {
		Time   []int64    `json:"time"`
		Temp   []float64  `json:"temperature_2m"`
		Prob   []*float64 `json:"precipitation_probability"`
		Precip []float64  `json:"precipitation"`
		Code   []int      `json:"weather_code"`
		Wind   []float64  `json:"wind_speed_10m"`
		IsDay  []int      `json:"is_day"`
		LPI    []*float64 `json:"lightning_potential"`
		CAPE   []*float64 `json:"cape"`
		UV     []*float64 `json:"uv_index"`
	} `json:"hourly"`
	Daily struct {
		Time    []int64    `json:"time"`
		Code    []int      `json:"weather_code"`
		Max     []float64  `json:"temperature_2m_max"`
		Min     []float64  `json:"temperature_2m_min"`
		Precip  []float64  `json:"precipitation_sum"`
		Prob    []*float64 `json:"precipitation_probability_max"`
		WindMax []float64  `json:"wind_speed_10m_max"`
		Dir     []*float64 `json:"wind_direction_10m_dominant"`
		Sunrise []int64    `json:"sunrise"`
		Sunset  []int64    `json:"sunset"`
		UV      []*float64 `json:"uv_index_max"`
	} `json:"daily"`
}

const openMeteoURL = "https://api.open-meteo.com/v1/forecast?current=temperature_2m,apparent_temperature,relative_humidity_2m," +
	"precipitation,weather_code,wind_speed_10m,wind_direction_10m,wind_gusts_10m,is_day" +
	"&hourly=temperature_2m,precipitation_probability,precipitation,weather_code,wind_speed_10m,is_day,lightning_potential,cape,uv_index" +
	"&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max," +
	"wind_speed_10m_max,wind_direction_10m_dominant,sunrise,sunset,uv_index_max" +
	"&timezone=auto&timeformat=unixtime&forecast_days=7&forecast_hours=24&wind_speed_unit=kmh"

func (a *App) fetchForecast(ctx context.Context, lat, lon float64) (WxForecast, error) {
	u := fmt.Sprintf("%s&latitude=%.2f&longitude=%.2f", openMeteoURL, lat, lon)
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
	if err != nil {
		return WxForecast{}, fmt.Errorf("open-meteo: %w", err)
	}
	var om omResp
	if err := json.Unmarshal(resp.Body, &om); err != nil {
		return WxForecast{}, fmt.Errorf("open-meteo: %w", err)
	}
	return convertForecast(&om)
}

func optInt(p []*float64, i int) *int {
	if i < len(p) && p[i] != nil {
		v := int(math.Round(*p[i]))
		return &v
	}
	return nil
}

func at[T any](s []T, i int) T {
	var zero T
	if i < len(s) {
		return s[i]
	}
	return zero
}

func convertForecast(om *omResp) (WxForecast, error) {
	if om.Current.Time == 0 || len(om.Daily.Time) == 0 {
		return WxForecast{}, errors.New("open-meteo: incomplete response")
	}
	var f WxForecast
	c := om.Current
	f.Current = WxCurrent{
		Time: c.Time, Temp: round1(c.Temp), Feels: round1(c.Feels), Humidity: int(math.Round(c.Humidity)),
		Precip: round1(c.Precip), Code: c.Code, IsDay: c.IsDay == 1,
		WindKmh: math.Round(c.Wind), GustKmh: math.Round(c.Gust), WindDir: windDir(c.Dir), Beaufort: beaufort(c.Wind),
	}
	f.Current.Desc, f.Current.Icon = wmoDesc(c.Code, c.IsDay == 1)
	f.Current.BftName = bftNames[f.Current.Beaufort]
	hh := om.Hourly
	for i, t := range hh.Time {
		h := WxHour{Time: t, Temp: round1(at(hh.Temp, i)), PrecipProb: optInt(hh.Prob, i), Precip: round1(at(hh.Precip, i)),
			Code: at(hh.Code, i), Beaufort: beaufort(at(hh.Wind, i))}
		h.Desc, h.Icon = wmoDesc(h.Code, at(hh.IsDay, i) == 1)
		f.Hourly = append(f.Hourly, h)
	}
	f.Thunder = computeThunder(hh.Time, hh.LPI, hh.CAPE, hh.Code)
	f.UV = computeUV(hh.Time, hh.UV)
	dd := om.Daily
	for i, t := range dd.Time {
		d := WxDay{Date: t, Code: at(dd.Code, i), Min: round1(at(dd.Min, i)), Max: round1(at(dd.Max, i)),
			PrecipSum: round1(at(dd.Precip, i)), PrecipProb: optInt(dd.Prob, i), Beaufort: beaufort(at(dd.WindMax, i)),
			Sunrise: at(dd.Sunrise, i), Sunset: at(dd.Sunset, i)}
		if p := at(dd.Dir, i); p != nil {
			d.WindDir = windDir(*p)
		}
		if p := at(dd.UV, i); p != nil {
			d.UV = round1(*p)
		}
		d.Desc, d.Icon = wmoDesc(d.Code, true)
		f.Daily = append(f.Daily, d)
	}
	return f, nil
}

// ---------------------------------------------------------------------------
// Buienradar: precipitation for the next two hours (NL/BE only)

func (a *App) fetchRain(ctx context.Context, lat, lon float64) (WxRain, error) {
	u := fmt.Sprintf("https://gpsgadget.buienradar.nl/data/raintext?lat=%.2f&lon=%.2f", lat, lon)
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "text/plain"})
	if err != nil {
		return WxRain{}, fmt.Errorf("buienradar: %w", err)
	}
	return parseRainText(string(resp.Body))
}

// parseRainText parses "vvv|HH:MM" lines; intensity in mm/h = 10^((v-109)/32).
func parseRainText(body string) (WxRain, error) {
	var r WxRain
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		v, t, ok := strings.Cut(strings.TrimSpace(line), "|")
		n, err := strconv.Atoi(strings.TrimSpace(v))
		t = strings.TrimSpace(t)
		if !ok || err != nil || n < 0 || n > 255 || len(t) != 5 || t[2] != ':' {
			continue
		}
		mm := 0.0
		if n > 0 {
			mm = math.Round(math.Pow(10, float64(n-109)/32)*100) / 100
		}
		r.Points = append(r.Points, RainPoint{Time: t, MMH: mm})
		r.MaxMMH = math.Max(r.MaxMMH, mm)
	}
	if len(r.Points) < 6 {
		return WxRain{}, errors.New("buienradar: unexpected response")
	}
	r.Summary = rainSummary(r.Points)
	return r, nil
}

const rainThreshold = 0.1 // mm/h below this counts as dry

func rainIntensity(mm float64) string {
	switch {
	case mm < 1:
		return "lichte regen"
	case mm < 5:
		return "matige regen"
	default:
		return "zware regen"
	}
}

// rainSummary produces one Dutch sentence describing the next two hours.
func rainSummary(pts []RainPoint) string {
	wet := func(p RainPoint) bool { return p.MMH >= rainThreshold }
	last := pts[len(pts)-1].Time
	first := -1
	for i, p := range pts {
		if wet(p) {
			first = i
			break
		}
	}
	if first < 0 {
		return "Droog tot " + last + "."
	}
	peak := 0.0
	end := -1
	for i := first; i < len(pts); i++ {
		if !wet(pts[i]) {
			end = i
			break
		}
		peak = math.Max(peak, pts[i].MMH)
	}
	kind := rainIntensity(peak)
	switch {
	case first == 0 && end < 0:
		return capitalize(kind) + " tot minstens " + last + "."
	case first == 0:
		return capitalize(kind) + ", droog vanaf " + pts[end].Time + "."
	case end < 0:
		return capitalize(kind) + " vanaf " + pts[first].Time + "."
	default:
		return capitalize(kind) + " tussen " + pts[first].Time + " en " + pts[end].Time + "."
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---------------------------------------------------------------------------
// MeteoAlarm warnings (legacy Atom feed with CAP fields)

type maFeed struct {
	Entries []struct {
		Title    string    `xml:"title"`
		AreaDesc string    `xml:"areaDesc"`
		Event    string    `xml:"event"`
		Severity string    `xml:"severity"`
		Onset    string    `xml:"onset"`
		Expires  string    `xml:"expires"`
		MsgType  string    `xml:"message_type"`
		Links    []xmlLink `xml:"link"`
	} `xml:"entry"`
}

var warningTypesNL = []struct{ key, nl string }{
	{"thunder", "Onweer"}, {"snow", "Sneeuw en ijzel"}, {"ice", "Sneeuw en ijzel"}, {"wind", "Wind"},
	{"fog", "Mist"}, {"high temp", "Hitte"}, {"heat", "Hitte"}, {"low temp", "Kou"}, {"cold", "Kou"},
	{"coast", "Kust"}, {"forest", "Natuurbrand"}, {"fire", "Natuurbrand"}, {"avalanch", "Lawines"},
	{"rain-flood", "Regen en overstroming"}, {"flood", "Overstroming"}, {"rain", "Regen"},
}

func warningTypeNL(s string) string {
	s = strings.ToLower(s)
	for _, t := range warningTypesNL {
		if strings.Contains(s, t.key) {
			return t.nl
		}
	}
	return "Weer"
}

func parseMeteoAlarm(body []byte, country string, now time.Time) ([]WxWarning, error) {
	var f maFeed
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("meteoalarm: %w", err)
	}
	var out []WxWarning
	seen := map[string]bool{}
	for _, e := range f.Entries {
		if strings.EqualFold(e.MsgType, "Cancel") {
			continue
		}
		title := strings.ToLower(e.Title)
		level := ""
		switch {
		case strings.HasPrefix(title, "red"):
			level = "red"
		case strings.HasPrefix(title, "orange"):
			level = "orange"
		case strings.HasPrefix(title, "yellow"):
			level = "yellow"
		default:
			switch strings.ToLower(e.Severity) {
			case "extreme":
				level = "red"
			case "severe":
				level = "orange"
			case "moderate":
				level = "yellow"
			}
		}
		if level == "" {
			continue // green / minor without a colour: no warning
		}
		w := WxWarning{Level: level, Area: strings.TrimSpace(e.AreaDesc), Country: country}
		// "Yellow Wind Warning issued for …" → type from the title, else from cap:event
		kind := e.Event
		if i := strings.Index(title, " warning"); i > 0 {
			if _, rest, ok := strings.Cut(title[:i], " "); ok {
				kind = rest
			}
		}
		w.Type = warningTypeNL(kind)
		w.Onset, _ = time.Parse(time.RFC3339, strings.TrimSpace(e.Onset))
		w.Expires, _ = time.Parse(time.RFC3339, strings.TrimSpace(e.Expires))
		if !w.Expires.IsZero() && w.Expires.Before(now) {
			continue
		}
		for _, l := range e.Links {
			if l.Rel == "" && strings.HasPrefix(l.Href, "https://meteoalarm.org") {
				w.URL = l.Href
				break
			}
		}
		k := w.Level + w.Type + w.Area + w.Onset.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, w)
	}
	return out, nil
}

func (a *App) fetchWarnings(ctx context.Context, country string) ([]WxWarning, error) {
	feed := a.config().Weather.MeteoAlarm[country]
	if feed == "" {
		return nil, nil
	}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: feed, Accept: feedAccept}) // a strict atom Accept gets HTTP 406
	if err != nil {
		return nil, fmt.Errorf("meteoalarm: %w", err)
	}
	return parseMeteoAlarm(resp.Body, country, time.Now())
}

// countriesFor returns the warning/radar countries (nl, be) that may cover a location.
func countriesFor(lat, lon float64, cc string) []string {
	switch strings.ToLower(cc) {
	case "nl":
		return []string{"nl"}
	case "be":
		return []string{"be"}
	case "":
	default:
		return nil
	}
	var out []string
	// Netherlands: main area north of 51.2°N, plus the southern Limburg strip.
	if (lat >= 51.2 && lat <= 53.7 && lon >= 3.2 && lon <= 7.3) || (lat >= 50.7 && lat < 51.2 && lon >= 5.6 && lon <= 6.3) {
		out = append(out, "nl")
	}
	// Belgium: up to 51.51°N, but the coast west of 4.3°E stops at 51.38°N (Zeeland is NL).
	if lat >= 49.4 && lat <= 51.51 && lon >= 2.5 && lon <= 6.5 && (lon >= 4.3 || lat <= 51.38) {
		out = append(out, "be")
	}
	return out
}

func normArea(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer("fryslân", "friesland", "provincie ", "", "province de ", "", "provincie", "").Replace(s)
}

func regionMatches(area, region string) bool {
	if region == "" || area == "" {
		return false
	}
	a, r := normArea(area), normArea(region)
	return a == r || strings.Contains(a, r) || strings.Contains(r, a)
}

// ---------------------------------------------------------------------------
// Handlers

type weatherCaches struct {
	forecast *ttlCache[WxForecast]
	rain     *ttlCache[WxRain]
	warnings *ttlCache[[]WxWarning]
	geocode  *ttlCache[[]GeoResult]
	limiter  *rateLimiter
}

func newWeatherCaches() *weatherCaches {
	return &weatherCaches{
		forecast: newTTLCache[WxForecast](500),
		rain:     newTTLCache[WxRain](500),
		warnings: newTTLCache[[]WxWarning](4),
		geocode:  newTTLCache[[]GeoResult](1000),
		limiter:  newRateLimiter(20, 30), // upstream fetches per client IP: 20/min, burst 30
	}
}

func parseCoord(s string, limit float64) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.Abs(f) > limit {
		return 0, false
	}
	return math.Round(f*100) / 100, true
}

func (a *App) handleWeather(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	q := r.URL.Query()
	var resp WeatherResp
	loc := cfg.Weather.Location
	lat, lon := math.Round(loc.Lat*100)/100, math.Round(loc.Lon*100)/100
	resp.Location.Name, resp.Location.Region, resp.Location.Country = loc.Name, loc.Region, strings.ToUpper(loc.Country)
	if q.Get("lat") != "" || q.Get("lon") != "" {
		var ok1, ok2 bool
		lat, ok1 = parseCoord(q.Get("lat"), 90)
		lon, ok2 = parseCoord(q.Get("lon"), 180)
		if !ok1 || !ok2 {
			writeError(w, r, http.StatusBadRequest, "lat/lon ongeldig")
			return
		}
		resp.Location.Name = ""
		resp.Location.Region = truncate(plainText(q.Get("region")), 60)
		resp.Location.Country = ""
		if cc := strings.ToUpper(q.Get("cc")); len(cc) == 2 && cc[0] >= 'A' && cc[0] <= 'Z' && cc[1] >= 'A' && cc[1] <= 'Z' {
			resp.Location.Country = cc
		}
	}
	resp.Location.Lat, resp.Location.Lon = lat, lon
	key := fmt.Sprintf("%.2f,%.2f", lat, lon)
	ip := a.clientIP(r)
	ctx := context.WithoutCancel(r.Context()) // a shared fetch must not die with one client
	limited := func() bool { return !a.wx.limiter.allow(ip) }

	fc, at, stale, err := a.wx.forecast.get(key, cfg.Weather.Interval.D(), func() (WxForecast, error) {
		if limited() {
			return WxForecast{}, errRateLimited
		}
		return a.fetchForecast(ctx, lat, lon)
	})
	if errors.Is(err, errRateLimited) {
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
		return
	}
	if err != nil {
		slog.Warn("weather fetch failed", "loc", key, "err", err)
		writeError(w, r, http.StatusBadGateway, "Open-Meteo is niet bereikbaar")
		return
	}
	resp.WxForecast, resp.Updated, resp.Stale = fc, at.UTC().Truncate(time.Second), stale
	resp.Warnings = []WxWarning{}
	resp.Errors = map[string]string{}

	countries := countriesFor(lat, lon, resp.Location.Country)
	if len(countries) > 0 {
		rain, _, _, err := a.wx.rain.get(key, 5*time.Minute, func() (WxRain, error) {
			if limited() {
				return WxRain{}, errRateLimited
			}
			return a.fetchRain(ctx, lat, lon)
		})
		if err != nil {
			resp.Errors["rain"] = "Buienradar is niet bereikbaar"
		} else {
			resp.Rain = &rain
		}
	}
	for _, c := range countries {
		ws, _, _, err := a.wx.warnings.get(c, 10*time.Minute, func() ([]WxWarning, error) { return a.fetchWarnings(ctx, c) })
		if err != nil {
			resp.Errors["warnings"] = "MeteoAlarm is niet bereikbaar"
			slog.Warn("meteoalarm fetch failed", "country", c, "err", err)
			continue
		}
		for _, wa := range ws {
			wa.Here = regionMatches(wa.Area, resp.Location.Region)
			resp.Warnings = append(resp.Warnings, wa)
		}
	}
	levelRank := map[string]int{"red": 0, "orange": 1, "yellow": 2}
	sort.SliceStable(resp.Warnings, func(i, j int) bool {
		wi, wj := resp.Warnings[i], resp.Warnings[j]
		if wi.Here != wj.Here {
			return wi.Here
		}
		return levelRank[wi.Level] < levelRank[wj.Level]
	})
	if len(resp.Errors) == 0 {
		resp.Errors = nil
	}
	writeJSON(w, r, http.StatusOK, 60, resp)
}

type GeoResult struct {
	Name    string  `json:"name"`
	Region  string  `json:"region,omitempty"`
	Country string  `json:"country,omitempty"`
	CC      string  `json:"cc"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

func (a *App) handleGeocode(w http.ResponseWriter, r *http.Request) {
	q := strings.Join(strings.Fields(r.URL.Query().Get("q")), " ")
	if n := len([]rune(q)); n < 2 || n > 80 {
		writeError(w, r, http.StatusBadRequest, "zoekterm moet 2 tot 80 tekens zijn")
		return
	}
	ip := a.clientIP(r)
	key := strings.ToLower(q)
	res, _, _, err := a.wx.geocode.get(key, 24*time.Hour, func() ([]GeoResult, error) {
		if !a.wx.limiter.allow(ip) {
			return nil, errRateLimited
		}
		return a.fetchGeocode(context.WithoutCancel(r.Context()), q)
	})
	if errors.Is(err, errRateLimited) {
		writeError(w, r, http.StatusTooManyRequests, "te veel zoekopdrachten, probeer het zo opnieuw")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "plaatsen zoeken is nu niet mogelijk")
		return
	}
	writeJSON(w, r, http.StatusOK, 3600, map[string]any{"results": res})
}

func (a *App) fetchGeocode(ctx context.Context, q string) ([]GeoResult, error) {
	u := "https://geocoding-api.open-meteo.com/v1/search?count=20&language=nl&format=json&name=" + url.QueryEscape(q)
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
	if err != nil {
		return nil, err
	}
	var raw struct {
		Results []struct {
			Name       string   `json:"name"`
			Admin1     string   `json:"admin1"`
			Country    string   `json:"country"`
			CC         string   `json:"country_code"`
			Lat        float64  `json:"latitude"`
			Lon        float64  `json:"longitude"`
			Population *float64 `json:"population"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, err
	}
	rank := func(cc string) int { // NL/BE first
		switch cc {
		case "NL":
			return 0
		case "BE":
			return 1
		}
		return 2
	}
	sort.SliceStable(raw.Results, func(i, j int) bool {
		ri, rj := rank(raw.Results[i].CC), rank(raw.Results[j].CC)
		if ri != rj {
			return ri < rj
		}
		pi, pj := 0.0, 0.0
		if raw.Results[i].Population != nil {
			pi = *raw.Results[i].Population
		}
		if raw.Results[j].Population != nil {
			pj = *raw.Results[j].Population
		}
		return pi > pj
	})
	out := []GeoResult{}
	for _, x := range raw.Results {
		if len(out) == 8 {
			break
		}
		out = append(out, GeoResult{Name: x.Name, Region: x.Admin1, Country: x.Country, CC: x.CC,
			Lat: math.Round(x.Lat*100) / 100, Lon: math.Round(x.Lon*100) / 100})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Shared state for scheduled JSON/RSS sources (threats, advisories)

type feedState struct {
	Data      any
	FetchedAt time.Time
	Err       string
	ErrSince  time.Time
	ETag      string
	LastMod   string
}

type stateStore struct {
	mu sync.RWMutex
	m  map[string]*feedState
}

func newStateStore() *stateStore { return &stateStore{m: map[string]*feedState{}} }

func (s *stateStore) get(k string) feedState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st := s.m[k]; st != nil {
		return *st
	}
	return feedState{}
}

func (s *stateStore) ok(k string, data any, etag, lastMod string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = &feedState{Data: data, FetchedAt: time.Now(), ETag: etag, LastMod: lastMod}
}

func (s *stateStore) fail(k string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.m[k]
	if st == nil {
		st = &feedState{}
		s.m[k] = st
	}
	st.Err = err.Error()
	if st.ErrSince.IsZero() {
		st.ErrSince = time.Now()
	}
}

// SourceInfo is the per-source footer entry ("bronnen") sent with threat and advisory data.
type SourceInfo struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	License   string     `json:"license,omitempty"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
	Error     string     `json:"error,omitempty"`
	ErrSince  *time.Time `json:"error_since,omitempty"`
}

func (s *stateStore) info(key, id, name, url, license string) SourceInfo {
	st := s.get(key)
	si := SourceInfo{ID: id, Name: name, URL: url, License: license, Error: st.Err}
	if !st.FetchedAt.IsZero() {
		t := st.FetchedAt.UTC().Truncate(time.Second)
		si.FetchedAt = &t
	}
	if st.Err != "" && !st.ErrSince.IsZero() {
		t := st.ErrSince.UTC().Truncate(time.Second)
		si.ErrSince = &t
	}
	return si
}

// fetchJob builds a scheduler job that fetches url, parses the body and stores the result.
func (a *App) fetchJob(key string, url func() string, accept string, header func() map[string]string,
	parse func([]byte) (any, error), after func(ctx context.Context, data any)) func(context.Context) error {
	return func(ctx context.Context) error {
		prev := a.threats.get(key)
		req := FetchReq{URL: url(), Accept: accept, ETag: prev.ETag, LastModified: prev.LastMod}
		if header != nil {
			req.Header = header()
		}
		resp, err := a.fetcher.Do(ctx, req)
		if err == nil && resp.NotModified && prev.Data != nil {
			a.threats.ok(key, prev.Data, prev.ETag, prev.LastMod)
			return nil
		}
		if err == nil {
			var data any
			if data, err = parse(resp.Body); err == nil {
				a.threats.ok(key, data, resp.ETag, resp.LastMo)
				if after != nil {
					after(ctx, data)
				}
				return nil
			}
		}
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
}

// ---------------------------------------------------------------------------
// SANS Internet Storm Center (DShield)

type ISCPort struct {
	Port    int    `json:"port"`
	Service string `json:"service,omitempty"`
	Records int64  `json:"records"`
	Targets int64  `json:"targets"`
	Sources int64  `json:"sources"`
}

type PortsData struct {
	Date     string    `json:"date"`
	Fallback bool      `json:"fallback"` // today had no data yet, this is yesterday
	Items    []ISCPort `json:"items"`
}

type DayStat struct {
	Date    string `json:"date"`
	Sources int64  `json:"sources"`
	Records int64  `json:"records"`
	Targets int64  `json:"targets"`
}

type DailyData struct {
	Days       []DayStat `json:"days"`
	Min        int64     `json:"min"`
	Max        int64     `json:"max"`
	Avg        int64     `json:"avg"`
	Last       *DayStat  `json:"last,omitempty"`  // latest complete day
	LastVsAvg  float64   `json:"last_vs_avg_pct"` // percentage difference vs 30-day average
	Today      *DayStat  `json:"today,omitempty"` // partial current day (UTC)
	Incomplete bool      `json:"-"`
}

type TopIP struct {
	IP      string `json:"ip"`
	Reports int64  `json:"reports"`
	Targets int64  `json:"targets"`
	CC      string `json:"cc,omitempty"`
	AS      string `json:"as,omitempty"`
	Org     string `json:"org,omitempty"`
}

var portServices = map[int]string{
	21: "ftp", 22: "ssh", 23: "telnet", 25: "smtp", 53: "dns", 80: "http", 81: "http-alt", 110: "pop3", 111: "rpcbind",
	123: "ntp", 135: "msrpc", 137: "netbios", 139: "netbios", 143: "imap", 161: "snmp", 389: "ldap", 443: "https",
	445: "smb", 465: "smtps", 502: "modbus", 587: "smtp", 631: "ipp", 853: "dns-over-tls", 993: "imaps", 995: "pop3s",
	1080: "socks", 1433: "mssql", 1521: "oracle", 1723: "pptp", 1883: "mqtt", 2000: "cisco-sccp", 2222: "ssh-alt",
	2323: "telnet-alt", 3128: "http-proxy", 3306: "mysql", 3389: "rdp", 5060: "sip", 5432: "postgresql", 5555: "adb",
	5900: "vnc", 6379: "redis", 7547: "tr-069", 8000: "http-alt", 8080: "http-proxy", 8081: "http-alt", 8088: "http-alt",
	8443: "https-alt", 8888: "http-alt", 9200: "elasticsearch", 10250: "kubelet", 11211: "memcached", 27017: "mongodb",
	37215: "huawei-upnp", 49152: "upnp", 51413: "bittorrent", 52869: "upnp",
}

// parseTopPorts accepts ISC's object form {"0": {...}, "1": {...}, "limit": ...} and a plain array.
func parseTopPorts(body []byte) ([]ISCPort, error) {
	type rec struct {
		Rank       int   `json:"rank"`
		TargetPort int   `json:"targetport"`
		Records    int64 `json:"records"`
		Targets    int64 `json:"targets"`
		Sources    int64 `json:"sources"`
	}
	var recs []rec
	body = trimSpaceBytes(body)
	if len(body) > 0 && body[0] == '[' {
		if err := json.Unmarshal(body, &recs); err != nil {
			return nil, fmt.Errorf("isc topports: %w", err)
		}
	} else {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("isc topports: %w", err)
		}
		for _, raw := range m {
			var r rec
			if json.Unmarshal(raw, &r) == nil && r.TargetPort > 0 {
				recs = append(recs, r)
			}
		}
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Rank != recs[j].Rank {
			return recs[i].Rank < recs[j].Rank
		}
		return recs[i].Records > recs[j].Records
	})
	var out []ISCPort
	for _, r := range recs {
		if r.TargetPort <= 0 || r.Records <= 0 {
			continue
		}
		out = append(out, ISCPort{Port: r.TargetPort, Service: portServices[r.TargetPort], Records: r.Records, Targets: r.Targets, Sources: r.Sources})
	}
	return out, nil
}

func trimSpaceBytes(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }

// parseDaily computes 30-day statistics over complete days; today (UTC) is partial and kept apart.
func parseDaily(body []byte, today string) (DailyData, error) {
	var days []DayStat
	if err := json.Unmarshal(body, &days); err != nil {
		return DailyData{}, fmt.Errorf("isc dailysummary: %w", err)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	d := DailyData{Days: days}
	var sum, n int64
	for i := range days {
		x := days[i]
		if x.Date >= today {
			d.Today = &days[i]
			continue
		}
		if x.Sources <= 0 {
			continue
		}
		if n == 0 || x.Sources < d.Min {
			d.Min = x.Sources
		}
		d.Max = max(d.Max, x.Sources)
		sum += x.Sources
		n++
		d.Last = &days[i]
	}
	if n == 0 {
		return DailyData{}, errors.New("isc dailysummary: no complete days")
	}
	d.Avg = sum / n
	d.LastVsAvg = math.Round((float64(d.Last.Sources)/float64(d.Avg)-1)*1000) / 10
	return d, nil
}

// normIP strips ISC's zero padding ("013.094.254.200") and validates the address.
func normIP(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if parts := strings.Split(s, "."); len(parts) == 4 {
		for i, p := range parts {
			p = strings.TrimLeft(p, "0")
			if p == "" {
				p = "0"
			}
			parts[i] = p
		}
		s = strings.Join(parts, ".")
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return "", false
	}
	return ip.String(), true
}

func parseTopIPs(body []byte) ([]TopIP, error) {
	var recs []struct {
		Source  string `json:"source"`
		Reports int64  `json:"reports"`
		Targets int64  `json:"targets"`
	}
	if err := json.Unmarshal(body, &recs); err != nil {
		return nil, fmt.Errorf("isc topips: %w", err)
	}
	var out []TopIP
	for _, r := range recs {
		if ip, ok := normIP(r.Source); ok {
			out = append(out, TopIP{IP: ip, Reports: r.Reports, Targets: r.Targets})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("isc topips: empty")
	}
	return out, nil
}

func parseInfocon(body []byte) (any, error) {
	var v struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("isc infocon: %w", err)
	}
	switch v.Status {
	case "green", "yellow", "orange", "red":
		return v.Status, nil
	}
	return nil, fmt.Errorf("isc infocon: unknown status %q", v.Status)
}

// ---------------------------------------------------------------------------
// abuse.ch Feodo Tracker (botnet C2 servers; data CC0)

type FeodoC2 struct {
	IP         string `json:"ip"`
	Port       int    `json:"port"`
	Status     string `json:"status"`
	Malware    string `json:"malware"`
	CC         string `json:"cc,omitempty"`
	AS         string `json:"as,omitempty"`
	FirstSeen  string `json:"first_seen,omitempty"`
	LastOnline string `json:"last_online,omitempty"`
}

func parseFeodo(body []byte) (any, error) {
	var recs []struct {
		IP         string `json:"ip_address"`
		Port       int    `json:"port"`
		Status     string `json:"status"`
		ASNumber   int    `json:"as_number"`
		ASName     string `json:"as_name"`
		Country    string `json:"country"`
		FirstSeen  string `json:"first_seen"`
		LastOnline string `json:"last_online"`
		Malware    string `json:"malware"`
	}
	if err := json.Unmarshal(body, &recs); err != nil {
		return nil, fmt.Errorf("feodo: %w", err)
	}
	out := make([]FeodoC2, 0, len(recs))
	for _, r := range recs {
		ip, ok := normIP(r.IP)
		if !ok {
			continue
		}
		c := FeodoC2{IP: ip, Port: r.Port, Status: strings.ToLower(r.Status), Malware: plainText(r.Malware),
			CC: strings.ToUpper(r.Country), FirstSeen: r.FirstSeen, LastOnline: r.LastOnline}
		if r.ASNumber > 0 {
			c.AS = fmt.Sprintf("AS%d %s", r.ASNumber, plainText(r.ASName))
		}
		out = append(out, c)
	}
	// online first, then most recently seen
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Status == "online") != (out[j].Status == "online") {
			return out[i].Status == "online"
		}
		return out[i].LastOnline > out[j].LastOnline
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// CISA Known Exploited Vulnerabilities (optional)

type KEVItem struct {
	CVE        string `json:"cve"`
	Vendor     string `json:"vendor"`
	Product    string `json:"product"`
	Name       string `json:"name"`
	Added      string `json:"added"`
	Ransomware bool   `json:"ransomware,omitempty"`
}

func parseKEV(body []byte) (any, error) {
	var f struct {
		Vulns []struct {
			CVE        string `json:"cveID"`
			Vendor     string `json:"vendorProject"`
			Product    string `json:"product"`
			Name       string `json:"vulnerabilityName"`
			Added      string `json:"dateAdded"`
			Ransomware string `json:"knownRansomwareCampaignUse"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("cisa kev: %w", err)
	}
	if len(f.Vulns) == 0 {
		return nil, errors.New("cisa kev: empty")
	}
	sort.SliceStable(f.Vulns, func(i, j int) bool { return f.Vulns[i].Added > f.Vulns[j].Added })
	var out []KEVItem
	for _, v := range f.Vulns[:min(10, len(f.Vulns))] {
		out = append(out, KEVItem{CVE: v.CVE, Vendor: plainText(v.Vendor), Product: plainText(v.Product),
			Name: truncate(plainText(v.Name), 120), Added: v.Added, Ransomware: strings.EqualFold(v.Ransomware, "Known")})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// ip-api.com geolocation: batched, rate-limited, cached 24 h in an LRU (max 10 000)

type geoInfo struct {
	CC, AS, Org string
	at          time.Time
}

type geoCache struct {
	mu          sync.Mutex
	ll          *list.List
	m           map[string]*list.Element
	max         int
	lookupMu    sync.Mutex // one lookup run at a time
	nextAllowed time.Time
}

type geoEntry struct {
	ip   string
	info geoInfo
}

func newGeoCache(max int) *geoCache {
	return &geoCache{ll: list.New(), m: map[string]*list.Element{}, max: max}
}

func (g *geoCache) get(ip string) (geoInfo, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.m[ip]
	if !ok {
		return geoInfo{}, false
	}
	en := e.Value.(*geoEntry)
	if time.Since(en.info.at) > 24*time.Hour {
		g.ll.Remove(e)
		delete(g.m, ip)
		return geoInfo{}, false
	}
	g.ll.MoveToFront(e)
	return en.info, true
}

func (g *geoCache) put(ip string, info geoInfo) {
	g.mu.Lock()
	defer g.mu.Unlock()
	info.at = time.Now()
	if e, ok := g.m[ip]; ok {
		e.Value.(*geoEntry).info = info
		g.ll.MoveToFront(e)
		return
	}
	g.m[ip] = g.ll.PushFront(&geoEntry{ip: ip, info: info})
	for g.ll.Len() > g.max {
		last := g.ll.Back()
		g.ll.Remove(last)
		delete(g.m, last.Value.(*geoEntry).ip)
	}
}

// nonPublic lists special-purpose ranges beyond what netip's Is* helpers cover.
var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001:db8::/32", "2002::/16"} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// publicIP reports whether s is a globally routable unicast address. Used to skip
// geolocation of private IPs and, in the image proxy, as the SSRF guard
// (private, loopback, link-local incl. cloud metadata 169.254.169.254, CGNAT, NAT64…).
func publicIP(s string) bool {
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return false
	}
	ip = ip.Unmap().WithZone("")
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// ip-api's free tier is HTTP-only; a var so tests can point it at a local server.
var ipAPIBatchURL = "http://ip-api.com/batch?fields=status,message,query,countryCode,as,org"

// geolocate looks up uncached public IPs in batches of ≤100, honouring ip-api's
// X-Rl (requests left) / X-Ttl (seconds until reset) headers. Free tier: 15 batch requests/min.
func (a *App) geolocate(ctx context.Context, ips []string) {
	if !a.config().Features.Geolocation {
		return
	}
	g := a.geo
	g.lookupMu.Lock()
	defer g.lookupMu.Unlock()
	var todo []string
	seen := map[string]bool{}
	for _, ip := range ips {
		if seen[ip] {
			continue
		}
		seen[ip] = true
		if _, ok := g.get(ip); ok {
			continue
		}
		if !publicIP(ip) {
			g.put(ip, geoInfo{})
			continue
		}
		todo = append(todo, ip)
	}
	if len(todo) == 0 {
		slog.Debug("ip-api: all IPs cached", "ips", len(seen))
		return
	}
	for start := 0; start < len(todo); start += 100 {
		batch := todo[start:min(start+100, len(todo))]
		if wait := time.Until(g.nextAllowed); wait > 0 {
			slog.Info("ip-api: waiting for rate-limit window", "wait", wait.Round(time.Second))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		}
		body, _ := json.Marshal(batch)
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: ipAPIBatchURL, Method: http.MethodPost, Accept: "application/json",
			Header: map[string]string{"Content-Type": "application/json"}, Body: body})
		rl, ttl := -1, 60
		if resp != nil {
			if v, e := strconv.Atoi(resp.Header.Get("X-Rl")); e == nil {
				rl = v
			}
			if v, e := strconv.Atoi(resp.Header.Get("X-Ttl")); e == nil {
				ttl = v
			}
		}
		// Stay within 15 requests/min even when headers are missing; wait for reset when exhausted.
		g.nextAllowed = time.Now().Add(4 * time.Second)
		if rl == 0 || (resp != nil && resp.Status == http.StatusTooManyRequests) {
			g.nextAllowed = time.Now().Add(time.Duration(ttl+1) * time.Second)
		}
		if err != nil {
			slog.Warn("ip-api batch failed", "ips", len(batch), "err", err, "requests_left", rl, "reset_in_s", ttl)
			return
		}
		var res []struct {
			Status, Message, Query, CountryCode, AS, Org string
		}
		if err := json.Unmarshal(resp.Body, &res); err != nil {
			slog.Warn("ip-api: bad response", "err", err)
			return
		}
		ok := 0
		for _, r := range res {
			info := geoInfo{}
			if r.Status == "success" {
				info = geoInfo{CC: strings.ToUpper(r.CountryCode), AS: plainText(r.AS), Org: plainText(r.Org)}
				ok++
			}
			g.put(r.Query, info)
		}
		slog.Info("ip-api batch", "ips", len(batch), "resolved", ok, "cached_total", g.ll.Len(), "requests_left", rl, "reset_in_s", ttl)
	}
}

// ---------------------------------------------------------------------------
// Security advisories

type Advisory struct {
	ID          string     `json:"id"`
	Source      string     `json:"source"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Published   time.Time  `json:"published"`
	Updated     *time.Time `json:"updated,omitempty"` // set when a newer version was published
	Version     string     `json:"version,omitempty"`
	Severity    string     `json:"severity"`              // low | medium | high | critical | unknown
	Probability string     `json:"probability,omitempty"` // NCSC "kans": L | M | H
	Impact      string     `json:"impact,omitempty"`      // NCSC "schade": L | M | H
	CVEs        []string   `json:"cves"`
	Products    []string   `json:"products"`
	Exploited   bool       `json:"exploited,omitempty"`
}

var (
	ncscTitleRe = regexp.MustCompile(`^\s*(NCSC-\d{4}-\d{3,5})\s*\[(\d+\.\d+)\]\s*\[([LMH])/([LMH])\]\s*(.+?)\s*$`)
	cveRe       = regexp.MustCompile(`(?i)\bCVE-\d{4}-\d{4,7}\b`)
	exploitedRe = regexp.MustCompile(`(?i)misbruik[^.]{0,60}waargenomen|actief (wordt )?misbruikt|actively exploited|exploited in the wild`)
	productRe   = regexp.MustCompile(`(?i)^kwetsba(?:ar)?he(?:id|den)\s+(?:verholpen|ontdekt|gevonden)\s+in\s+`)
	productSep  = regexp.MustCompile(`\s+en\s+|,\s*`)
	sevHighRe   = regexp.MustCompile(`\bhigh\b|\bhoog\b|\bhoch\b`)
	sevMedRe    = regexp.MustCompile(`\bmedium\b|\bmoderate\b|\bgemiddeld\b|\bmittel\b`)
	sevLowRe    = regexp.MustCompile(`\blow\b|\blaag\b|\bniedrig\b`)
)

// ncscTitle parses "NCSC-2026-0123 [1.00] [M/H] Kwetsbaarheden verholpen in …".
func ncscTitle(t string) (id, version, prob, impact, rest string, ok bool) {
	m := ncscTitleRe.FindStringSubmatch(strings.Join(strings.Fields(t), " "))
	if m == nil {
		return "", "", "", "", "", false
	}
	return m[1], m[2], m[3], m[4], m[5], true
}

// ncscSeverity derives severity from kans (probability) and schade (impact).
func ncscSeverity(prob, impact string) string {
	rank := map[string]int{"L": 1, "M": 2, "H": 3}
	p, i := rank[prob], rank[impact]
	switch {
	case p == 0 || i == 0:
		return "unknown"
	case p == 3 && i == 3:
		return "critical"
	case p+i == 5: // M/H or H/M
		return "high"
	case p+i == 4: // M/M, L/H, H/L
		return "medium"
	default:
		return "low"
	}
}

func ncscProducts(rest string) []string {
	loc := productRe.FindStringIndex(rest)
	if loc == nil {
		return []string{}
	}
	var out []string
	for _, p := range productSep.Split(rest[loc[1]:], -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func extractCVEs(s string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range cveRe.FindAllString(s, -1) {
		c = strings.ToUpper(c)
		if !seen[c] && len(out) < 50 {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

var (
	sevRatingRe = regexp.MustCompile(`(?i)(?:security impact rating|severity)\s*:\s*(critical|high|medium|moderate|low|informational)`)
	sevCVSSRe   = regexp.MustCompile(`(?i)cvss(?:v?3(?:\.\d)?)?\s*(?:base\s*)?score\s*:\s*(\d{1,2}(?:\.\d)?)`)
)

// explicitSeverity reads a severity that vendor feeds state outright: Cisco "Security Impact
// Rating: Critical", Palo Alto "(Severity: HIGH)", Fortinet "CVSSv3 Score: 9.8". Empty = none.
func explicitSeverity(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	if m := sevRatingRe.FindStringSubmatch(s); m != nil {
		switch strings.ToLower(m[1]) {
		case "critical":
			return "critical"
		case "high":
			return "high"
		case "medium", "moderate":
			return "medium"
		default:
			return "low"
		}
	}
	if m := sevCVSSRe.FindStringSubmatch(s); m != nil {
		f, _ := strconv.ParseFloat(m[1], 64)
		switch {
		case f >= 9:
			return "critical"
		case f >= 7:
			return "high"
		case f >= 4:
			return "medium"
		case f > 0:
			return "low"
		}
	}
	return ""
}

var fortiProductRe = regexp.MustCompile(`\b(Forti[A-Z][A-Za-z]+(?: (?:Windows|macOS|Linux|EMS|Cloud|Manager))?)`)

// fortiProduct: the Fortinet product an advisory is about ("FortiClient Windows"), from its description.
func fortiProduct(src AdvisorySource, title, desc string) string {
	if !strings.Contains(src.URL, "fortinet.com") || strings.Contains(title, "Forti") {
		return ""
	}
	if m := fortiProductRe.FindStringSubmatch(desc); m != nil {
		return m[1]
	}
	return ""
}

func advAccept(s AdvisorySource) string {
	if s.Format == "ghsa" {
		return "application/vnd.github+json"
	}
	return feedAccept
}

// parseGHSA reads GET /repos/{owner}/{repo}/security-advisories: published advisories with
// their GHSA id, CVE, severity and summary.
func parseGHSA(src AdvisorySource, body []byte) ([]Advisory, error) {
	var list []struct {
		GHSA      string     `json:"ghsa_id"`
		CVE       *string    `json:"cve_id"`
		URL       string     `json:"html_url"`
		Summary   string     `json:"summary"`
		Severity  string     `json:"severity"`
		State     string     `json:"state"`
		Published *time.Time `json:"published_at"`
		Updated   *time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("github advisories: %w", err)
	}
	out := []Advisory{}
	for _, x := range list {
		if x.State != "" && x.State != "published" || x.Published == nil || !strings.HasPrefix(x.URL, "https://github.com/") {
			continue
		}
		a := Advisory{ID: x.GHSA, Source: src.ID, Title: truncate(plainText(x.Summary), 200), URL: x.URL, Published: x.Published.UTC(), CVEs: []string{}, Products: []string{}}
		switch strings.ToLower(x.Severity) {
		case "critical", "high", "medium", "low":
			a.Severity = strings.ToLower(x.Severity)
		default:
			a.Severity = "unknown"
		}
		if x.CVE != nil && *x.CVE != "" {
			a.CVEs = []string{*x.CVE}
		}
		if x.Updated != nil && x.Updated.Sub(*x.Published) > 24*time.Hour {
			u := x.Updated.UTC()
			a.Updated = &u
		}
		out = append(out, a)
	}
	return out, nil
}

func keywordSeverity(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "critical") || strings.Contains(s, "kritiek") || strings.Contains(s, "kritisch"):
		return "critical"
	case sevHighRe.MatchString(s):
		return "high"
	case sevMedRe.MatchString(s):
		return "medium"
	case sevLowRe.MatchString(s):
		return "low"
	}
	return "unknown"
}

// normalizeAdvisories converts feed items to advisories. NCSC titles carry id,
// version and kans/schade; other feeds get keyword-based severity.
func normalizeAdvisories(src AdvisorySource, raws []rawItem, now time.Time) []Advisory {
	base, _ := url.Parse(src.URL)
	byID := map[string]Advisory{}
	var order []string
	for _, r := range raws {
		link := safeURL(r.Link, base)
		title := plainText(r.Title)
		if link == "" || title == "" {
			continue
		}
		desc := plainText(r.Summary)
		a := Advisory{Source: src.ID, URL: link, CVEs: extractCVEs(title + " " + desc), Products: []string{},
			Exploited: exploitedRe.MatchString(desc)}
		pub, ok := parseDate(r.Date)
		if !ok || pub.After(now.Add(5*time.Minute)) {
			pub = now
		}
		a.Published = pub.UTC().Truncate(time.Second)
		if id, ver, p, i, rest, ok := ncscTitle(title); ok && src.Format == "ncsc" {
			a.ID, a.Version, a.Probability, a.Impact, a.Title = id, ver, p, i, truncate(rest, 200)
			a.Severity = ncscSeverity(p, i)
			a.Products = ncscProducts(rest)
			if ver != "1.00" {
				u := a.Published
				a.Updated = &u
			}
		} else {
			a.ID = firstNonEmpty(r.GUID, link)
			if p := fortiProduct(src, title, desc); p != "" { // Fortinet titles name the weakness, not the product
				title = p + ": " + title
			}
			a.Title = truncate(title, 200)
			if a.Severity = explicitSeverity(title + " " + desc); a.Severity == "" {
				a.Severity = keywordSeverity(title + " " + desc)
			}
		}
		// keep the highest version per advisory id
		if old, ok := byID[a.ID]; ok {
			if old.Version >= a.Version {
				continue
			}
		} else {
			order = append(order, a.ID)
		}
		byID[a.ID] = a
	}
	out := make([]Advisory, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

// ---------------------------------------------------------------------------
// Jobs and handlers

const (
	iscBase     = "https://isc.sans.edu/api/"
	feodoURL    = "https://feodotracker.abuse.ch/downloads/ipblocklist.json"
	kevURL      = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"
	iscLicense  = "SANS ISC/DShield-gegevens, niet-commercieel gebruik (CC BY-NC-SA)"
	feodoLicens = "abuse.ch Feodo Tracker, CC0"
)

// threatJobs returns the scheduler jobs for the threat panels and advisories.
func (a *App) threatJobs(cfg *Config) []Job {
	var jobs []Job
	if cfg.Threats.Enabled {
		iv, daily := cfg.Threats.Interval.D(), cfg.Threats.DailyInterval.D()
		utcDay := func(off int) string { return time.Now().UTC().AddDate(0, 0, off).Format("2006-01-02") }
		jobs = append(jobs,
			Job{Key: "isc:infocon", Interval: iv, Run: a.fetchJob("isc:infocon", func() string { return iscBase + "infocon?json" },
				"application/json", nil, parseInfocon, nil)},
			Job{Key: "isc:topports", Interval: iv, Run: a.runTopPorts},
			Job{Key: "isc:daily", Interval: daily, Run: a.fetchJob("isc:daily",
				func() string { return iscBase + "dailysummary/" + utcDay(-29) + "/" + utcDay(0) + "?json" }, "application/json", nil,
				func(b []byte) (any, error) { return parseDaily(b, utcDay(0)) }, nil)},
			Job{Key: "isc:topips", Interval: iv, Run: a.fetchJob("isc:topips", func() string { return iscBase + "topips/records/20?json" },
				"application/json", nil, func(b []byte) (any, error) { return parseTopIPs(b) },
				func(ctx context.Context, data any) {
					var ips []string
					for _, t := range data.([]TopIP) {
						ips = append(ips, t.IP)
					}
					a.geolocate(ctx, ips)
				})},
			Job{Key: "abusech:feodo", Interval: iv, Run: a.fetchJob("abusech:feodo", func() string { return feodoURL }, "application/json",
				func() map[string]string {
					if k := a.config().Keys.AbusechAuthKey; k != "" {
						return map[string]string{"Auth-Key": k}
					}
					return nil
				}, parseFeodo, nil)},
		)
		if cfg.Threats.CISAKEV {
			jobs = append(jobs, Job{Key: "cisa:kev", Interval: max(iv, time.Hour), Run: a.fetchJob("cisa:kev",
				func() string { return kevURL }, "application/json", nil, parseKEV, nil)})
		}
	}
	if cfg.Alerts.NCTV.Enabled {
		jobs = append(jobs, Job{Key: "nctv", Sig: cfg.Alerts.NCTV.URL, Interval: cfg.Alerts.NCTV.Interval.D(),
			Run: a.fetchJob("nctv", func() string { return a.config().Alerts.NCTV.URL }, "text/html", nil, parseNCTV, nil)})
	}
	if cfg.Traffic.Enabled {
		jobs = append(jobs, Job{Key: "ndw:traffic", Sig: cfg.Traffic.URL, Interval: cfg.Traffic.Interval.D(), Run: a.runTraffic})
	}
	if cfg.Energy.Enabled {
		jobs = append(jobs, Job{Key: "energyzero", Sig: fmt.Sprint(cfg.Energy.URL, cfg.Energy.VAT, cfg.Energy.ElectricityExtra, cfg.Energy.GasExtra),
			Interval: cfg.Energy.Interval.D(), Run: a.runEnergy})
	}
	if cfg.Air.Enabled {
		jobs = append(jobs, Job{Key: "lml:stations", Sig: cfg.Air.StationsURL, Interval: 24 * time.Hour,
			Run: a.fetchJob("lml:stations", func() string { return a.config().Air.StationsURL }, "text/csv, */*;q=0.5", nil, parseLMLStations, nil)},
			Job{Key: "lml:lki", Sig: cfg.Air.Base, Interval: cfg.Air.Interval.D(), Run: a.runAirLKI})
	}
	if cfg.Trains.Enabled && cfg.Keys.NSAPIKey != "" {
		jobs = append(jobs, Job{Key: "ns:disruptions", Sig: cfg.Trains.URL, Interval: cfg.Trains.Interval.D(),
			Run: a.fetchJob("ns:disruptions", func() string { return a.config().Trains.URL }, "application/json",
				func() map[string]string {
					return map[string]string{"Ocp-Apim-Subscription-Key": a.config().Keys.NSAPIKey}
				},
				func(b []byte) (any, error) { return parseNSDisruptions(b, time.Now()) }, nil)})
	}
	if cfg.Ransomware.Enabled {
		for _, cc := range cfg.Ransomware.Countries {
			cc := cc
			u := strings.TrimSuffix(cfg.Ransomware.Base, "/") + "/countryvictims/" + cc
			jobs = append(jobs, Job{Key: "rw:" + cc, Sig: u, Interval: cfg.Ransomware.Interval.D(),
				Run: a.fetchJob("rw:"+cc, func() string { return u }, "application/json", nil,
					func(b []byte) (any, error) { return parseRansomware(b, cc, time.Now()) }, nil)})
		}
	}
	if cfg.Economy.Enabled {
		jobs = append(jobs, Job{Key: "econ:figures", Sig: cfg.Economy.EurostatBase + "|" + cfg.Economy.ECBBase, Interval: cfg.Economy.Interval.D(), Run: a.runEconomy})
	}
	if cfg.Markets.Enabled {
		jobs = append(jobs, Job{Key: "yahoo:markets", Sig: fmt.Sprint(cfg.Markets.URL, cfg.Markets.Indices, cfg.Markets.Stocks), Interval: cfg.Markets.Interval.D(), Run: a.runMarkets})
	}
	if cfg.Utilities.Enabled {
		jobs = append(jobs, Job{Key: "grid:outages", Sig: cfg.Utilities.LianderURL + "|" + cfg.Utilities.StedinURL, Interval: cfg.Utilities.Interval.D(), Run: a.runUtilities})
	}
	if cfg.NLAlert.Enabled {
		jobs = append(jobs, Job{Key: "nlalert", Sig: cfg.NLAlert.URL, Interval: cfg.NLAlert.Interval.D(),
			Run: a.fetchJob("nlalert", func() string { return a.config().NLAlert.URL }, "application/json", nil,
				func(b []byte) (any, error) { return parseNLAlerts(b) }, nil)})
	}
	if cfg.Fuel.Enabled {
		jobs = append(jobs, Job{Key: "fuel:gla", Sig: cfg.Fuel.URL, Interval: cfg.Fuel.Interval.D(),
			Run: a.fetchJob("fuel:gla", func() string { return a.config().Fuel.URL }, "text/html", nil,
				func(b []byte) (any, error) { return parseFuelPrices(b) }, nil)})
	}
	if w := cfg.Waste; w.Enabled && hasWasteDefault(cfg) {
		jobs = append(jobs, Job{Key: "waste:calendar", Sig: fmt.Sprint(w.Provider, w.Providers, w.Postcode, w.Number, w.Suffix, w.ICSURL, w.HomeAssistant.URL, w.HomeAssistant.Entities),
			Interval: w.Interval.D(), Run: a.runWaste})
	}
	if cfg.Amber.Enabled {
		jobs = append(jobs, Job{Key: "burgernet:amber", Sig: cfg.Amber.URL, Interval: cfg.Amber.Interval.D(),
			Run: a.fetchJob("burgernet:amber", func() string { return a.config().Amber.URL }, "application/json", nil,
				func(b []byte) (any, error) { return parseAmber(b) }, nil)})
	}
	if cfg.Features.SourceIcons { // the job itself skips icons that are fresh: weekly, or after 6 h on failure
		jobs = append(jobs, Job{Key: "icons", Sig: "icons", Interval: time.Hour, Run: a.iconJob})
	}
	if cfg.Radiation.Enabled {
		jobs = append(jobs, Job{Key: radKey, Sig: cfg.Radiation.URL, Interval: cfg.Radiation.Interval.D(),
			Run: a.fetchJob(radKey, func() string { return radURL(a.config().Radiation.URL) }, "application/json", nil,
				func(b []byte) (any, error) { return parseRadiation(b) }, nil)})
	}
	if cfg.Satellite.Enabled {
		jobs = append(jobs, Job{Key: satKey, Sig: cfg.Satellite.URL + " " + cfg.Satellite.Layer, Interval: cfg.Satellite.Interval.D(), Run: a.satelliteJob})
	}
	if cfg.Sky.Enabled && cfg.Sky.Launches {
		jobs = append(jobs, Job{Key: "ll2:launches", Sig: cfg.Sky.LaunchesURL, Interval: time.Hour,
			Run: a.fetchJob("ll2:launches", func() string { return a.config().Sky.LaunchesURL }, "application/json", nil,
				func(b []byte) (any, error) { return parseLaunches(b, time.Now()) }, nil)})
	}
	if w := cfg.World; w.Enabled {
		// the jobs keep a week; the handler shows world.hours of it
		jobs = append(jobs, Job{Key: "usgs:world", Sig: fmt.Sprint(w.USGSURL, w.QuakeMinMag), Interval: w.Interval.D(),
			Run: a.fetchJob("usgs:world", func() string { return a.config().World.USGSURL }, "application/json", nil,
				func(b []byte) (any, error) { return parseUSGS(b, w.QuakeMinMag, 7*24, time.Now()) }, nil)})
		if w.FireRisk {
			jobs = append(jobs, Job{Key: "brandweer:firerisk", Sig: w.FireRiskURL, Interval: time.Hour,
				Run: a.fetchJob("brandweer:firerisk", func() string { return a.config().World.FireRiskURL }, "text/html", nil,
					func(b []byte) (any, error) { return parseFireRisk(b) }, nil)})
		}
		if w.Water {
			jobs = append(jobs, Job{Key: "rws:water", Sig: w.WaterURL, Interval: 10 * time.Minute, Run: a.runWater})
		}
		jobs = append(jobs, Job{Key: "eonet:events", Sig: fmt.Sprint(w.EONETURL, w.FireMinHa), Interval: w.Interval.D(),
			Run: a.fetchJob("eonet:events", func() string { return a.config().World.EONETURL }, "application/json", nil,
				func(b []byte) (any, error) { return parseEONET(b, w.FireMinHa, 7*24, time.Now()) }, nil)})
	}
	if cfg.Threats.Enabled && cfg.Threats.URLhausNL {
		jobs = append(jobs, Job{Key: "urlhaus:nl", Sig: cfg.Threats.URLhausNLURL, Interval: 3 * time.Hour, Run: a.runURLhausNL})
	}
	if cfg.Threats.Enabled && cfg.Threats.ThreatFox && cfg.Keys.AbusechAuthKey != "" {
		jobs = append(jobs, Job{Key: "threatfox:iocs", Sig: cfg.Threats.ThreatFoxURL + cfg.Keys.AbusechAuthKey[:min(4, len(cfg.Keys.AbusechAuthKey))], Interval: time.Hour, Run: a.runThreatFox})
	}
	if cfg.Exploits.Enabled {
		jobs = append(jobs, Job{Key: "exploitdb", Sig: cfg.Exploits.ExploitDBURL, Interval: time.Hour,
			Run: a.fetchJob("exploitdb", func() string { return a.config().Exploits.ExploitDBURL }, "application/rss+xml, application/xml", nil,
				func(b []byte) (any, error) { return parseExploitDB(b, time.Now()) }, nil)})
		jobs = append(jobs, Job{Key: "epss:risers", Sig: cfg.Exploits.EPSSURL, Interval: 6 * time.Hour, Run: a.runEPSS})
	}
	if cfg.NLThreat.Enabled && cfg.Keys.CloudflareRadarToken != "" {
		jobs = append(jobs, Job{Key: "radar:nl", Sig: cfg.NLThreat.RadarURL + cfg.NLThreat.Country + cfg.Keys.CloudflareRadarToken[:min(4, len(cfg.Keys.CloudflareRadarToken))], Interval: cfg.NLThreat.Interval.D(), Run: a.runRadarNL})
	}
	if cfg.Sky.Enabled {
		jobs = append(jobs, Job{Key: "noaa:kp", Sig: cfg.Sky.KpURL, Interval: 3 * time.Hour,
			Run: a.fetchJob("noaa:kp", func() string { return a.config().Sky.KpURL }, "application/json", nil,
				func(b []byte) (any, error) { return parseKpForecast(b) }, nil)})
		if cfg.Sky.SpaceWeather {
			jobs = append(jobs, Job{Key: "noaa:space", Sig: cfg.Sky.SpaceWeatherURL, Interval: 30 * time.Minute, Run: a.runSpaceWeather})
		}
	}
	if cfg.Sports.Enabled && slices.Contains(cfg.Sports.Sports, "f1") {
		jobs = append(jobs, Job{Key: "f1:jolpica", Sig: cfg.Sports.F1URL, Interval: cfg.Sports.Interval.D(), Run: a.runF1})
	}
	if cfg.Push.Enabled {
		jobs = append(jobs, Job{Key: "push:watch", Interval: time.Minute, Run: a.runPushWatch})
	}
	if cfg.Quakes.Enabled {
		jobs = append(jobs, Job{Key: "knmi:quakes", Sig: fmt.Sprint(cfg.Quakes.URL, cfg.Quakes.Days), Interval: cfg.Quakes.Interval.D(), Run: a.runQuakes})
	}
	if cfg.Outages.Enabled && cfg.Outages.Internet.Enabled {
		in := cfg.Outages.Internet
		jobs = append(jobs, Job{Key: "ioda:internet", Sig: fmt.Sprint(in.Base, in.Country, in.Networks), Interval: in.Interval.D(), Run: a.runInternet})
	}
	if cfg.Today.Enabled {
		jobs = append(jobs, Job{Key: "rijk:schoolholidays", Sig: cfg.Today.SchoolURL, Interval: 24 * time.Hour,
			Run: a.fetchJob("rijk:schoolholidays", func() string { return a.config().Today.SchoolURL }, "application/json", nil, parseSchoolHolidays, nil)})
		if cfg.Today.OnThisDay {
			jobs = append(jobs, Job{Key: "wiki:onthisday", Sig: cfg.Today.WikiURL, Interval: time.Hour, Run: a.runOnThisDay})
		}
	}
	if cfg.Politics.Enabled {
		jobs = append(jobs, Job{Key: "tk:politics", Sig: cfg.Politics.Base, Interval: cfg.Politics.Interval.D(), Run: a.runPolitics})
	}
	if cfg.Breaches.Enabled {
		sensitive := cfg.Breaches.IncludeSensitive
		jobs = append(jobs, Job{Key: "hibp:breaches", Sig: fmt.Sprint(cfg.Breaches.URL, sensitive), Interval: cfg.Breaches.Interval.D(),
			Run: a.fetchJob("hibp:breaches", func() string { return a.config().Breaches.URL }, "application/json", nil,
				func(b []byte) (any, error) { return parseBreaches(b, sensitive, time.Now()) }, nil)})
		if cfg.Breaches.Phishing {
			jobs = append(jobs, Job{Key: "fhd:alerts", Sig: cfg.Breaches.PhishingURL, Interval: 2 * time.Hour,
				Run: a.fetchJob("fhd:alerts", func() string { return a.config().Breaches.PhishingURL }, "application/rss+xml, application/xml", nil,
					func(b []byte) (any, error) { return parseScamAlerts(b, time.Now()) }, nil)})
		}
	}
	if cfg.Outages.Enabled {
		for _, p := range cfg.Outages.Providers {
			if !p.IsEnabled() {
				continue
			}
			p := p
			parse := map[string]func([]byte) (any, error){
				"statuspage": parseStatuspage, "m365": parseM365,
				"rss": func(b []byte) (any, error) { return parseStatusRSS(b, time.Now()) },
				"gcp": func(b []byte) (any, error) { return parseGCP(b, time.Now()) },
			}[p.Format]
			jobs = append(jobs, Job{Key: "outage:" + p.ID, Sig: p.URL + "|" + p.Format, Interval: cfg.Outages.Interval.D(),
				Run: a.fetchJob("outage:"+p.ID, func() string { return p.URL }, "application/json, application/rss+xml;q=0.9, */*;q=0.5", nil, parse, nil)})
		}
	}
	for _, s := range cfg.Advisories {
		if !s.IsEnabled() {
			continue
		}
		s := s
		iv := cfg.Fetch.DefaultInterval.D()
		if s.Interval != 0 {
			iv = s.Interval.D()
		}
		jobs = append(jobs, Job{Key: "adv:" + s.ID, Sig: s.URL + "|" + s.Format, Interval: iv,
			Run: a.fetchJob("adv:"+s.ID, func() string { return s.URL }, advAccept(s), nil, func(b []byte) (any, error) {
				if s.Format == "ghsa" {
					return parseGHSA(s, b)
				}
				raws, err := parseFeed(b, "")
				if err != nil {
					return nil, err
				}
				return normalizeAdvisories(s, raws, time.Now()), nil
			}, nil)})
	}
	return jobs
}

// runTopPorts fetches today's top ports (UTC); early in the day ISC has none yet, then yesterday.
func (a *App) runTopPorts(ctx context.Context) error {
	now := time.Now().UTC()
	var lastErr error
	for i, day := range []time.Time{now, now.AddDate(0, 0, -1)} {
		date := day.Format("2006-01-02")
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: iscBase + "topports/records/10/" + date + "?json", Accept: "application/json"})
		if err != nil {
			lastErr = err
			break
		}
		ports, err := parseTopPorts(resp.Body)
		if err != nil {
			lastErr = err
			break
		}
		if len(ports) > 0 {
			a.threats.ok("isc:topports", PortsData{Date: date, Fallback: i == 1, Items: ports}, "", "")
			return nil
		}
		lastErr = errors.New("isc topports: no data for today or yesterday")
	}
	a.threats.fail("isc:topports", lastErr)
	slog.Warn("fetch failed", "source", "isc:topports", "err", lastErr)
	return lastErr
}

type CountryCount struct {
	CC    string `json:"cc"`
	Count int    `json:"count"`
}

func countByCountry(ccs []string) []CountryCount {
	m := map[string]int{}
	for _, c := range ccs {
		if c == "" {
			c = "??"
		}
		m[c]++
	}
	out := make([]CountryCount, 0, len(m))
	for c, n := range m {
		out = append(out, CountryCount{CC: c, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].CC < out[j].CC
	})
	return out
}

func (a *App) handleThreats(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Threats.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	resp := map[string]any{"enabled": true, "geolocation": cfg.Features.Geolocation}
	if v, ok := a.threats.get("isc:infocon").Data.(string); ok {
		resp["infocon"] = v
	}
	if v, ok := a.threats.get("isc:topports").Data.(PortsData); ok {
		resp["ports"] = v
	}
	if v, ok := a.threats.get("isc:daily").Data.(DailyData); ok {
		resp["daily"] = v
	}
	if v, ok := a.threats.get("isc:topips").Data.([]TopIP); ok {
		ips := make([]TopIP, len(v))
		ccs := make([]string, 0, len(v))
		for i, t := range v {
			if g, ok := a.geo.get(t.IP); ok {
				t.CC, t.AS, t.Org = g.CC, g.AS, g.Org
			}
			ips[i] = t
			if cfg.Features.Geolocation {
				ccs = append(ccs, t.CC)
			}
		}
		top := map[string]any{"items": ips}
		if cfg.Features.Geolocation {
			top["countries"] = countByCountry(ccs)
		}
		resp["top_ips"] = top
	}
	if v, ok := a.threats.get("abusech:feodo").Data.([]FeodoC2); ok {
		online := 0
		ccs := make([]string, 0, len(v))
		for _, c := range v {
			if c.Status == "online" {
				online++
			}
			ccs = append(ccs, c.CC)
		}
		resp["feodo"] = map[string]any{"items": v[:min(25, len(v))], "total": len(v), "online": online, "countries": countByCountry(ccs)}
	}
	if cfg.Threats.CISAKEV {
		if v, ok := a.threats.get("cisa:kev").Data.([]KEVItem); ok {
			resp["kev"] = v
		}
	}
	sources := []SourceInfo{
		a.threats.info("isc:topports", "isc", "SANS Internet Storm Center (DShield)", "https://isc.sans.edu/", iscLicense),
		a.threats.info("abusech:feodo", "feodo", "abuse.ch Feodo Tracker", "https://feodotracker.abuse.ch/", feodoLicens),
	}
	if st := a.threats.get("isc:daily"); st.Err != "" {
		sources[0].Error = st.Err
	}
	if cfg.Features.Geolocation {
		sources = append(sources, SourceInfo{ID: "ip-api", Name: "ip-api.com", URL: "https://ip-api.com/", License: "geolocatie, gratis voor niet-commercieel gebruik"})
	}
	if cfg.Threats.CISAKEV {
		sources = append(sources, a.threats.info("cisa:kev", "kev", "CISA Known Exploited Vulnerabilities",
			"https://www.cisa.gov/known-exploited-vulnerabilities-catalog", "publiek domein"))
	}
	if cfg.Threats.URLhausNL {
		e := a.feedEntry("urlhaus:nl")
		if v, ok := a.threats.get("urlhaus:nl").Data.(URLhausNL); ok {
			e["data"] = v
		}
		resp["urlhaus"] = e
		sources = append(sources, a.threats.info("urlhaus:nl", "urlhaus", "abuse.ch URLhaus", "https://urlhaus.abuse.ch/", "CC0"))
	}
	if cfg.Threats.ThreatFox {
		e := a.feedEntry("threatfox:iocs")
		if cfg.Keys.AbusechAuthKey == "" {
			e["missing_key"] = true
		} else if v, ok := a.threats.get("threatfox:iocs").Data.(ThreatFoxData); ok {
			e["data"] = v
		}
		resp["threatfox"] = e
		if cfg.Keys.AbusechAuthKey != "" {
			sources = append(sources, a.threats.info("threatfox:iocs", "threatfox", "abuse.ch ThreatFox", "https://threatfox.abuse.ch/", "CC0"))
		}
	}
	resp["sources"] = sources
	writeJSON(w, r, http.StatusOK, 60, resp)
}

func (a *App) handleAdvisories(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	want := map[string]bool{}
	for _, id := range strings.Split(r.URL.Query().Get("sources"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = min(max(v, 1), 100)
	}
	var all, edge []Advisory
	sources := []SourceInfo{}
	groups := map[string]string{}
	for _, s := range cfg.Advisories {
		if !s.IsEnabled() || (len(want) > 0 && !want[s.ID]) {
			continue
		}
		key := "adv:" + s.ID
		sources = append(sources, a.threats.info(key, s.ID, s.Name, firstNonEmpty(s.Homepage, s.URL), ""))
		if s.Group != "" {
			groups[s.ID] = s.Group
		}
		if v, ok := a.threats.get(key).Data.([]Advisory); ok {
			if s.Group == "edge" {
				edge = append(edge, v...)
			} else {
				all = append(all, v...)
			}
		}
	}
	ts := func(a Advisory) time.Time {
		if a.Updated != nil {
			return *a.Updated
		}
		return a.Published
	}
	trim := func(l []Advisory) []Advisory { // each tab gets its own limit, so busy vendor feeds never push out NCSC
		sort.SliceStable(l, func(i, j int) bool { return ts(l[i]).After(ts(l[j])) })
		if len(l) > limit {
			l = l[:limit]
		}
		return l
	}
	// vendor feeds differ a lot in volume (Cisco publishes daily): at most 8 per vendor in the edge tab
	perVendor := map[string]int{}
	sort.SliceStable(edge, func(i, j int) bool { return ts(edge[i]).After(ts(edge[j])) })
	capped := edge[:0]
	for _, x := range edge {
		if perVendor[x.Source]++; perVendor[x.Source] <= 8 {
			capped = append(capped, x)
		}
	}
	all = append(trim(all), trim(capped)...)
	if all == nil {
		all = []Advisory{}
	}
	writeJSON(w, r, http.StatusOK, 60, map[string]any{"items": all, "sources": sources, "groups": groups})
}

// ---------------------------------------------------------------------------
// Top bar: NCTV terrorism threat level and the KNMI weather code.

type NCTVLevel struct {
	Level int    `json:"level"`
	Name  string `json:"name"`
	Since string `json:"since,omitempty"` // the DTN that set it, e.g. "juni 2026"
}

var (
	nctvLevelRe = regexp.MustCompile(`(?i)dreigingsniveau\b[^.]{0,200}?\bniveau\s+([1-5])\s+(?:op\s+een\s+schaal\s+)?van\s+(?:de\s+)?(?:5|vijf)\b`)
	nctvDTNRe   = regexp.MustCompile(`(?i)\bDTN\s+van\s+([a-z]+\s+\d{4})`)
	nctvNames   = []string{"", "Minimaal", "Beperkt", "Aanzienlijk", "Substantieel", "Kritiek"}
)

// parseNCTV reads the threat level from the NCTV page. There is no feed or
// structured field, so only this one sentence is read ("… niveau 4 op een schaal
// van 5"); if the wording changes the result is an error ("onbekend"), never a guess.
func parseNCTV(page []byte) (any, error) {
	text := plainText(string(page))
	m := nctvLevelRe.FindStringSubmatch(text)
	if m == nil {
		return nil, errors.New("nctv: threat level sentence not found on page")
	}
	lvl, _ := strconv.Atoi(m[1])
	out := NCTVLevel{Level: lvl, Name: nctvNames[lvl]}
	if d := nctvDTNRe.FindStringSubmatch(text); d != nil {
		out.Since = strings.ToLower(d[1])
	}
	return out, nil
}

type KNMIStatus struct {
	Level  string     `json:"level"`  // none | yellow | orange | red
	Active bool       `json:"active"` // in force now; false = announced for later
	Onset  *time.Time `json:"onset,omitempty"`
	Types  []string   `json:"types,omitempty"`
	Areas  []string   `json:"areas,omitempty"`
	Count  int        `json:"count"`
	Heat   *KNMIHeat  `json:"heat,omitempty"` // a KNMI heat warning (code geel = Nationaal Hitteplan active)
}

type KNMIHeat struct {
	Level  string `json:"level"`
	Active bool   `json:"active"`
}

// knmiSummary: the highest KNMI code among Dutch warnings (MeteoAlarm carries KNMI's codes).
func knmiSummary(ws []WxWarning, now time.Time) KNMIStatus {
	rank := map[string]int{"none": 0, "yellow": 1, "orange": 2, "red": 3}
	s := KNMIStatus{Level: "none"}
	active := func(w WxWarning) bool { return w.Onset.IsZero() || !w.Onset.After(now) }
	for _, w := range ws {
		if w.Country != "nl" {
			continue
		}
		s.Count++
		if r := rank[w.Level]; r > rank[s.Level] || (r == rank[s.Level] && active(w) && !s.Active) {
			s.Level, s.Active = w.Level, active(w)
		}
	}
	seenT, seenA := map[string]bool{}, map[string]bool{}
	for _, w := range ws {
		if w.Country != "nl" || w.Level != s.Level {
			continue
		}
		if !s.Active && !w.Onset.IsZero() && (s.Onset == nil || w.Onset.Before(*s.Onset)) {
			t := w.Onset
			s.Onset = &t
		}
		if !seenT[w.Type] {
			seenT[w.Type] = true
			s.Types = append(s.Types, w.Type)
		}
		if !seenA[w.Area] {
			seenA[w.Area] = true
			s.Areas = append(s.Areas, w.Area)
		}
	}
	for _, w := range ws { // heat on its own: it can hide behind a higher code for wind
		if w.Country != "nl" || w.Type != "Hitte" {
			continue
		}
		if s.Heat == nil || rank[w.Level] > rank[s.Heat.Level] || (rank[w.Level] == rank[s.Heat.Level] && active(w)) {
			s.Heat = &KNMIHeat{Level: w.Level, Active: active(w) || (s.Heat != nil && s.Heat.Active && s.Heat.Level == w.Level)}
		}
	}
	return s
}

func (a *App) feedEntry(key string) map[string]any {
	st := a.threats.get(key)
	e := map[string]any{}
	if !st.FetchedAt.IsZero() {
		e["fetched_at"] = st.FetchedAt.UTC().Truncate(time.Second)
	}
	if st.Err != "" {
		e["error"] = st.Err
		e["error_since"] = st.ErrSince.UTC().Truncate(time.Second)
	}
	return e
}

func (a *App) handleAlerts(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	resp := map[string]any{}
	if cfg.Alerts.NCTV.Enabled {
		e := a.feedEntry("nctv")
		e["url"] = cfg.Alerts.NCTV.URL
		if v, ok := a.threats.get("nctv").Data.(NCTVLevel); ok {
			e["level"], e["name"], e["since"] = v.Level, v.Name, v.Since
		}
		resp["nctv"] = e
	}
	if cfg.Alerts.KNMI {
		ctx := context.WithoutCancel(r.Context())
		ws, at, _, err := a.wx.warnings.get("nl", 10*time.Minute, func() ([]WxWarning, error) { return a.fetchWarnings(ctx, "nl") })
		e := map[string]any{"url": "https://www.knmi.nl/nederland-nu/weer/waarschuwingen"}
		if err != nil {
			e["error"] = "MeteoAlarm is niet bereikbaar"
		} else {
			e["status"], e["fetched_at"] = knmiSummary(ws, time.Now()), at.UTC().Truncate(time.Second)
		}
		resp["knmi"] = e
	}
	if cfg.World.Enabled && cfg.World.Water {
		e := a.feedEntry("rws:water")
		e["url"] = strings.TrimRight(cfg.World.WaterURL, "/") + "/owb/"
		if v, ok := a.threats.get("rws:water").Data.(WaterStatus); ok {
			e["status"] = v
		}
		resp["water"] = e
	}
	if cc := cfg.Alarms.Counts; cfg.Alarms.Enabled && cc.Enabled {
		resp["p2000"] = map[string]any{"window_minutes": 60, "label": cc.Label, "cities": cc.Cities, "services": a.p2kCounts(cc.Cities, time.Now())}
	}
	writeJSON(w, r, http.StatusOK, 30, resp)
}

// ---------------------------------------------------------------------------
// Traffic (NDW open data, Rijkswaterstaat and road authorities).
// Records carry Alert-C location codes; road numbers and names come from NDW's
// VILD location table, read with HTTP range requests (only the ~400 KB table
// inside the 42 MB zip) and kept in memory.

type vildLoc struct {
	Des, Road, Name1, Name2, Exit string
	Lin                           int
}

type vildTable struct {
	version string
	locs    map[int]vildLoc
	at      time.Time
}

type TrafficItem struct {
	Road  string    `json:"road"`
	Dir   string    `json:"dir,omitempty"` // "richting Gorinchem"
	From  string    `json:"from,omitempty"`
	To    string    `json:"to,omitempty"`
	Kind  string    `json:"kind"` // Dutch description
	Delay int       `json:"delay_s,omitempty"`
	Since time.Time `json:"since"`
}

type TrafficData struct {
	Jams          []TrafficItem `json:"jams"`
	JamCount      int           `json:"jam_count"`
	TotalDelay    int           `json:"total_delay_s"`
	Accidents     []TrafficItem `json:"accidents"`
	AccidentCount int           `json:"accident_count"`
	Closures      int           `json:"closures"`
	Table         string        `json:"location_table,omitempty"`
}

type ndwRecord struct {
	typ, status, start, end, delay, abnormal, mgmt, dir, table, tableVer string
	locs                                                                 []int
}

// parseNDW streams a DATEX II situation publication and keeps the fields we use.
func parseNDW(r io.Reader) ([]ndwRecord, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	var recs []ndwRecord
	var cur *ndwRecord
	var stack []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return recs, fmt.Errorf("ndw: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "situationRecord" {
				cur = &ndwRecord{}
				for _, at := range t.Attr {
					if at.Name.Local == "type" {
						_, cur.typ, _ = strings.Cut(at.Value, ":")
					}
				}
			}
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if t.Name.Local == "situationRecord" && cur != nil {
				recs = append(recs, *cur)
				cur = nil
			}
		case xml.CharData:
			if cur == nil || len(stack) == 0 {
				continue
			}
			v := strings.TrimSpace(string(t))
			if v == "" {
				continue
			}
			first := func(dst *string) {
				if *dst == "" {
					*dst = v
				}
			}
			switch stack[len(stack)-1] {
			case "validityStatus":
				first(&cur.status)
			case "overallStartTime":
				first(&cur.start)
			case "overallEndTime":
				first(&cur.end)
			case "delayTimeValue":
				first(&cur.delay)
			case "abnormalTrafficType":
				first(&cur.abnormal)
			case "roadOrCarriagewayOrLaneManagementType":
				first(&cur.mgmt)
			case "alertCDirectionCoded":
				first(&cur.dir)
			case "alertCLocationTableNumber":
				first(&cur.table)
			case "alertCLocationTableVersion":
				first(&cur.tableVer)
			case "specificLocation":
				if n, err := strconv.Atoi(v); err == nil && len(cur.locs) < 4 {
					cur.locs = append(cur.locs, n)
				}
			}
		}
	}
	return recs, nil
}

var abnormalNL = map[string]string{
	"stationaryTraffic": "Stilstaand verkeer", "queuingTraffic": "File", "slowTraffic": "Langzaam rijdend verkeer",
	"heavyTraffic": "Druk verkeer",
}

func (rec ndwRecord) activeAt(now time.Time) bool {
	if rec.status == "suspended" {
		return false
	}
	if t, err := time.Parse(time.RFC3339, rec.start); err == nil && t.After(now) {
		return false
	}
	if t, err := time.Parse(time.RFC3339, rec.end); err == nil && t.Before(now) {
		return false
	}
	return true
}

func vildName(l vildLoc) string {
	if strings.HasPrefix(l.Des, "Knooppunt") {
		return "knp. " + l.Name1
	}
	return l.Name1
}

func describe(rec ndwRecord, t *vildTable) TrafficItem {
	var it TrafficItem
	if t == nil || len(rec.locs) == 0 {
		return it
	}
	p, ok := t.locs[rec.locs[0]] // primary: where the event ends (head of the queue)
	if !ok {
		return it
	}
	it.Road, it.To = p.Road, vildName(p)
	if len(rec.locs) > 1 {
		if s, ok := t.locs[rec.locs[1]]; ok && rec.locs[1] != rec.locs[0] {
			it.From = vildName(s)
		}
	}
	if lin, ok := t.locs[p.Lin]; ok {
		switch rec.dir {
		case "positive":
			if lin.Name2 != "" {
				it.Dir = "richting " + lin.Name2
			}
		case "negative":
			if lin.Name1 != "" {
				it.Dir = "richting " + lin.Name1
			}
		}
	}
	return it
}

func buildTraffic(recs []ndwRecord, t *vildTable, now time.Time) TrafficData {
	d := TrafficData{Jams: []TrafficItem{}, Accidents: []TrafficItem{}}
	if t != nil {
		d.Table = t.version
	}
	best := map[string]int{} // dedupe jams on the same stretch
	for _, rec := range recs {
		if !rec.activeAt(now) {
			continue
		}
		since, _ := time.Parse(time.RFC3339, rec.start)
		switch rec.typ {
		case "AbnormalTraffic":
			it := describe(rec, t)
			it.Kind = abnormalNL[rec.abnormal]
			if it.Kind == "" {
				it.Kind = "File"
			}
			if f, err := strconv.ParseFloat(rec.delay, 64); err == nil && f > 0 {
				it.Delay = int(f)
			}
			it.Since = since.UTC()
			d.JamCount++
			d.TotalDelay += it.Delay
			if it.Road == "" { // counted, but a jam without a location is not worth listing
				continue
			}
			k := it.Road + "|" + it.Dir + "|" + it.From + "|" + it.To
			if i, dup := best[k]; dup {
				if it.Delay > d.Jams[i].Delay {
					d.Jams[i] = it
				}
				continue
			}
			best[k] = len(d.Jams)
			d.Jams = append(d.Jams, it)
		case "Accident":
			it := describe(rec, t)
			it.Kind, it.Since = "Ongeval", since.UTC()
			d.AccidentCount++
			if it.Road != "" {
				d.Accidents = append(d.Accidents, it)
			}
		case "RoadOrCarriagewayOrLaneManagement":
			if rec.mgmt == "roadClosed" || rec.mgmt == "carriagewayClosures" {
				d.Closures++
			}
		}
	}
	sort.SliceStable(d.Jams, func(i, j int) bool { return d.Jams[i].Delay > d.Jams[j].Delay })
	sort.SliceStable(d.Accidents, func(i, j int) bool { return d.Accidents[i].Since.After(d.Accidents[j].Since) })
	d.Jams = d.Jams[:min(30, len(d.Jams))]
	d.Accidents = d.Accidents[:min(15, len(d.Accidents))]
	return d
}

// rangeReaderAt reads a remote file through HTTP range requests (256 KB blocks, cached).
type rangeReaderAt struct {
	ctx   context.Context
	f     *Fetcher
	url   string
	size  int64
	cache map[int64][]byte
}

const rangeBlock = 256 << 10

func newRangeReader(ctx context.Context, f *Fetcher, url string) (*rangeReaderAt, error) {
	resp, err := f.Do(ctx, FetchReq{URL: url, Header: map[string]string{"Range": "bytes=0-0", "Accept-Encoding": "identity"}})
	if err != nil {
		return nil, err
	}
	_, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/")
	size, err := strconv.ParseInt(total, 10, 64)
	if resp.Status != http.StatusPartialContent || !ok || err != nil || size <= 0 {
		return nil, errors.New("server does not support range requests")
	}
	return &rangeReaderAt{ctx: ctx, f: f, url: url, size: size, cache: map[int64][]byte{}}, nil
}

func (r *rangeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for n < len(p) {
		pos := off + int64(n)
		if pos >= r.size {
			return n, io.EOF
		}
		bi := pos / rangeBlock
		blk, ok := r.cache[bi]
		if !ok {
			start := bi * rangeBlock
			end := min(start+rangeBlock, r.size) - 1
			resp, err := r.f.Do(r.ctx, FetchReq{URL: r.url, Header: map[string]string{
				"Range": fmt.Sprintf("bytes=%d-%d", start, end), "Accept-Encoding": "identity"}})
			if err != nil {
				return n, err
			}
			if resp.Status != http.StatusPartialContent || int64(len(resp.Body)) != end-start+1 {
				return n, errors.New("range request not honoured")
			}
			if len(r.cache) > 64 { // a VILD table needs ~5 blocks; refuse to buffer a whole large file
				return n, errors.New("zip read too large")
			}
			blk = resp.Body
			r.cache[bi] = blk
		}
		n += copy(p[n:], blk[pos-bi*rangeBlock:])
	}
	return n, nil
}

// loadVILD fetches VILD<version>.zip's .dbf location table via range requests.
func (a *App) loadVILD(ctx context.Context, version string) (*vildTable, error) {
	url := strings.TrimSuffix(a.config().Traffic.VILDBase, "/") + "/VILD" + version + ".zip"
	ra, err := newRangeReader(ctx, a.fetcher, url)
	if err != nil {
		return nil, fmt.Errorf("vild %s: %w", version, err)
	}
	zr, err := zip.NewReader(ra, ra.size)
	if err != nil {
		return nil, fmt.Errorf("vild zip: %w", err)
	}
	for _, f := range zr.File {
		if strings.Contains(f.Name, "/") || !strings.EqualFold(filepath.Ext(f.Name), ".dbf") || f.UncompressedSize64 > 20<<20 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, 20<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		locs, err := parseVILD(b)
		if err != nil {
			return nil, err
		}
		slog.Info("vild location table loaded", "version", version, "locations", len(locs), "range_requests", len(ra.cache)+1)
		return &vildTable{version: version, locs: locs, at: time.Now()}, nil
	}
	return nil, errors.New("vild: no location table (.dbf) in zip")
}

// parseVILD reads the dBASE III table (Latin-1) with the location codes we need.
func parseVILD(b []byte) (map[int]vildLoc, error) {
	if len(b) < 33 {
		return nil, errors.New("dbf: too short")
	}
	nrec := int(binary.LittleEndian.Uint32(b[4:8]))
	hlen := int(binary.LittleEndian.Uint16(b[8:10]))
	rlen := int(binary.LittleEndian.Uint16(b[10:12]))
	type field struct {
		name     string
		off, len int
	}
	fields := map[string]field{}
	off := 1 // byte 0 of each record is the deletion flag
	for p := 32; p+32 <= len(b) && p < hlen && b[p] != 0x0d; p += 32 {
		name := strings.TrimRight(string(b[p:p+11]), "\x00")
		l := int(b[p+16])
		fields[name] = field{name, off, l}
		off += l
	}
	if off > rlen || hlen+nrec*rlen > len(b) {
		return nil, errors.New("dbf: inconsistent header")
	}
	get := func(rec []byte, name string) string {
		f, ok := fields[name]
		if !ok {
			return ""
		}
		return strings.TrimSpace(latin1(rec[f.off : f.off+f.len]))
	}
	locs := make(map[int]vildLoc, nrec)
	for i := 0; i < nrec; i++ {
		rec := b[hlen+i*rlen : hlen+(i+1)*rlen]
		if rec[0] == '*' { // deleted
			continue
		}
		nr, err := strconv.Atoi(get(rec, "LOC_NR"))
		if err != nil {
			continue
		}
		lin, _ := strconv.Atoi(get(rec, "LIN_REF"))
		locs[nr] = vildLoc{Des: get(rec, "LOC_DES"), Road: get(rec, "ROADNUMBER"), Name1: get(rec, "FIRST_NAME"),
			Name2: get(rec, "SECND_NAME"), Exit: get(rec, "EXIT_NR"), Lin: lin}
	}
	if len(locs) == 0 {
		return nil, errors.New("dbf: no locations")
	}
	return locs, nil
}

func latin1(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 0x80 && c <= 0x9f {
			sb.WriteRune(cp1252[c-0x80])
		} else {
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

func (a *App) runTraffic(ctx context.Context) error {
	const key = "ndw:traffic"
	cfg := a.config().Traffic
	prev := a.threats.get(key)
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: cfg.URL, Accept: "application/gzip, application/xml;q=0.9, */*;q=0.5",
		ETag: prev.ETag, LastModified: prev.LastMod})
	if err == nil && resp.NotModified && prev.Data != nil {
		a.threats.ok(key, prev.Data, prev.ETag, prev.LastMod)
		return nil
	}
	if err == nil {
		var body io.Reader = bytes.NewReader(resp.Body)
		if len(resp.Body) > 2 && resp.Body[0] == 0x1f && resp.Body[1] == 0x8b {
			var zr *gzip.Reader
			if zr, err = gzip.NewReader(body); err == nil {
				body = io.LimitReader(zr, 40<<20)
			}
		}
		var recs []ndwRecord
		if err == nil {
			recs, err = parseNDW(body)
		}
		if err == nil {
			version := ""
			for _, r := range recs {
				if r.table != "" {
					version = r.table + "." + r.tableVer
					break
				}
			}
			t := a.vild.Load()
			if version != "" && (t == nil || t.version != version || time.Since(t.at) > 7*24*time.Hour) {
				if nt, lerr := a.loadVILD(ctx, version); lerr != nil {
					slog.Warn("vild load failed, road names unavailable", "err", lerr)
				} else {
					a.vild.Store(nt)
					t = nt
				}
			}
			a.threats.ok(key, buildTraffic(recs, t, time.Now()), resp.ETag, resp.LastMo)
			return nil
		}
	}
	a.threats.fail(key, err)
	slog.Warn("fetch failed", "source", key, "err", err)
	return err
}

func (a *App) handleTraffic(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Traffic.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	resp := a.feedEntry("ndw:traffic")
	resp["enabled"] = true
	resp["source"] = map[string]string{"name": "NDW (Rijkswaterstaat, provincies en gemeenten)", "url": "https://www.ndw.nu/", "license": "open data"}
	if v, ok := a.threats.get("ndw:traffic").Data.(TrafficData); ok {
		resp["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 60, resp)
}

// ---------------------------------------------------------------------------
// Datalekken: the latest company breaches from Have I Been Pwned (CC BY 4.0).
// The public breach list needs no key. Spam lists, malware/stealer logs,
// fabricated, retired, unverified and (by default) sensitive entries are left
// out, as are entries without a domain: what remains are breaches of an organisation.
// "NL" = a .nl domain, or the description mentions Dutch / the Netherlands.

type Breach struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Domain      string    `json:"domain"`
	URL         string    `json:"url"`
	BreachDate  string    `json:"breach_date,omitempty"` // YYYY-MM-DD
	Added       time.Time `json:"added"`
	Count       int64     `json:"count"`
	DataClasses []string  `json:"data_classes,omitempty"`
	Summary     string    `json:"summary,omitempty"`
}

type BreachData struct {
	NL     []Breach `json:"nl"`
	Other  []Breach `json:"other"`
	Total  int      `json:"total"`  // entries in the HIBP list
	Shown  int      `json:"shown"`  // entries left after the filters
	Latest string   `json:"latest"` // newest AddedDate in the list, for the freshness line
}

var (
	breachNLRe     = regexp.MustCompile(`(?i)\b(dutch|netherlands|nederland)\b`)
	breachNameRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	breachDateRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	breachPerGroup = 3
)

func parseBreaches(body []byte, includeSensitive bool, now time.Time) (any, error) {
	var list []struct {
		Name, Title, Domain, BreachDate, Description     string
		AddedDate                                        time.Time
		PwnCount                                         int64
		DataClasses                                      []string
		IsVerified, IsFabricated, IsSensitive, IsRetired bool
		IsSpamList, IsMalware, IsStealerLog              bool
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("hibp: %w", err)
	}
	if len(list) == 0 {
		return nil, errors.New("hibp: empty breach list")
	}
	d := BreachData{NL: []Breach{}, Other: []Breach{}, Total: len(list)}
	var keep []Breach
	nl := map[string]bool{}
	for _, b := range list {
		if b.AddedDate.After(now.Add(24*time.Hour)) || !breachNameRe.MatchString(b.Name) {
			continue
		}
		if s := b.AddedDate.UTC().Format(time.RFC3339); s > d.Latest {
			d.Latest = s
		}
		if !b.IsVerified || b.IsFabricated || b.IsRetired || b.IsSpamList || b.IsMalware || b.IsStealerLog ||
			(b.IsSensitive && !includeSensitive) || strings.TrimSpace(b.Domain) == "" {
			continue
		}
		desc := plainText(b.Description)
		domain := strings.ToLower(strings.TrimSpace(b.Domain))
		x := Breach{Name: b.Name, Title: truncate(plainText(firstNonEmpty(b.Title, b.Name)), 100), Domain: truncate(plainText(domain), 80),
			URL: "https://haveibeenpwned.com/Breach/" + url.PathEscape(b.Name), Added: b.AddedDate.UTC(), Count: max(b.PwnCount, 0),
			Summary: truncate(desc, 300)}
		if breachDateRe.MatchString(b.BreachDate) {
			x.BreachDate = b.BreachDate
		}
		for _, c := range b.DataClasses {
			if len(x.DataClasses) == 6 {
				break
			}
			if c = truncate(plainText(c), 40); c != "" {
				x.DataClasses = append(x.DataClasses, c)
			}
		}
		keep = append(keep, x)
		nl[b.Name] = strings.HasSuffix(domain, ".nl") || breachNLRe.MatchString(desc)
	}
	d.Shown = len(keep)
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].Added.After(keep[j].Added) })
	for _, x := range keep {
		if nl[x.Name] && len(d.NL) < breachPerGroup {
			d.NL = append(d.NL, x)
		} else if !nl[x.Name] && len(d.Other) < breachPerGroup {
			d.Other = append(d.Other, x)
		}
	}
	return d, nil
}

func (a *App) handleBreaches(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Breaches.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("hibp:breaches")
	e["enabled"] = true
	if v, ok := a.threats.get("hibp:breaches").Data.(BreachData); ok {
		e["data"] = v
	}
	if cfg.Breaches.Phishing {
		ph := a.feedEntry("fhd:alerts")
		if v, ok := a.threats.get("fhd:alerts").Data.([]ScamAlert); ok {
			ph["items"] = v
		}
		ph["url"] = "https://www.fraudehelpdesk.nl/actueel/"
		e["phishing"] = ph
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}

// ---------------------------------------------------------------------------
// Outages of online services (status pages). Formats: statuspage (Atlassian
// Statuspage summary.json, used by Cloudflare, STACKIT and many others), rss
// (Azure, AWS), m365 (the Microsoft 365 public status page) and gcp (Google
// Cloud incidents.json).

type OutageIncident struct {
	Title    string    `json:"title"`
	URL      string    `json:"url,omitempty"`
	Status   string    `json:"status,omitempty"`
	Updated  time.Time `json:"updated"`
	Resolved bool      `json:"resolved,omitempty"`
}

type OutageData struct {
	Status    string           `json:"status"` // ok | minor | major
	Summary   string           `json:"summary,omitempty"`
	Incidents []OutageIncident `json:"incidents"`
}

var statuspageNL = map[string]string{"investigating": "onderzoek", "identified": "oorzaak gevonden", "monitoring": "hersteld, wordt gevolgd",
	"resolved": "opgelost", "scheduled": "gepland", "in_progress": "bezig", "verifying": "wordt gecontroleerd", "completed": "afgerond"}

func parseStatuspage(body []byte) (any, error) {
	var s struct {
		Status struct {
			Indicator   string `json:"indicator"`
			Description string `json:"description"`
		} `json:"status"`
		Incidents []struct {
			Name      string    `json:"name"`
			Status    string    `json:"status"`
			Impact    string    `json:"impact"`
			Shortlink string    `json:"shortlink"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"incidents"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("statuspage: %w", err)
	}
	d := OutageData{Status: "ok", Summary: plainText(s.Status.Description), Incidents: []OutageIncident{}}
	switch s.Status.Indicator {
	case "minor", "maintenance":
		d.Status = "minor"
	case "major", "critical":
		d.Status = "major"
	case "none":
	default:
		return nil, fmt.Errorf("statuspage: unknown indicator %q", s.Status.Indicator)
	}
	for _, in := range s.Incidents {
		if len(d.Incidents) == 10 {
			break
		}
		d.Incidents = append(d.Incidents, OutageIncident{Title: truncate(plainText(in.Name), 160), URL: safeURL(in.Shortlink, nil),
			Status: statuspageNL[in.Status], Updated: in.UpdatedAt.UTC(), Resolved: in.Status == "resolved"})
	}
	return d, nil
}

func parseM365(body []byte) (any, error) {
	var svcs []struct {
		Name    string `json:"ServiceDisplayName"`
		Status  string `json:"Status"`
		Title   string `json:"Title"`
		Updated string `json:"LastUpdatedTime"`
	}
	if err := json.Unmarshal(body, &svcs); err != nil {
		return nil, fmt.Errorf("m365: %w", err)
	}
	if len(svcs) == 0 {
		return nil, errors.New("m365: empty status list")
	}
	d := OutageData{Status: "ok", Incidents: []OutageIncident{}}
	for _, s := range svcs {
		if strings.EqualFold(s.Status, "Operational") || s.Status == "" {
			continue
		}
		sev := "minor"
		if l := strings.ToLower(s.Status); strings.Contains(l, "interruption") || strings.Contains(l, "outage") {
			sev = "major"
		}
		if d.Status != "major" {
			d.Status = sev
		}
		t, _ := parseDate(s.Updated)
		title := plainText(s.Name)
		if tt := plainText(s.Title); tt != "" {
			title += ": " + tt
		}
		d.Incidents = append(d.Incidents, OutageIncident{Title: truncate(title, 160), Status: plainText(s.Status), Updated: t.UTC()})
	}
	return d, nil
}

// parseStatusRSS: incidents published in the last 24 h. AWS keeps history in its
// feed; "Service is operating normally" / "[RESOLVED]" items count as resolved.
func parseStatusRSS(body []byte, now time.Time) (any, error) {
	raws, err := parseFeed(body, "")
	if err != nil {
		return nil, err
	}
	d := OutageData{Status: "ok", Incidents: []OutageIncident{}}
	for _, r := range raws {
		t, ok := parseDate(r.Date)
		if !ok {
			t = now
		}
		if now.Sub(t) > 24*time.Hour {
			continue
		}
		title := truncate(plainText(r.Title), 160)
		lt := strings.ToLower(title)
		resolved := strings.HasPrefix(lt, "service is operating normally") || strings.Contains(lt, "[resolved]") || strings.HasPrefix(lt, "resolved")
		if !resolved {
			d.Status = "minor"
		}
		if len(d.Incidents) < 10 {
			d.Incidents = append(d.Incidents, OutageIncident{Title: title, URL: safeURL(r.Link, nil), Updated: t.UTC(), Resolved: resolved})
		}
	}
	return d, nil
}

// parseGCP reads status.cloud.google.com/incidents.json: incidents without an end
// are ongoing (an outage or high severity is major, a disruption minor, an
// informational notice leaves the status ok); those that ended in the last 24 h
// are shown as resolved. The affected locations (e.g. europe-west4) follow the title.
func parseGCP(body []byte, now time.Time) (any, error) {
	var list []struct {
		End          string `json:"end"`
		Modified     string `json:"modified"`
		Desc         string `json:"external_desc"`
		Severity     string `json:"severity"`
		StatusImpact string `json:"status_impact"`
		URI          string `json:"uri"`
		Current      []struct {
			ID string `json:"id"`
		} `json:"currently_affected_locations"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("gcp: %w", err)
	}
	d := OutageData{Status: "ok", Incidents: []OutageIncident{}}
	for _, in := range list {
		upd, _ := parseDate(in.Modified)
		ended, hasEnd := parseDate(in.End)
		resolved := in.End != ""
		if resolved && (!hasEnd || now.Sub(ended) > 24*time.Hour) {
			continue
		}
		title := plainText(in.Desc)
		if title == "" {
			continue
		}
		status := map[string]string{"SERVICE_OUTAGE": "storing", "SERVICE_DISRUPTION": "verstoring", "SERVICE_INFORMATION": "informatie"}[in.StatusImpact]
		if resolved {
			status = "opgelost"
		} else if in.StatusImpact != "SERVICE_INFORMATION" {
			if in.StatusImpact == "SERVICE_OUTAGE" || in.Severity == "high" {
				d.Status = "major"
			} else if d.Status == "ok" {
				d.Status = "minor"
			}
		}
		var locs []string
		for _, l := range in.Current {
			if id := plainText(l.ID); id != "" && len(locs) < 3 {
				locs = append(locs, id)
			}
		}
		if len(locs) > 0 {
			title += " (" + strings.Join(locs, ", ") + ")"
		}
		if len(d.Incidents) < 10 {
			d.Incidents = append(d.Incidents, OutageIncident{Title: truncate(title, 160), URL: safeURL("https://status.cloud.google.com/"+strings.TrimPrefix(in.URI, "/"), nil),
				Status: status, Updated: upd.UTC(), Resolved: resolved})
		}
	}
	return d, nil
}

func (a *App) handleOutages(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Outages.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	var list []map[string]any
	for _, p := range cfg.Outages.Providers {
		if !p.IsEnabled() {
			continue
		}
		e := a.feedEntry("outage:" + p.ID)
		e["id"], e["name"], e["url"] = p.ID, p.Name, firstNonEmpty(p.Homepage, p.URL)
		if v, ok := a.threats.get("outage:" + p.ID).Data.(OutageData); ok {
			e["data"] = v
		}
		list = append(list, e)
	}
	resp := map[string]any{"enabled": true, "providers": list}
	if cfg.Outages.Internet.Enabled {
		e := a.feedEntry("ioda:internet")
		e["url"] = "https://ioda.inetintel.cc.gatech.edu/country/" + cfg.Outages.Internet.Country
		if v, ok := a.threats.get("ioda:internet").Data.([]InternetEntity); ok {
			e["entities"] = v
		}
		resp["internet"] = e
	}
	writeJSON(w, r, http.StatusOK, 60, resp)
}

// ---------------------------------------------------------------------------
// Alarmeringen: P2000 alerts per city from Zwaailicht.nl (Atom, refreshed every
// minute). Grouped per service, at most two each. Fetched on demand per city
// and cached; house numbers are already left out by Zwaailicht.

type Alarm struct {
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Time    time.Time `json:"time"`
	Urgency string    `json:"urgency,omitempty"` // spoed | geen spoed | gepland
	Units   string    `json:"units,omitempty"`
	Detail  string    `json:"detail,omitempty"` // e.g. "ernstig letsel", "gevaarlijke stoffen"
	City    string    `json:"city,omitempty"`   // slug, set for alerts elsewhere in the country
	service string
	id      string // Atom <id>, unique per alert
}

type AlarmGroup struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Scope string  `json:"scope"` // city | national
	Items []Alarm `json:"items"`
}

var (
	alarmServices   = []struct{ id, name string }{{"brandweer", "Brandweer"}, {"ambulance", "Ambulance"}, {"politie", "Politie"}, {"lifeliner", "Lifeliner"}}
	citySlugRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,48}$`)
	unitsRe         = regexp.MustCompile(`(?i)Ingezet:\s*([^.]+)\.`)
	detailRe        = regexp.MustCompile(`\(([^)]{3,60})\)|Let op:\s*([^.]{3,80})`)
	errCityNotFound = errors.New("city not found")
)

// parseAlarms reads a Zwaailicht Atom feed: title, summary, link, updated and
// the service/city categories.
func parseAlarms(body []byte) ([]Alarm, error) {
	var f struct {
		Entries []struct {
			ID      string `xml:"id"`
			Title   string `xml:"title"`
			Summary string `xml:"summary"`
			Updated string `xml:"updated"`
			Links   []struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
			Cats []struct {
				Term string `xml:"term,attr"`
			} `xml:"category"`
		} `xml:"entry"`
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("zwaailicht: %w", err)
	}
	out := make([]Alarm, 0, len(f.Entries))
	for _, e := range f.Entries {
		if len(e.Cats) == 0 || len(e.Links) == 0 {
			continue
		}
		a := Alarm{service: strings.ToLower(e.Cats[0].Term), id: firstNonEmpty(e.ID, e.Links[0].Href)}
		if len(e.Cats) > 1 {
			a.City = strings.ToLower(e.Cats[1].Term)
		}
		// the title starts with a service emoji; the panel shows its own label
		a.Title = truncate(strings.TrimLeftFunc(plainText(e.Title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), 140)
		a.URL = safeURL(e.Links[0].Href, nil)
		if !strings.HasPrefix(a.URL, "https://zwaailicht.nl/") {
			continue
		}
		t, ok := parseDate(e.Updated)
		if !ok {
			continue
		}
		a.Time = t.UTC()
		sum := plainText(e.Summary)
		switch l := strings.ToLower(sum); {
		case strings.Contains(l, "zonder spoed"):
			a.Urgency = "geen spoed"
		case strings.Contains(l, "met spoed"), strings.Contains(l, "prio 1"), strings.Contains(l, "a1 "):
			a.Urgency = "spoed"
		case strings.Contains(l, "gepland vervoer"):
			a.Urgency = "gepland"
		}
		if m := unitsRe.FindStringSubmatch(sum); m != nil {
			a.Units = truncate(strings.TrimSpace(m[1]), 80)
		}
		if m := detailRe.FindStringSubmatch(sum); m != nil {
			a.Detail = strings.TrimSpace(m[1] + m[2])
		}
		out = append(out, a)
	}
	return out, nil
}

// groupAlarms: at most two per service, newest first. Lifeliner flights are rare
// per city; when there is none, the latest national ones are shown (scope "national").
func groupAlarms(city, national []Alarm) []AlarmGroup {
	sort.SliceStable(city, func(i, j int) bool { return city[i].Time.After(city[j].Time) })
	groups := make([]AlarmGroup, 0, len(alarmServices))
	for _, s := range alarmServices {
		g := AlarmGroup{ID: s.id, Name: s.name, Scope: "city", Items: []Alarm{}}
		for _, a := range city {
			if a.service == s.id && len(g.Items) < 2 {
				a.City = ""
				g.Items = append(g.Items, a)
			}
		}
		if s.id == "lifeliner" && len(g.Items) == 0 && len(national) > 0 {
			g.Scope = "national"
			for _, a := range national {
				if len(g.Items) < 2 {
					g.Items = append(g.Items, a)
				}
			}
		}
		groups = append(groups, g)
	}
	return groups
}

func (a *App) fetchAlarmFeed(ctx context.Context, slug string) ([]Alarm, error) {
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: strings.TrimSuffix(a.config().Alarms.Base, "/") + "/" + slug + ".xml", Accept: feedAccept})
	if resp != nil && resp.Status == http.StatusNotFound {
		return nil, errCityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("zwaailicht: %w", err)
	}
	return parseAlarms(resp.Body)
}

func (a *App) handleAlarms(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Alarms.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	slug := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("city")))
	if slug == "" {
		slug = cfg.Alarms.City
	}
	if !citySlugRe.MatchString(slug) || slug == "lifeliner" || slug == "brandweer" || slug == "ambulance" || slug == "politie" {
		writeError(w, r, http.StatusBadRequest, "ongeldige plaatsnaam")
		return
	}
	ip := a.clientIP(r)
	ctx := context.WithoutCancel(r.Context())
	ttl := cfg.Alarms.Interval.D()
	items, at, stale, err := a.alarms.get(slug, ttl, func() ([]Alarm, error) {
		if !a.wx.limiter.allow(ip) {
			return nil, errRateLimited
		}
		return a.fetchAlarmFeed(ctx, slug)
	})
	switch {
	case errors.Is(err, errCityNotFound):
		writeError(w, r, http.StatusNotFound, "plaats niet gevonden bij Zwaailicht.nl")
		return
	case errors.Is(err, errRateLimited):
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
		return
	case err != nil:
		writeError(w, r, http.StatusBadGateway, "Zwaailicht.nl is niet bereikbaar")
		return
	}
	var national []Alarm
	hasLife := false
	for _, it := range items {
		hasLife = hasLife || it.service == "lifeliner"
	}
	if !hasLife { // shared cache entry for the national Lifeliner feed
		national, _, _, _ = a.alarms.get("lifeliner", ttl, func() ([]Alarm, error) { return a.fetchAlarmFeed(ctx, "lifeliner") })
	}
	writeJSON(w, r, http.StatusOK, 60, map[string]any{
		"enabled": true, "city": slug, "groups": groupAlarms(append([]Alarm(nil), items...), national),
		"fetched_at": at.UTC().Truncate(time.Second), "stale": stale,
		"source": map[string]string{"name": "Zwaailicht.nl (P2000)", "url": "https://zwaailicht.nl/"},
	})
}

// ---------------------------------------------------------------------------
// P2000 counts for the top bar: alerts per service in the last hour for a
// configured area (one or more city feeds, e.g. Den Haag, or a whole safety
// region). A city feed holds its latest 50 alerts; the feeds are polled and alert
// ids kept in a 60-minute window. A count is "complete" only when the polls
// cover the whole hour without a gap.

func newP2KCounters() map[string]*p2kCounter { return map[string]*p2kCounter{} }

var p2kServices = []struct{ id, name, icon string }{
	{"brandweer", "Brandweer", "🔥"}, {"ambulance", "Ambulance", "🚑"}, {"politie", "Politie", "🚔"},
	{"lifeliner", "Lifeliner", "🚁"}, {"knrm", "KNRM", "⛵"},
}

type p2kSeen struct {
	t       time.Time
	service string
}

// p2kCounter tracks one feed (one city).
type p2kCounter struct {
	mu        sync.Mutex
	seen      map[string]p2kSeen
	covered   time.Time // counts are complete for alerts after this moment
	lastFetch time.Time
}

// add merges one poll of a feed (its newest 50 alerts).
func (c *p2kCounter) add(items []Alarm, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]p2kSeen{}
	}
	oldest := now
	for _, a := range items {
		c.seen[a.id] = p2kSeen{a.Time, a.service}
		if a.Time.Before(oldest) {
			oldest = a.Time
		}
	}
	full := len(items) >= 50 // a full feed reaches back only to its oldest entry
	switch {
	case c.lastFetch.IsZero() && full:
		c.covered = oldest
	case c.lastFetch.IsZero():
		c.covered = time.Time{} // the feed holds everything recent
	case full && oldest.After(c.lastFetch):
		c.covered = oldest // alerts between the previous poll and this feed's oldest entry were missed
	}
	c.lastFetch = now
	for id, s := range c.seen {
		if now.Sub(s.t) > 2*time.Hour {
			delete(c.seen, id)
		}
	}
}

// count returns the alerts of one service ("" = all) in the hour before now.
func (c *p2kCounter) count(now time.Time, service string) (n int, complete bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	since := now.Add(-time.Hour)
	for _, s := range c.seen {
		if s.t.After(since) && !s.t.After(now) && (service == "" || s.service == service) {
			n++
		}
	}
	complete = !c.lastFetch.IsZero() && !c.covered.After(since) && now.Sub(c.lastFetch) < 15*time.Minute
	return n, complete
}

type P2KCount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Count    int    `json:"count"`
	Complete bool   `json:"complete"`
}

func (a *App) p2kCounts(cities []string, now time.Time) []P2KCount {
	out := make([]P2KCount, 0, len(p2kServices))
	for _, s := range p2kServices {
		pc := P2KCount{ID: s.id, Name: s.name, Icon: s.icon, Complete: true}
		for _, city := range cities {
			c := a.p2k[city]
			if c == nil {
				pc.Complete = false
				continue
			}
			n, complete := c.count(now, s.id)
			pc.Count += n
			pc.Complete = pc.Complete && complete
		}
		out = append(out, pc)
	}
	return out
}

func (a *App) p2kJobs(cfg *Config) []Job {
	cc := cfg.Alarms.Counts
	if !cfg.Alarms.Enabled || !cc.Enabled {
		return nil
	}
	var jobs []Job
	for _, city := range cc.Cities {
		city := city
		if a.p2k[city] == nil {
			a.p2k[city] = &p2kCounter{}
		}
		c := a.p2k[city]
		jobs = append(jobs, Job{Key: "p2k:" + city, Interval: cc.Interval.D(), Run: func(ctx context.Context) error {
			items, err := a.fetchAlarmFeed(ctx, city)
			if err != nil {
				a.threats.fail("p2k:"+city, err)
				slog.Warn("fetch failed", "source", "p2k:"+city, "err", err)
				return err
			}
			c.add(items, time.Now())
			a.threats.ok("p2k:"+city, len(items), "", "")
			return nil
		}})
	}
	return jobs
}

// ---------------------------------------------------------------------------
// Energieprijzen: day-ahead electricity and gas prices from EnergyZero (no key).
// With inclBtw=true the API rounds to whole cents, so market prices are fetched
// without VAT and VAT (plus optional fixed extras such as energy tax and the
// supplier's markup, from config) is added here.

type EnergyPoint struct {
	Time  time.Time `json:"t"`
	Price float64   `json:"p"` // €/kWh or €/m³
}

type EnergyData struct {
	Electricity []EnergyPoint `json:"electricity"` // hourly: today and, from about 13:00, tomorrow
	Gas         []EnergyPoint `json:"gas"`
	AllIn       bool          `json:"all_in"` // extras configured: prices approximate the full consumer price
}

func parseEnergyZero(body []byte, vat, extra float64) ([]EnergyPoint, error) {
	var r struct {
		Prices []struct {
			ReadingDate time.Time `json:"readingDate"`
			Price       *float64  `json:"price"`
		} `json:"Prices"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("energyzero: %w", err)
	}
	out := make([]EnergyPoint, 0, len(r.Prices))
	for _, p := range r.Prices {
		if p.Price == nil || *p.Price < -5 || *p.Price > 10 || p.ReadingDate.IsZero() {
			continue
		}
		v := *p.Price*(1+vat) + extra
		out = append(out, EnergyPoint{Time: p.ReadingDate.UTC(), Price: math.Round(v*1e5) / 1e5})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

func (a *App) runEnergy(ctx context.Context) error {
	const key = "energyzero"
	cfg := a.config().Energy
	now := time.Now().In(amsterdam)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, amsterdam)
	till := from.AddDate(0, 0, 2).Add(-time.Millisecond)
	get := func(usage int, extra float64) ([]EnergyPoint, error) {
		u := fmt.Sprintf("%s?fromDate=%s&tillDate=%s&interval=4&usageType=%d&inclBtw=false", cfg.URL,
			from.UTC().Format("2006-01-02T15:04:05.000Z"), till.UTC().Format("2006-01-02T15:04:05.000Z"), usage)
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
		if err != nil {
			return nil, fmt.Errorf("energyzero: %w", err)
		}
		return parseEnergyZero(resp.Body, cfg.VAT, extra)
	}
	el, err := get(1, cfg.ElectricityExtra)
	if err == nil && len(el) == 0 {
		err = errors.New("energyzero: no electricity prices")
	}
	var gas []EnergyPoint
	if err == nil {
		gas, err = get(3, cfg.GasExtra)
	}
	if err != nil {
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	a.threats.ok(key, EnergyData{Electricity: el, Gas: gas, AllIn: cfg.ElectricityExtra != 0 || cfg.GasExtra != 0}, "", "")
	return nil
}

func (a *App) handleEnergy(w http.ResponseWriter, r *http.Request) {
	if !a.config().Energy.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("energyzero")
	e["enabled"] = true
	if v, ok := a.threats.get("energyzero").Data.(EnergyData); ok {
		e["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 60, e)
}

// ---------------------------------------------------------------------------
// Luchtkwaliteit: the Luchtkwaliteitsindex (LKI, 1–11) and pollutant values from
// the nearest Luchtmeetnet station (RIVM, GGD, DCMR, provinces; open data).
// Station coordinates come from RIVM's station list (one CSV, daily), the LKI of
// all stations from the Luchtmeetnet API on the configured interval (≈3 requests),
// and pollutants per station on demand (cached 30 min).

type airStation struct {
	Number, Name, Municipality string
	Lat, Lon                   float64
}

type airLKI struct {
	Value float64
	At    time.Time
}

type AirComponent struct {
	Formula string    `json:"formula"`
	Value   float64   `json:"value"`
	At      time.Time `json:"at"`
}

var (
	airStationRe = regexp.MustCompile(`^[A-Z]{2}[0-9A-Z]{3,8}$`)
	airFormulas  = []string{"NO2", "PM25", "PM10", "O3"}
	errLMLBusy   = errors.New("luchtmeetnet: HTTP 429, too many requests")
)

type lmlPage struct {
	Pagination struct {
		LastPage int `json:"last_page"`
	} `json:"pagination"`
	Data json.RawMessage `json:"data"`
}

func (a *App) lmlGet(ctx context.Context, path string) (lmlPage, error) {
	var p lmlPage
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: strings.TrimSuffix(a.config().Air.Base, "/") + path, Accept: "application/json"})
	if resp != nil && resp.Status == http.StatusTooManyRequests {
		return p, errLMLBusy
	}
	if err != nil {
		return p, fmt.Errorf("luchtmeetnet: %w", err)
	}
	if err := json.Unmarshal(resp.Body, &p); err != nil {
		return p, fmt.Errorf("luchtmeetnet: %w", err)
	}
	return p, nil
}

// parseLMLStations reads RIVM's list of measuring locations (luchtmeetnet_meetlocaties.csv,
// semicolon-separated): id;source;name;place;lat;lon;height;start;end. Stations with an end date are
// closed and left out. One file, fetched once a day with a conditional GET, replaces ~100 API calls.
func parseLMLStations(body []byte) (any, error) {
	out := map[string]airStation{}
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "meetlocatie_id") {
			continue
		}
		f := strings.Split(line, ";")
		if len(f) < 9 || !airStationRe.MatchString(f[0]) || strings.TrimSpace(f[8]) != "" {
			continue
		}
		lat, err1 := strconv.ParseFloat(f[4], 64)
		lon, err2 := strconv.ParseFloat(f[5], 64)
		if err1 != nil || err2 != nil || lat < 50 || lat > 54 || lon < 2.5 || lon > 7.5 {
			continue
		}
		out[f[0]] = airStation{Number: f[0], Name: truncate(plainText(f[2]), 80), Municipality: truncate(plainText(f[3]), 60), Lat: lat, Lon: lon}
	}
	if len(out) < 10 {
		return nil, fmt.Errorf("luchtmeetnet: only %d active stations in the station list", len(out))
	}
	return out, nil
}

func (a *App) runAirLKI(ctx context.Context) error {
	const key = "lml:lki"
	now := time.Now().UTC()
	span := fmt.Sprintf("start=%s&end=%s", now.Add(-3*time.Hour).Format("2006-01-02T15:04:05Z"), now.Format("2006-01-02T15:04:05Z"))
	out := map[string]airLKI{}
	for page, last := 1, 1; page <= last && page <= 20; page++ {
		p, err := a.lmlGet(ctx, fmt.Sprintf("/lki?%s&order_by=timestamp_measured&order_direction=desc&page=%d", span, page))
		if err != nil {
			a.threats.fail(key, err)
			return err
		}
		last = p.Pagination.LastPage
		var list []struct {
			Station string    `json:"station_number"`
			Value   float64   `json:"value"`
			At      time.Time `json:"timestamp_measured"`
		}
		if err := json.Unmarshal(p.Data, &list); err != nil {
			a.threats.fail(key, err)
			return err
		}
		for _, m := range list {
			if m.Value < 1 || m.Value > 11 || !airStationRe.MatchString(m.Station) {
				continue
			}
			if cur, ok := out[m.Station]; !ok || m.At.After(cur.At) {
				out[m.Station] = airLKI{Value: m.Value, At: m.At.UTC()}
			}
		}
	}
	a.threats.ok(key, out, "", "")
	return nil
}

func (a *App) fetchAirComponents(ctx context.Context, station string) ([]AirComponent, error) {
	now := time.Now().UTC()
	p, err := a.lmlGet(ctx, fmt.Sprintf("/measurements?station_number=%s&start=%s&end=%s&order_by=timestamp_measured&order_direction=desc", station,
		now.Add(-4*time.Hour).Format("2006-01-02T15:04:05Z"), now.Format("2006-01-02T15:04:05Z")))
	if err != nil {
		return nil, err
	}
	var list []struct {
		Formula string    `json:"formula"`
		Value   float64   `json:"value"`
		At      time.Time `json:"timestamp_measured"`
	}
	if err := json.Unmarshal(p.Data, &list); err != nil {
		return nil, fmt.Errorf("luchtmeetnet: %w", err)
	}
	latest := map[string]AirComponent{}
	for _, m := range list {
		if cur, ok := latest[m.Formula]; (!ok || m.At.After(cur.At)) && m.Value >= 0 && m.Value < 5000 {
			latest[m.Formula] = AirComponent{Formula: m.Formula, Value: math.Round(m.Value*10) / 10, At: m.At.UTC()}
		}
	}
	out := []AirComponent{}
	for _, f := range airFormulas {
		if c, ok := latest[f]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}

// nearestAirStation: the closest station that reported an LKI in the last 3 hours.
func nearestAirStation(stations map[string]airStation, lki map[string]airLKI, lat, lon float64) (airStation, airLKI, float64, bool) {
	var best airStation
	var bl airLKI
	bd := math.MaxFloat64
	for n, s := range stations {
		l, ok := lki[n]
		if !ok {
			continue
		}
		if d := distanceKm(lat, lon, s.Lat, s.Lon); d < bd || (d == bd && n < best.Number) {
			best, bl, bd = s, l, d
		}
	}
	return best, bl, bd, bd < math.MaxFloat64
}

func (a *App) handleAir(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Air.Enabled {
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
	stations, _ := a.threats.get("lml:stations").Data.(map[string]airStation)
	lki, _ := a.threats.get("lml:lki").Data.(map[string]airLKI)
	e := a.feedEntry("lml:lki")
	e["enabled"] = true
	e["source"] = map[string]string{"name": "Luchtmeetnet", "url": "https://www.luchtmeetnet.nl/"}
	s, l, dist, ok := nearestAirStation(stations, lki, lat, lon)
	if !ok {
		if st := a.threats.get("lml:stations"); st.Err != "" && e["error"] == nil && len(stations) == 0 {
			e["error"], e["error_since"] = st.Err, st.ErrSince.UTC().Truncate(time.Second)
		}
		writeJSON(w, r, http.StatusOK, 60, e)
		return
	}
	e["station"] = map[string]any{"number": s.Number, "name": s.Name, "municipality": s.Municipality,
		"distance_km": math.Round(dist*10) / 10, "url": "https://www.luchtmeetnet.nl/meetpunten?station=" + url.QueryEscape(s.Number)}
	e["lki"] = map[string]any{"value": l.Value, "at": l.At}
	ip := a.clientIP(r)
	ctx := context.WithoutCancel(r.Context())
	comps, _, _, err := a.air.get(s.Number, 30*time.Minute, func() ([]AirComponent, error) {
		if !a.wx.limiter.allow(ip) {
			return nil, errRateLimited
		}
		return a.fetchAirComponents(ctx, s.Number)
	})
	if err == nil {
		e["components"] = comps
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}

// ---------------------------------------------------------------------------
// Treinstoringen: current disruptions from the NS Disruptions API (v3). Needs a
// free subscription key (keys.ns_api_key or NS_API_KEY); without one the panel
// explains how to get it. Parsing is lenient: unknown fields are ignored and an
// entry needs only a title.

type TrainDisruption struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"` // calamity | disruption | maintenance
	Title     string     `json:"title"`
	Situation string     `json:"situation,omitempty"`
	Cause     string     `json:"cause,omitempty"`
	Extra     string     `json:"extra_time,omitempty"`
	Expected  string     `json:"expected,omitempty"`
	Impact    int        `json:"impact,omitempty"` // 1–5
	Start     *time.Time `json:"start,omitempty"`
	End       *time.Time `json:"end,omitempty"`
}

type TrainData struct {
	Calamities  []TrainDisruption `json:"calamities"`
	Disruptions []TrainDisruption `json:"disruptions"`
	Maintenance []TrainDisruption `json:"maintenance"`
	MaintTotal  int               `json:"maintenance_total"`
}

func parseNSDisruptions(body []byte, now time.Time) (any, error) {
	var list []struct {
		ID                string                       `json:"id"`
		Type              string                       `json:"type"`
		Title             string                       `json:"title"`
		Description       string                       `json:"description"`
		IsActive          *bool                        `json:"isActive"`
		Start             string                       `json:"start"`
		End               string                       `json:"end"`
		Period            string                       `json:"period"` // maintenance: "Donderdag 1 februari 2024 4:00 uur t/m ..."
		ExpectedDuration  struct{ Description string } `json:"expectedDuration"`
		SummaryAdditional struct{ Label string }       `json:"summaryAdditionalTravelTime"`
		Impact            struct{ Value int }          `json:"impact"`
		Timespans         []struct {
			Situation            struct{ Label string } `json:"situation"`
			Cause                struct{ Label string } `json:"cause"`
			AdditionalTravelTime struct{ Label string } `json:"additionalTravelTime"`
		} `json:"timespans"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("ns: %w", err)
	}
	d := TrainData{Calamities: []TrainDisruption{}, Disruptions: []TrainDisruption{}, Maintenance: []TrainDisruption{}}
	for _, x := range list {
		if x.IsActive != nil && !*x.IsActive {
			continue
		}
		t := TrainDisruption{ID: truncate(plainText(x.ID), 60), Title: truncate(strings.TrimSuffix(plainText(x.Title), "."), 160), Impact: x.Impact.Value,
			Expected: truncate(plainText(x.ExpectedDuration.Description), 160), Extra: truncate(plainText(x.SummaryAdditional.Label), 80)}
		if t.Title == "" {
			continue
		}
		if len(x.Timespans) > 0 {
			ts := x.Timespans[0]
			t.Situation = truncate(plainText(ts.Situation.Label), 200)
			t.Cause = truncate(plainText(ts.Cause.Label), 120)
			if t.Extra == "" {
				t.Extra = truncate(plainText(ts.AdditionalTravelTime.Label), 80)
			}
		}
		if t.Situation == "" {
			t.Situation = truncate(plainText(x.Description), 200)
		}
		if t.Expected == "" {
			t.Expected = truncate(plainText(x.Period), 160)
		}
		// NS starts the situation with the cause ("Door een defect spoor: ..."): don't repeat it
		if t.Cause != "" && strings.Contains(strings.ToLower(t.Situation), strings.ToLower(t.Cause)) {
			t.Cause = ""
		}
		if s, ok := parseDate(x.Start); ok {
			s = s.UTC()
			t.Start = &s
		}
		if e, ok := parseDate(x.End); ok {
			e = e.UTC()
			t.End = &e
		}
		if t.Impact < 0 || t.Impact > 5 {
			t.Impact = 0
		}
		switch strings.ToUpper(x.Type) {
		case "CALAMITY":
			t.Type = "calamity"
			d.Calamities = append(d.Calamities, t)
		case "MAINTENANCE":
			t.Type = "maintenance"
			d.MaintTotal++
			if t.Start == nil || !t.Start.After(now) {
				d.Maintenance = append(d.Maintenance, t)
			}
		default:
			t.Type = "disruption"
			d.Disruptions = append(d.Disruptions, t)
		}
	}
	sort.SliceStable(d.Disruptions, func(i, j int) bool { return d.Disruptions[i].Impact > d.Disruptions[j].Impact })
	sort.SliceStable(d.Maintenance, func(i, j int) bool { return d.Maintenance[i].Impact > d.Maintenance[j].Impact })
	if len(d.Maintenance) > 5 {
		d.Maintenance = d.Maintenance[:5]
	}
	return d, nil
}

func (a *App) handleTrains(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Trains.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	if cfg.Keys.NSAPIKey == "" {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": true, "key": false})
		return
	}
	e := a.feedEntry("ns:disruptions")
	e["enabled"], e["key"] = true, true
	if v, ok := a.threats.get("ns:disruptions").Data.(TrainData); ok {
		e["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 60, e)
}

// ---------------------------------------------------------------------------
// Politiek vandaag: today's meetings of the Tweede Kamer (or the next day with
// meetings, up to a week ahead) and the latest votes, from the Tweede Kamer open
// data portal (OData, no key). Written deadlines and internal procedure meetings
// are left out.

type TKActivity struct {
	Number    string    `json:"number"`
	Kind      string    `json:"kind"`
	Subject   string    `json:"subject"`
	Committee string    `json:"committee,omitempty"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end,omitempty"`
	Cancelled bool      `json:"cancelled,omitempty"`
	URL       string    `json:"url"`
}

type TKVote struct {
	Result  string    `json:"result"` // aangenomen | verworpen
	Kind    string    `json:"kind"`   // Motie, Amendement, Wetsvoorstel, ...
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
	URL     string    `json:"url"`
}

type PoliticsData struct {
	Day        string       `json:"day"` // YYYY-MM-DD of the activities shown
	Activities []TKActivity `json:"activities"`
	Votes      []TKVote     `json:"votes"`
}

var (
	tkNumberRe = regexp.MustCompile(`^\d{4}[A-Z]\d{4,6}$`)
	tkSkip     = regexp.MustCompile(`^(Inbreng|E-mailprocedure|Procedurevergadering|Strategische procedurevergadering|Delegatievergadering|Constituerende vergadering|Werkbezoek|Gesprek|Vergadering)`)
)

func tkActivityURL(number, kind string) string {
	path := "commissievergaderingen/details?id="
	if strings.HasPrefix(kind, "Plenair") || kind == "Stemmingen" || kind == "Regeling van werkzaamheden" || kind == "Hamerstukken" || strings.HasPrefix(kind, "Vragenuur") {
		path = "plenaire_vergaderingen/details/activiteit?id="
	}
	return "https://www.tweedekamer.nl/debat_en_vergadering/" + path + url.QueryEscape(number)
}

func parseTKActivities(body []byte, now time.Time) (string, []TKActivity, error) {
	var r struct {
		Value []struct {
			Nummer, Soort, Onderwerp, Status, Voortouwafkorting string
			Aanvangstijd, Eindtijd                              *time.Time
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", nil, fmt.Errorf("tweedekamer: %w", err)
	}
	byDay := map[string][]TKActivity{}
	for _, v := range r.Value {
		if v.Aanvangstijd == nil || !tkNumberRe.MatchString(v.Nummer) || tkSkip.MatchString(v.Soort) {
			continue
		}
		st := v.Aanvangstijd.In(amsterdam)
		a := TKActivity{Number: v.Nummer, Kind: truncate(plainText(v.Soort), 60), Subject: truncate(plainText(strings.TrimSuffix(strings.TrimSpace(v.Onderwerp), "(geannuleerd)")), 160),
			Committee: truncate(plainText(v.Voortouwafkorting), 20), Start: st.UTC(), Cancelled: strings.EqualFold(v.Status, "Geannuleerd"), URL: tkActivityURL(v.Nummer, v.Soort)}
		if v.Eindtijd != nil {
			a.End = v.Eindtijd.UTC()
		}
		if a.Subject == "" {
			a.Subject = a.Kind
		}
		day := st.Format("2006-01-02")
		byDay[day] = append(byDay[day], a)
	}
	today := now.In(amsterdam).Format("2006-01-02")
	var days []string
	for d := range byDay {
		if d >= today {
			days = append(days, d)
		}
	}
	sort.Strings(days)
	for _, d := range days {
		list := byDay[d]
		active := 0
		for _, x := range list {
			if !x.Cancelled {
				active++
			}
		}
		if active == 0 && d != today {
			continue
		}
		if d == today && active == 0 && len(days) > 1 {
			continue // nothing left today: show the next day with meetings
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Start.Before(list[j].Start) })
		return d, list, nil
	}
	return today, []TKActivity{}, nil
}

func parseTKVotes(body []byte) ([]TKVote, error) {
	var r struct {
		Value []struct {
			BesluitSoort string
			GewijzigdOp  time.Time
			Zaak         []struct {
				Nummer, Soort, Onderwerp, Titel string
				Document                        []struct{ DocumentNummer string }
			}
			Agendapunt *struct {
				Activiteit *struct{ Datum *time.Time }
			}
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("tweedekamer: %w", err)
	}
	out := []TKVote{}
	for _, v := range r.Value {
		if len(v.Zaak) == 0 {
			continue
		}
		z := v.Zaak[0]
		res := "verworpen"
		if strings.HasSuffix(v.BesluitSoort, "aangenomen") {
			res = "aangenomen"
		}
		date := v.GewijzigdOp
		if v.Agendapunt != nil && v.Agendapunt.Activiteit != nil && v.Agendapunt.Activiteit.Datum != nil {
			date = *v.Agendapunt.Activiteit.Datum
		}
		link := "https://www.tweedekamer.nl/kamerstukken/detail?id=" + url.QueryEscape(z.Nummer)
		if len(z.Document) > 0 && z.Document[0].DocumentNummer != "" {
			link += "&did=" + url.QueryEscape(z.Document[0].DocumentNummer)
		}
		subj := firstNonEmpty(z.Onderwerp, z.Titel)
		if subj == "" || !tkNumberRe.MatchString(z.Nummer) {
			continue
		}
		out = append(out, TKVote{Result: res, Kind: truncate(plainText(z.Soort), 40), Subject: truncate(plainText(subj), 150), Date: date.UTC(), URL: link})
	}
	return out, nil
}

func (a *App) runPolitics(ctx context.Context) error {
	const key = "tk:politics"
	base := strings.TrimSuffix(a.config().Politics.Base, "/")
	now := time.Now().In(amsterdam)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, amsterdam)
	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("Verwijderd eq false and Datum ge %s and Datum lt %s", today.Format("2006-01-02"), today.AddDate(0, 0, 8).Format("2006-01-02")))
	q.Set("$orderby", "Aanvangstijd")
	q.Set("$top", "250")
	q.Set("$select", "Nummer,Soort,Onderwerp,Status,Voortouwafkorting,Aanvangstijd,Eindtijd")
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "/Activiteit?" + strings.ReplaceAll(q.Encode(), "+", "%20"), Accept: "application/json"})
	var d PoliticsData
	if err == nil {
		d.Day, d.Activities, err = parseTKActivities(resp.Body, now)
	}
	if err == nil {
		v := url.Values{}
		v.Set("$filter", "Verwijderd eq false and (BesluitSoort eq 'Stemmen - aangenomen' or BesluitSoort eq 'Stemmen - verworpen')")
		v.Set("$orderby", "GewijzigdOp desc")
		v.Set("$top", "6")
		v.Set("$select", "BesluitSoort,GewijzigdOp")
		v.Set("$expand", "Zaak($select=Nummer,Soort,Onderwerp,Titel;$expand=Document($select=DocumentNummer)),Agendapunt($select=Id;$expand=Activiteit($select=Datum))")
		if resp, err = a.fetcher.Do(ctx, FetchReq{URL: base + "/Besluit?" + strings.ReplaceAll(v.Encode(), "+", "%20"), Accept: "application/json"}); err == nil {
			d.Votes, err = parseTKVotes(resp.Body)
		}
	}
	if err != nil {
		err = fmt.Errorf("tweedekamer: %w", err)
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	a.threats.ok(key, d, "", "")
	return nil
}

func (a *App) handlePolitics(w http.ResponseWriter, r *http.Request) {
	if !a.config().Politics.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("tk:politics")
	e["enabled"] = true
	if v, ok := a.threats.get("tk:politics").Data.(PoliticsData); ok {
		e["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 60, e)
}

// ---------------------------------------------------------------------------
// Vandaag: date and ISO week, Dutch public holidays, school holidays per region
// (Rijksoverheid open data, fetched daily), moon phase and the next clock change.
// Holidays, moon and clock are calculated here; only school holidays are fetched.

type Holiday struct {
	Name string `json:"name"`
	Date string `json:"date"` // YYYY-MM-DD
}

// easter returns Easter Sunday (Gregorian calendar, anonymous algorithm).
func easter(year int) time.Time {
	a, b, c := year%19, year/100, year%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, amsterdam)
}

// dutchHolidays: the national public holidays of a year, in date order.
func dutchHolidays(year int) []Holiday {
	e := easter(year)
	d := func(t time.Time) string { return t.Format("2006-01-02") }
	day := func(m time.Month, dd int) time.Time { return time.Date(year, m, dd, 0, 0, 0, 0, amsterdam) }
	king := day(time.April, 27)
	if king.Weekday() == time.Sunday {
		king = day(time.April, 26)
	}
	list := []Holiday{
		{"Nieuwjaarsdag", d(day(time.January, 1))},
		{"Goede Vrijdag", d(e.AddDate(0, 0, -2))},
		{"Eerste Paasdag", d(e)},
		{"Tweede Paasdag", d(e.AddDate(0, 0, 1))},
		{"Koningsdag", d(king)},
		{"Bevrijdingsdag", d(day(time.May, 5))},
		{"Hemelvaartsdag", d(e.AddDate(0, 0, 39))},
		{"Eerste Pinksterdag", d(e.AddDate(0, 0, 49))},
		{"Tweede Pinksterdag", d(e.AddDate(0, 0, 50))},
		{"Eerste Kerstdag", d(day(time.December, 25))},
		{"Tweede Kerstdag", d(day(time.December, 26))},
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Date < list[j].Date })
	return list
}

// moonPhaseTime: the time of a new moon (phase 0) or full moon (phase 0.5) for lunation k
// (k = 0 around 6 January 2000), after Meeus, Astronomical Algorithms ch. 49 (main terms,
// accurate to a few minutes).
func moonPhaseTime(k float64, full bool) time.Time {
	if full {
		k += 0.5
	}
	t := k / 1236.85
	rad := math.Pi / 180
	jde := 2451550.09766 + 29.530588861*k + 0.00015437*t*t - 0.00000015*t*t*t
	m := (2.5534 + 29.10535670*k - 0.0000014*t*t) * rad
	mp := (201.5643 + 385.81693528*k + 0.0107582*t*t) * rad
	f := (160.7108 + 390.67050284*k - 0.0016118*t*t) * rad
	om := (124.7746 - 1.56375588*k + 0.0020672*t*t) * rad
	e := 1 - 0.002516*t - 0.0000074*t*t
	var c float64
	if full {
		c = -0.40614*math.Sin(mp) + 0.17302*e*math.Sin(m) + 0.01614*math.Sin(2*mp) + 0.01043*math.Sin(2*f) +
			0.00734*e*math.Sin(mp-m) - 0.00515*e*math.Sin(mp+m) + 0.00209*e*e*math.Sin(2*m) - 0.00111*math.Sin(mp-2*f) -
			0.00057*math.Sin(mp+2*f) + 0.00056*e*math.Sin(2*mp+m) - 0.00042*math.Sin(3*mp) + 0.00042*e*math.Sin(m+2*f) +
			0.00038*e*math.Sin(m-2*f) - 0.00024*e*math.Sin(2*mp-m) - 0.00017*math.Sin(om)
	} else {
		c = -0.40720*math.Sin(mp) + 0.17241*e*math.Sin(m) + 0.01608*math.Sin(2*mp) + 0.01039*math.Sin(2*f) +
			0.00739*e*math.Sin(mp-m) - 0.00514*e*math.Sin(mp+m) + 0.00208*e*e*math.Sin(2*m) - 0.00111*math.Sin(mp-2*f) -
			0.00057*math.Sin(mp+2*f) + 0.00056*e*math.Sin(2*mp+m) - 0.00042*math.Sin(3*mp) + 0.00042*e*math.Sin(m+2*f) +
			0.00038*e*math.Sin(m-2*f) - 0.00024*e*math.Sin(2*mp-m) - 0.00017*math.Sin(om)
	}
	jde += c
	// JDE (dynamical time) to UTC: ΔT is about 70 s in the 2020s, small enough to ignore here.
	return time.Unix(int64((jde-2440587.5)*86400), 0).UTC()
}

type MoonInfo struct {
	Phase        string     `json:"phase"` // new | waxing-crescent | first-quarter | waxing-gibbous | full | waning-gibbous | last-quarter | waning-crescent
	Illumination float64    `json:"illumination"`
	NextFull     time.Time  `json:"next_full"`
	NextNew      time.Time  `json:"next_new"`
	Moment       *time.Time `json:"moment,omitempty"` // exact new/full moon when within a day of now
}

func moonInfo(now time.Time) MoonInfo {
	const syn = 29.530588861
	k := math.Floor((float64(now.Unix())/86400 + 2440587.5 - 2451550.09766) / syn)
	prevNew := moonPhaseTime(k, false)
	for prevNew.After(now) {
		k--
		prevNew = moonPhaseTime(k, false)
	}
	nextNew := moonPhaseTime(k+1, false)
	for !nextNew.After(now) {
		k++
		prevNew, nextNew = nextNew, moonPhaseTime(k+1, false)
	}
	full := moonPhaseTime(k, true)
	nextFull := full
	if !full.After(now) {
		nextFull = moonPhaseTime(k+1, true)
	}
	age := now.Sub(prevNew).Hours() / 24
	frac := age / nextNew.Sub(prevNew).Hours() * 24
	names := []string{"new", "waxing-crescent", "first-quarter", "waxing-gibbous", "full", "waning-gibbous", "last-quarter", "waning-crescent"}
	idx := int(math.Floor(frac*8+0.5)) % 8
	// within ~a day of the exact moment, name the principal phase and give its time
	var moment *time.Time
	switch {
	case math.Abs(now.Sub(prevNew).Hours()) < 24:
		idx, moment = 0, &prevNew
	case math.Abs(nextNew.Sub(now).Hours()) < 24:
		idx, moment = 0, &nextNew
	case math.Abs(full.Sub(now).Hours()) < 24:
		idx, moment = 4, &full
	}
	return MoonInfo{Phase: names[idx], Illumination: math.Round((1-math.Cos(2*math.Pi*frac))/2*100) / 100, NextFull: nextFull, NextNew: nextNew, Moment: moment}
}

// nextClockChange: the next switch between summer and winter time within 60 days.
func nextClockChange(now time.Time) (time.Time, string, bool) {
	lastSunday := func(y int, m time.Month) time.Time {
		t := time.Date(y, m+1, 1, 0, 0, 0, 0, amsterdam).AddDate(0, 0, -1)
		for t.Weekday() != time.Sunday {
			t = t.AddDate(0, 0, -1)
		}
		return t
	}
	y := now.In(amsterdam).Year()
	for _, c := range []struct {
		t   time.Time
		dir string
	}{{lastSunday(y, time.March), "forward"}, {lastSunday(y, time.October), "back"}, {lastSunday(y+1, time.March), "forward"}} {
		switch at := c.t.Add(time.Duration(2+boolInt(c.dir == "back")) * time.Hour); {
		case at.After(now) && at.Sub(now) <= 60*24*time.Hour:
			return c.t, c.dir, true
		}
	}
	return time.Time{}, "", false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type SchoolHoliday struct {
	Type  string `json:"type"`
	Start string `json:"start"` // first day, YYYY-MM-DD (Amsterdam)
	End   string `json:"end"`   // last day
}

// parseSchoolHolidays reads the Rijksoverheid school-holiday feed into lists per region
// (noord, midden, zuid); "heel Nederland" entries are added to all three.
func parseSchoolHolidays(body []byte) (any, error) {
	var years []struct {
		Content []struct {
			Vacations []struct {
				Type    string `json:"type"`
				Regions []struct {
					Region    string    `json:"region"`
					StartDate time.Time `json:"startdate"`
					EndDate   time.Time `json:"enddate"`
				} `json:"regions"`
			} `json:"vacations"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &years); err != nil {
		return nil, fmt.Errorf("schoolvakanties: %w", err)
	}
	out := map[string][]SchoolHoliday{"noord": {}, "midden": {}, "zuid": {}}
	n := 0
	for _, y := range years {
		for _, c := range y.Content {
			for _, v := range c.Vacations {
				typ := truncate(plainText(v.Type), 60)
				for _, r := range v.Regions {
					if r.StartDate.IsZero() || r.EndDate.Before(r.StartDate) {
						continue
					}
					h := SchoolHoliday{Type: typ, Start: r.StartDate.In(amsterdam).Format("2006-01-02"), End: r.EndDate.In(amsterdam).Format("2006-01-02")}
					reg := strings.ToLower(strings.TrimSpace(r.Region))
					for _, name := range []string{"noord", "midden", "zuid"} {
						if reg == name || strings.Contains(reg, "heel nederland") {
							out[name] = append(out[name], h)
							n++
						}
					}
				}
			}
		}
	}
	if n == 0 {
		return nil, errors.New("schoolvakanties: no holidays in the feed")
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].Start < out[k][j].Start })
	}
	return out, nil
}

// schoolNow: per region the holiday going on today, else the next one.
func schoolNow(all map[string][]SchoolHoliday, today string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for reg, list := range all {
		for _, h := range list {
			if h.End < today {
				continue
			}
			out[reg] = map[string]any{"holiday": h, "current": h.Start <= today}
			break
		}
	}
	return out
}

func (a *App) handleToday(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Today.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	now := time.Now()
	local := now.In(amsterdam)
	today := local.Format("2006-01-02")
	_, week := local.ISOWeek()
	var todays, next []Holiday
	for _, y := range []int{local.Year(), local.Year() + 1} {
		for _, h := range dutchHolidays(y) {
			switch {
			case h.Date == today:
				todays = append(todays, h)
			case h.Date > today && len(next) < 2:
				next = append(next, h)
			}
		}
	}
	resp := map[string]any{"enabled": true, "date": today, "week": week, "holidays_today": todays, "holidays_next": next, "moon": moonInfo(now)}
	if d, dir, ok := nextClockChange(now); ok {
		resp["clock_change"] = map[string]string{"date": d.Format("2006-01-02"), "direction": dir}
	}
	school := a.feedEntry("rijk:schoolholidays")
	if v, ok := a.threats.get("rijk:schoolholidays").Data.(map[string][]SchoolHoliday); ok {
		school["regions"] = schoolNow(v, today)
	}
	resp["school"] = school
	if cfg.Today.OnThisDay {
		od := a.feedEntry("wiki:onthisday")
		if v, ok := a.threats.get("wiki:onthisday").Data.(OnThisDay); ok && v.Date == today {
			od["title"], od["url"], od["events"] = v.Title, v.URL, v.Events
		}
		resp["on_this_day"] = od
	}
	writeJSON(w, r, http.StatusOK, 300, resp)
}

// ---------------------------------------------------------------------------
// Ransomware NL: organisations claimed by ransomware groups on their leak sites, per
// country, from ransomware.live (free API v2: personal use, 1 request per minute per
// endpoint). Only name, website, sector, group and date are kept: descriptions can
// quote stolen data, and links to the groups' .onion sites are never passed on.

type RansomVictim struct {
	Name       string    `json:"name"`
	Website    string    `json:"website,omitempty"`
	Sector     string    `json:"sector,omitempty"`
	Group      string    `json:"group"`
	GroupURL   string    `json:"group_url"`
	Country    string    `json:"country"`
	Discovered time.Time `json:"discovered"`
}

type RansomData struct {
	Victims []RansomVictim `json:"victims"` // newest first, all of the last 12 months (trimmed in the handler)
	Total   int            `json:"total"`   // all claims ransomware.live lists for the country
}

var rwCountryRe = regexp.MustCompile(`^[A-Z]{2}$`)

func parseRansomware(body []byte, country string, now time.Time) (any, error) {
	if b := bytes.TrimSpace(body); len(b) > 0 && b[0] == '{' { // {"message": "1 per 1 minute"}: rate limit or error
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &m)
		return nil, fmt.Errorf("ransomware.live: %s", firstNonEmpty(truncate(plainText(m.Message), 80), "unexpected response"))
	}
	var list []struct {
		PostTitle  string `json:"post_title"`
		Website    string `json:"website"`
		Activity   string `json:"activity"`
		Group      string `json:"group_name"`
		Country    string `json:"country"`
		Discovered string `json:"discovered"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("ransomware.live: %w", err)
	}
	d := RansomData{Victims: []RansomVictim{}, Total: len(list)}
	cutoff := now.AddDate(-1, 0, 0)
	for _, v := range list {
		t, ok := parseDate(v.Discovered)
		if !ok || t.Before(cutoff) || t.After(now.Add(time.Hour)) {
			continue
		}
		name := truncate(plainText(v.PostTitle), 100)
		group := truncate(plainText(v.Group), 40)
		if name == "" || group == "" {
			continue
		}
		site := strings.ToLower(truncate(plainText(v.Website), 80))
		if strings.Contains(site, ".onion") || strings.ContainsAny(site, " /") {
			site = ""
		}
		d.Victims = append(d.Victims, RansomVictim{Name: name, Website: site, Sector: truncate(plainText(v.Activity), 40), Group: group,
			GroupURL: "https://www.ransomware.live/group/" + url.PathEscape(strings.ToLower(group)), Country: country, Discovered: t.UTC()})
	}
	sort.SliceStable(d.Victims, func(i, j int) bool { return d.Victims[i].Discovered.After(d.Victims[j].Discovered) })
	return d, nil
}

func (a *App) handleRansomware(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Ransomware.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	now := time.Now()
	var all []RansomVictim
	var sources []map[string]any
	for _, c := range cfg.Ransomware.Countries {
		key := "rw:" + c
		e := a.feedEntry(key)
		e["country"], e["url"] = c, "https://www.ransomware.live/country/"+c
		if v, ok := a.threats.get(key).Data.(RansomData); ok {
			all = append(all, v.Victims...)
			e["total"] = v.Total
		}
		sources = append(sources, e)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Discovered.After(all[j].Discovered) })
	count := func(days int) int {
		n, cut := 0, now.AddDate(0, 0, -days)
		for _, v := range all {
			if v.Discovered.After(cut) {
				n++
			}
		}
		return n
	}
	groups := map[string]int{}
	for _, v := range all {
		if v.Discovered.After(now.AddDate(0, 0, -90)) {
			groups[v.Group]++
		}
	}
	type gc struct {
		Name  string `json:"name"`
		URL   string `json:"url"`
		Count int    `json:"count"`
	}
	var top []gc
	for g, n := range groups {
		top = append(top, gc{g, "https://www.ransomware.live/group/" + url.PathEscape(strings.ToLower(g)), n})
	}
	sort.Slice(top, func(i, j int) bool {
		return top[i].Count > top[j].Count || (top[i].Count == top[j].Count && top[i].Name < top[j].Name)
	})
	if len(top) > 3 {
		top = top[:3]
	}
	last7, last30, last365 := count(7), count(30), count(365) // before the list is trimmed for display
	if len(all) > 8 {
		all = all[:8]
	}
	writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": true, "sources": sources, "victims": all,
		"last7": last7, "last30": last30, "last365": last365, "top_groups": top})
}

// ---------------------------------------------------------------------------
// Hooikoorts: pollen forecast (alder, birch, grass, mugwort, olive, ragweed) for a
// location, from the Open-Meteo Air Quality API (CAMS Europe). Daily maxima for the
// next three days, fetched on demand per ~10 km cell and cached for an hour.

var pollenTypes = []string{"alder", "birch", "grass", "mugwort", "olive", "ragweed"}

type PollenDay struct {
	Date string             `json:"date"`
	Max  map[string]float64 `json:"max"`          // grains/m³, highest hourly value of the day
	O3   *float64           `json:"o3,omitempty"` // ozone µg/m³, highest hourly value of the day (Hitte en smog)
}

type PollenData struct {
	Days []PollenDay        `json:"days"`
	Now  map[string]float64 `json:"now"`
}

func parsePollen(body []byte, now time.Time) (PollenData, error) {
	var r struct {
		Hourly map[string]json.RawMessage `json:"hourly"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return PollenData{}, fmt.Errorf("open-meteo pollen: %w", err)
	}
	var times []string
	if err := json.Unmarshal(r.Hourly["time"], &times); err != nil || len(times) == 0 {
		return PollenData{}, errors.New("open-meteo pollen: no hourly data")
	}
	d := PollenData{Now: map[string]float64{}}
	byDay := map[string]map[string]float64{}
	var order []string
	nowKey := now.In(amsterdam).Format("2006-01-02T15")
	for _, typ := range pollenTypes {
		var vals []*float64
		if json.Unmarshal(r.Hourly[typ+"_pollen"], &vals) != nil {
			continue
		}
		for i, v := range vals {
			if i >= len(times) || v == nil || *v < 0 || *v > 100000 || len(times[i]) < 13 {
				continue
			}
			day := times[i][:10]
			if byDay[day] == nil {
				byDay[day] = map[string]float64{}
				order = append(order, day)
			}
			if *v > byDay[day][typ] {
				byDay[day][typ] = math.Round(*v*10) / 10
			}
			if times[i][:13] == nowKey {
				d.Now[typ] = math.Round(*v*10) / 10
			}
		}
	}
	o3 := map[string]float64{}
	var ozone []*float64
	if json.Unmarshal(r.Hourly["ozone"], &ozone) == nil {
		for i, v := range ozone {
			if i < len(times) && v != nil && *v >= 0 && *v < 2000 && len(times[i]) >= 10 && *v > o3[times[i][:10]] {
				o3[times[i][:10]] = math.Round(*v)
			}
		}
	}
	sort.Strings(order)
	today := now.In(amsterdam).Format("2006-01-02")
	for _, day := range order {
		if day >= today && len(d.Days) < 3 {
			pd := PollenDay{Date: day, Max: byDay[day]}
			if v, ok := o3[day]; ok {
				pd.O3 = &v
			}
			d.Days = append(d.Days, pd)
		}
	}
	if len(d.Days) == 0 {
		return PollenData{}, errors.New("open-meteo pollen: no data for today")
	}
	return d, nil
}

func (a *App) handlePollen(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Pollen.Enabled {
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
	lat, lon = math.Round(lat*10)/10, math.Round(lon*10)/10 // CAMS cells are ~10 km: share the cache
	ip := a.clientIP(r)
	ctx := context.WithoutCancel(r.Context())
	d, at, stale, err := a.pollen.get(fmt.Sprintf("%.1f,%.1f", lat, lon), time.Hour, func() (PollenData, error) {
		if !a.wx.limiter.allow(ip) {
			return PollenData{}, errRateLimited
		}
		u := fmt.Sprintf("%s?latitude=%.1f&longitude=%.1f&hourly=%s&timezone=Europe%%2FAmsterdam&forecast_days=4", cfg.Pollen.URL, lat, lon,
			strings.Join(func() []string {
				var s []string
				for _, t := range pollenTypes {
					s = append(s, t+"_pollen")
				}
				if cfg.Air.HeatSmog {
					s = append(s, "ozone") // same request: the ozone forecast for Hitte en smog
				}
				return s
			}(), ","))
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
		if err != nil {
			return PollenData{}, fmt.Errorf("open-meteo pollen: %w", err)
		}
		return parsePollen(resp.Body, time.Now())
	})
	switch {
	case errors.Is(err, errRateLimited):
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
		return
	case err != nil:
		writeError(w, r, http.StatusBadGateway, "Open-Meteo is niet bereikbaar")
		return
	}
	writeJSON(w, r, http.StatusOK, 600, map[string]any{"enabled": true, "data": d, "fetched_at": at.UTC().Truncate(time.Second), "stale": stale,
		"location": map[string]float64{"lat": lat, "lon": lon}})
}

// ---------------------------------------------------------------------------
// Kritieke infrastructuur: electricity and gas outages of the grid operators that publish
// them: Liander (public ArcGIS feature service IStoringen, behind Liander's outage map)
// and Stedin (the JSON behind web.stedin.net/storingen, undocumented). Enexis, the small
// operators and the drinking-water companies publish no open outage data; the panel says so.

type UtilityOutage struct {
	ID        string     `json:"id"`
	Energy    string     `json:"energy"`  // elektriciteit | gas | overig
	Planned   bool       `json:"planned"` // planned maintenance
	Status    string     `json:"status,omitempty"`
	Reported  time.Time  `json:"reported"`
	Estimate  *time.Time `json:"estimate,omitempty"`
	EstText   string     `json:"estimate_text,omitempty"` // Stedin: "26-09-2026 tussen 13:15 en 13:45 uur"
	Cause     string     `json:"cause,omitempty"`
	Customers string     `json:"customers,omitempty"` // Liander bucket such as "< 25"
	Places    string     `json:"places,omitempty"`
	Postcodes int        `json:"postcodes,omitempty"`
}

type GridOperator struct {
	Name        string          `json:"name"`
	URL         string          `json:"url"`
	Area        string          `json:"area"`
	Active      []UtilityOutage `json:"active"`
	Planned     []UtilityOutage `json:"planned"`
	Resolved24h int             `json:"resolved_24h"` // -1 = unknown
	FetchedAt   *time.Time      `json:"fetched_at,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// titleNL: "AMSTERDAM" → "Amsterdam", "'S-GRAVENHAGE" → "'s-Gravenhage".
func titleNL(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		parts := strings.Split(w, "-")
		for j, p := range parts {
			if strings.HasPrefix(p, "'") || (j > 0 || i > 0) && (p == "aan" || p == "den" || p == "de" || p == "op" || p == "van" || p == "bij" || p == "in") {
				continue
			}
			if r := []rune(p); len(r) > 0 {
				parts[j] = strings.ToUpper(string(r[0])) + string(r[1:])
			}
		}
		words[i] = strings.Join(parts, "-")
	}
	return strings.Join(words, " ")
}

// parseLianderOutages reads the IStoringen feature service (Liander's outage map).
func parseLianderOutages(body []byte) ([]UtilityOutage, error) {
	var r struct {
		Features []struct {
			Attributes struct {
				Nummer    int    `json:"STORING_NUMMER"`
				Type      string `json:"STORING_TYPE"`
				Energie   string `json:"STORING_ENERGIESOORT"`
				Status    string `json:"STORING_STATUS"`
				Gemeld    *int64 `json:"STORING_DATUM_GEMELD"`
				Schatting *int64 `json:"STORING_DATUM_SCHATTING"`
				Oorzaak   string `json:"STORING_OORZAAK"`
				Klanten   string `json:"STORING_GETROFFEN_KLANTEN"`
				Plaatsen  string `json:"STORING_GETROFFEN_PLAATSEN"`
			} `json:"attributes"`
		} `json:"features"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("liander: %w", err)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("liander: %s", firstNonEmpty(truncate(plainText(r.Error.Message), 80), "error"))
	}
	if r.Features == nil {
		return nil, errors.New("liander: unexpected response")
	}
	out := []UtilityOutage{}
	for _, f := range r.Features {
		a := f.Attributes
		if a.Gemeld == nil || strings.EqualFold(a.Status, "opgelost") || a.Type == "dummy" {
			continue
		}
		energy := strings.ToLower(strings.TrimSpace(a.Energie))
		if energy != "elektriciteit" && energy != "gas" {
			energy = "overig"
		}
		o := UtilityOutage{ID: strconv.Itoa(a.Nummer), Energy: energy, Planned: a.Type == "P", Status: truncate(plainText(a.Status), 40),
			Reported: time.UnixMilli(*a.Gemeld).UTC(), Customers: truncate(plainText(a.Klanten), 20)}
		if a.Schatting != nil {
			t := time.UnixMilli(*a.Schatting).UTC()
			o.Estimate = &t
		}
		if c := plainText(a.Oorzaak); c != "" && !strings.EqualFold(c, "nog niet bekend") {
			o.Cause = truncate(c, 60)
		}
		places := strings.Split(plainText(a.Plaatsen), ",")
		for i := range places {
			places[i] = titleNL(strings.TrimSpace(places[i]))
		}
		o.Places = truncate(strings.Join(places, ", "), 100)
		out = append(out, o)
	}
	return out, nil
}

// parseStedinPlace reads the outages of one place from Stedin's outage API (the JSON behind
// web.stedin.net/storingen; undocumented).
func parseStedinPlace(body []byte, place string) ([]UtilityOutage, error) {
	var list []struct {
		ID             string   `json:"id"`
		IncidentType   string   `json:"incidentType"`
		Utility        string   `json:"utility"`
		StartTime      string   `json:"startTime"`
		TimeIndication string   `json:"timeIndication"`
		PostalCodes    []string `json:"postalCodes"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("stedin: %w", err)
	}
	out := []UtilityOutage{}
	for _, x := range list {
		t, ok := parseDate(x.StartTime)
		if !ok {
			continue
		}
		energy := map[string]string{"electricity": "elektriciteit", "gas": "gas"}[strings.ToLower(x.Utility)]
		if energy == "" {
			energy = "overig"
		}
		out = append(out, UtilityOutage{ID: truncate(plainText(x.ID), 40), Energy: energy, Planned: strings.EqualFold(x.IncidentType, "Maintenance"),
			Reported: t.UTC(), EstText: truncate(plainText(x.TimeIndication), 80), Places: truncate(plainText(place), 60), Postcodes: len(x.PostalCodes)})
	}
	return out, nil
}

func (a *App) fetchLiander(ctx context.Context, base string) (GridOperator, error) {
	g := GridOperator{Name: "Liander", URL: "https://www.liander.nl/storingen-en-onderhoud", Area: "Noord-Holland, Gelderland, Flevoland, delen van Friesland en Zuid-Holland",
		Resolved24h: -1}
	q := url.Values{}
	q.Set("where", "STORING_STATUS <> 'opgelost'")
	q.Set("outFields", "STORING_NUMMER,STORING_TYPE,STORING_ENERGIESOORT,STORING_STATUS,STORING_DATUM_GEMELD,STORING_DATUM_SCHATTING,STORING_OORZAAK,STORING_GETROFFEN_KLANTEN,STORING_GETROFFEN_PLAATSEN")
	q.Set("orderByFields", "STORING_DATUM_GEMELD DESC")
	q.Set("resultRecordCount", "500")
	q.Set("returnGeometry", "false")
	q.Set("f", "json")
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "/query?" + q.Encode(), Accept: "application/json"})
	if err != nil {
		return g, fmt.Errorf("liander: %w", err)
	}
	list, err := parseLianderOutages(resp.Body)
	if err != nil {
		return g, err
	}
	c := url.Values{}
	c.Set("where", fmt.Sprintf("STORING_STATUS = 'opgelost' AND STORING_TYPE <> 'P' AND STORING_DATUM_EIND >= TIMESTAMP '%s'", time.Now().UTC().Add(-24*time.Hour).Format("2006-01-02 15:04:05")))
	c.Set("returnCountOnly", "true")
	c.Set("f", "json")
	if r2, err2 := a.fetcher.Do(ctx, FetchReq{URL: base + "/query?" + c.Encode(), Accept: "application/json"}); err2 == nil {
		var n struct {
			Count *int `json:"count"`
		}
		if json.Unmarshal(r2.Body, &n) == nil && n.Count != nil {
			g.Resolved24h = *n.Count
		}
	}
	g.Active, g.Planned = splitPlanned(list)
	return g, nil
}

func (a *App) fetchStedin(ctx context.Context, base string) (GridOperator, error) {
	g := GridOperator{Name: "Stedin", URL: "https://web.stedin.net/storingen", Area: "Zuid-Holland, Utrecht en Zeeland (grotendeels)", Resolved24h: -1}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "?active=true", Accept: "application/json"})
	if err != nil {
		return g, fmt.Errorf("stedin: %w", err)
	}
	var ov struct {
		Total  *int `json:"total"`
		Places []struct {
			Place string `json:"place"`
			Count int    `json:"count"`
		} `json:"places"`
	}
	if err := json.Unmarshal(resp.Body, &ov); err != nil || ov.Total == nil {
		return g, errors.New("stedin: unexpected response")
	}
	var list []UtilityOutage
	for i, p := range ov.Places {
		if i == 25 || p.Place == "" { // one request per place; a storm day is summarised by the first 25
			break
		}
		r, err := a.fetcher.Do(ctx, FetchReq{URL: base + "?" + url.Values{"place": {p.Place}, "active": {"true"}}.Encode(), Accept: "application/json"})
		if err != nil {
			continue
		}
		if items, err := parseStedinPlace(r.Body, p.Place); err == nil {
			list = append(list, items...)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Reported.After(list[j].Reported) })
	g.Active, g.Planned = splitPlanned(list)
	return g, nil
}

func splitPlanned(list []UtilityOutage) (active, planned []UtilityOutage) {
	active, planned = []UtilityOutage{}, []UtilityOutage{}
	for _, o := range list {
		if o.Planned {
			planned = append(planned, o)
		} else {
			active = append(active, o)
		}
	}
	return
}

func (a *App) runUtilities(ctx context.Context) error {
	const key = "grid:outages"
	cfg := a.config().Utilities
	prev, _ := a.threats.get(key).Data.([]GridOperator)
	var out []GridOperator
	okN := 0
	for i, f := range []func() (GridOperator, error){
		func() (GridOperator, error) { return a.fetchLiander(ctx, strings.TrimSuffix(cfg.LianderURL, "/")) },
		func() (GridOperator, error) { return a.fetchStedin(ctx, strings.TrimSuffix(cfg.StedinURL, "/")) },
	} {
		g, err := f()
		now := time.Now().UTC().Truncate(time.Second)
		if err != nil {
			if i < len(prev) && prev[i].FetchedAt != nil { // keep the last good data, with the error
				g = prev[i]
			}
			g.Error = truncate(err.Error(), 120)
			slog.Warn("fetch failed", "source", key, "operator", g.Name, "err", err)
		} else {
			g.FetchedAt = &now
			okN++
		}
		out = append(out, g)
	}
	if okN == 0 {
		err := errors.New("netbeheerders: no operator reachable")
		a.threats.fail(key, err)
		if prev == nil {
			return err
		}
	}
	a.threats.ok(key, out, "", "")
	return nil
}

func (a *App) handleUtilities(w http.ResponseWriter, r *http.Request) {
	if !a.config().Utilities.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("grid:outages")
	e["enabled"] = true
	if v, ok := a.threats.get("grid:outages").Data.([]GridOperator); ok {
		e["operators"] = v
	}
	writeJSON(w, r, http.StatusOK, 60, e)
}

// ---------------------------------------------------------------------------
// Aardbevingen: earthquakes in and around the Netherlands from KNMI's FDSN event
// service (open data). Explosions, quarry blasts and sonic booms are left out;
// "induced or triggered" events (gas extraction, Groningen) are kept and marked.

type Quake struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	Place   string    `json:"place"`
	Mag     *float64  `json:"mag,omitempty"`
	MagType string    `json:"mag_type,omitempty"`
	Depth   float64   `json:"depth_km"`
	Induced bool      `json:"induced,omitempty"`
	URL     string    `json:"url"`
}

var quakeIDRe = regexp.MustCompile(`^[a-z]{2,8}\d{4}[a-z]{2,8}$`)

func parseKNMIQuakes(body []byte) ([]Quake, error) {
	out := []Quake{}
	lines := strings.Split(string(body), "\n")
	header := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EventID|") {
			header = true
			continue
		}
		f := strings.Split(line, "|")
		if line == "" || len(f) < 14 {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(f[13]))
		if typ != "earthquake" && typ != "induced or triggered event" {
			continue
		}
		t, err := time.Parse("2006-01-02T15:04:05.999999", strings.TrimSpace(f[1]))
		if err != nil || !quakeIDRe.MatchString(f[0]) {
			continue
		}
		q := Quake{ID: f[0], Time: t.UTC(), Place: truncate(plainText(f[12]), 60), MagType: truncate(plainText(f[9]), 6), Induced: typ != "earthquake",
			URL: "https://www.knmi.nl/nederland-nu/seismologie/aardbevingen/" + url.PathEscape(f[0])}
		if d, err := strconv.ParseFloat(f[4], 64); err == nil && d >= 0 && d < 700 {
			q.Depth = math.Round(d*10) / 10
		}
		if m, err := strconv.ParseFloat(f[10], 64); err == nil && m > -3 && m < 10 {
			m = math.Round(m*10) / 10
			q.Mag = &m
		}
		out = append(out, q)
	}
	if !header {
		return nil, errors.New("knmi: not an FDSN text response")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

func (a *App) runQuakes(ctx context.Context) error {
	const key = "knmi:quakes"
	cfg := a.config().Quakes
	q := url.Values{}
	q.Set("starttime", time.Now().UTC().AddDate(0, 0, -cfg.Days).Format("2006-01-02"))
	q.Set("minlatitude", "50.6")
	q.Set("maxlatitude", "53.8")
	q.Set("minlongitude", "3.2")
	q.Set("maxlongitude", "7.3")
	q.Set("format", "text")
	q.Set("orderby", "time")
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: cfg.URL + "?" + q.Encode(), Accept: "text/plain"})
	if resp != nil && resp.Status == http.StatusNoContent { // FDSN: no events in the window
		a.threats.ok(key, []Quake{}, "", "")
		return nil
	}
	var list []Quake
	if err == nil {
		list, err = parseKNMIQuakes(resp.Body)
	}
	if err != nil {
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	if len(list) > 200 {
		list = list[:200]
	}
	a.threats.ok(key, list, "", "")
	return nil
}

func (a *App) handleQuakes(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Quakes.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("knmi:quakes")
	e["enabled"], e["days"] = true, cfg.Quakes.Days
	if v, ok := a.threats.get("knmi:quakes").Data.([]Quake); ok {
		e["quakes"] = v
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}

// ---------------------------------------------------------------------------
// Internet in Nederland (part of the Storingen panel): outage events detected by
// IODA (Georgia Tech) for the country and a few large Dutch networks, last 7 days.
// Data: Copyright Georgia Tech Research Corporation; no key; attribution shown.

type InternetEvent struct {
	Start      time.Time `json:"start"`
	Duration   int       `json:"duration_s"`
	Datasource string    `json:"datasource"` // bgp | ping-slash24 | merit-nt | gtr ...
	Score      float64   `json:"score"`
}

type InternetEntity struct {
	Name   string          `json:"name"`
	Kind   string          `json:"kind"` // country | asn
	Code   string          `json:"code"`
	Events []InternetEvent `json:"events"`
	Error  string          `json:"error,omitempty"`
}

func parseIODAEvents(body []byte) ([]InternetEvent, error) {
	var r struct {
		Data []struct {
			Start      int64   `json:"start"`
			Duration   int     `json:"duration"`
			Datasource string  `json:"datasource"`
			Score      float64 `json:"score"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("ioda: %w", err)
	}
	if r.Error != nil || r.Data == nil {
		return nil, fmt.Errorf("ioda: %v", firstNonEmpty(fmt.Sprint(r.Error), "unexpected response"))
	}
	out := []InternetEvent{}
	for _, e := range r.Data {
		if e.Start <= 0 || e.Duration < 0 || e.Duration > 30*86400 {
			continue
		}
		out = append(out, InternetEvent{Start: time.Unix(e.Start, 0).UTC(), Duration: e.Duration, Datasource: truncate(plainText(e.Datasource), 20), Score: math.Round(e.Score)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out, nil
}

func (a *App) runInternet(ctx context.Context) error {
	const key = "ioda:internet"
	cfg := a.config().Outages.Internet
	now := time.Now()
	type ent struct{ kind, code, name string }
	ents := []ent{{"country", cfg.Country, "Nederland"}}
	if cfg.Country != "NL" {
		ents[0].name = cfg.Country
	}
	for _, n := range cfg.Networks {
		ents = append(ents, ent{"asn", strconv.Itoa(n.ASN), n.Name})
	}
	var out []InternetEntity
	ok := 0
	for _, e := range ents {
		u := fmt.Sprintf("%s/outages/events?entityType=%s&entityCode=%s&from=%d&until=%d", strings.TrimSuffix(cfg.Base, "/"), e.kind, url.QueryEscape(e.code),
			now.Add(-7*24*time.Hour).Unix(), now.Unix())
		ie := InternetEntity{Name: e.name, Kind: e.kind, Code: e.code, Events: []InternetEvent{}}
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
		if err == nil {
			ie.Events, err = parseIODAEvents(resp.Body)
		}
		if err != nil {
			ie.Error = truncate(err.Error(), 120)
			ie.Events = []InternetEvent{}
		} else {
			ok++
		}
		out = append(out, ie)
	}
	if ok == 0 {
		err := fmt.Errorf("ioda: no network reachable (%s)", out[0].Error)
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	a.threats.ok(key, out, "", "")
	return nil
}

// ---------------------------------------------------------------------------
// Economie in cijfers: Dutch inflation (HICP, with the euro area for comparison) and
// unemployment from Eurostat, the ECB deposit facility rate and the EUR/USD reference
// rate from the ECB Data Portal. All open data, no key.

type EconPoint struct {
	Period string  `json:"period"` // 2026-08, or a date for daily series
	Value  float64 `json:"value"`
}

type EconomyData struct {
	Inflation    []EconPoint `json:"inflation"`              // NL, annual rate of change, last 13 months
	InflationEA  *EconPoint  `json:"inflation_ea,omitempty"` // euro area, latest month
	Unemployment []EconPoint `json:"unemployment"`           // NL, seasonally adjusted, last 13 months
	Rate         *EconPoint  `json:"rate,omitempty"`         // ECB deposit facility rate
	RateSince    string      `json:"rate_since,omitempty"`   // date of the last change
	EURUSD       []EconPoint `json:"eurusd"`                 // last two reference rates
	Errors       []string    `json:"errors,omitempty"`
}

// parseEurostat reads a single-series JSON-stat response (all dimensions but time fixed).
func parseEurostat(body []byte) ([]EconPoint, error) {
	var r struct {
		Value     map[string]float64 `json:"value"`
		Dimension struct {
			Time struct {
				Category struct {
					Index map[string]int `json:"index"`
				} `json:"category"`
			} `json:"time"`
		} `json:"dimension"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("eurostat: %w", err)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("eurostat: %s", truncate(fmt.Sprint(r.Error), 100))
	}
	var out []EconPoint
	for period, i := range r.Dimension.Time.Category.Index {
		if v, ok := r.Value[strconv.Itoa(i)]; ok && v > -100 && v < 1000 {
			out = append(out, EconPoint{Period: period, Value: v})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("eurostat: no values")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Period < out[j].Period })
	return out, nil
}

// parseECBCSV reads TIME_PERIOD and OBS_VALUE from an ECB Data Portal CSV response.
func parseECBCSV(body []byte) ([]EconPoint, error) {
	rows, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil || len(rows) < 2 {
		return nil, fmt.Errorf("ecb: not a CSV response")
	}
	ti, vi := -1, -1
	for i, h := range rows[0] {
		switch h {
		case "TIME_PERIOD":
			ti = i
		case "OBS_VALUE":
			vi = i
		}
	}
	if ti < 0 || vi < 0 {
		return nil, errors.New("ecb: TIME_PERIOD/OBS_VALUE missing")
	}
	var out []EconPoint
	for _, r := range rows[1:] {
		if len(r) <= ti || len(r) <= vi {
			continue
		}
		if v, err := strconv.ParseFloat(r[vi], 64); err == nil {
			out = append(out, EconPoint{Period: r[ti], Value: v})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("ecb: no values")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Period < out[j].Period })
	return out, nil
}

func (a *App) runEconomy(ctx context.Context) error {
	const key = "econ:figures"
	cfg := a.config().Economy
	get := func(u, accept string) ([]byte, error) {
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: accept})
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
	es := strings.TrimSuffix(cfg.EurostatBase, "/")
	ecb := strings.TrimSuffix(cfg.ECBBase, "/")
	d := EconomyData{Inflation: []EconPoint{}, Unemployment: []EconPoint{}, EURUSD: []EconPoint{}}
	fail := func(what string, err error) { d.Errors = append(d.Errors, what+": "+truncate(err.Error(), 80)) }
	if b, err := get(es+"/prc_hicp_minr?geo=NL&coicop18=TOTAL&unit=RCH_A&lastTimePeriod=13", "application/json"); err != nil {
		fail("inflatie", err)
	} else if p, err := parseEurostat(b); err != nil {
		fail("inflatie", err)
	} else {
		d.Inflation = p
	}
	if b, err := get(es+"/prc_hicp_minr?geo=EA&coicop18=TOTAL&unit=RCH_A&lastTimePeriod=1", "application/json"); err == nil {
		if p, err := parseEurostat(b); err == nil {
			d.InflationEA = &p[len(p)-1]
		}
	}
	if b, err := get(es+"/une_rt_m?geo=NL&s_adj=SA&age=TOTAL&sex=T&unit=PC_ACT&lastTimePeriod=13", "application/json"); err != nil {
		fail("werkloosheid", err)
	} else if p, err := parseEurostat(b); err != nil {
		fail("werkloosheid", err)
	} else {
		d.Unemployment = p
	}
	start := time.Now().AddDate(-4, 0, 0).Format("2006-01-02")
	if b, err := get(ecb+"/FM/D.U2.EUR.4F.KR.DFR.LEV?format=csvdata&startPeriod="+start, "text/csv"); err != nil {
		fail("rente", err)
	} else if p, err := parseECBCSV(b); err != nil {
		fail("rente", err)
	} else {
		last := p[len(p)-1]
		d.Rate = &last
		d.RateSince = p[0].Period
		for i := len(p) - 1; i > 0; i-- {
			if p[i-1].Value != last.Value {
				d.RateSince = p[i].Period
				break
			}
		}
	}
	if b, err := get(ecb+"/EXR/D.USD.EUR.SP00.A?format=csvdata&lastNObservations=2", "text/csv"); err != nil {
		fail("wisselkoers", err)
	} else if p, err := parseECBCSV(b); err != nil {
		fail("wisselkoers", err)
	} else {
		d.EURUSD = p
	}
	if len(d.Inflation) == 0 && len(d.Unemployment) == 0 && d.Rate == nil && len(d.EURUSD) == 0 {
		err := fmt.Errorf("economie: no source reachable (%s)", strings.Join(d.Errors, "; "))
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	a.threats.ok(key, d, "", "")
	return nil
}

func (a *App) handleEconomy(w http.ResponseWriter, r *http.Request) {
	if !a.config().Economy.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("econ:figures")
	e["enabled"] = true
	if v, ok := a.threats.get("econ:figures").Data.(EconomyData); ok {
		e["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}

// ---------------------------------------------------------------------------
// Beurs: indices and the AEX stocks' daily change, for the top 3 risers and fallers.
// Yahoo Finance's unofficial "spark" endpoint: no key, one request per 10 symbols;
// undocumented and for personal use (the panel says so). Prices are delayed.

type MarketSymbol struct {
	Symbol string `yaml:"symbol" json:"symbol"`
	Name   string `yaml:"name" json:"name"`
}

type Quote struct {
	Symbol    string    `json:"symbol"`
	Name      string    `json:"name"`
	Price     float64   `json:"price"`
	ChangePct float64   `json:"change_pct"`
	PrevClose float64   `json:"prev_close,omitempty"`
	Time      time.Time `json:"time,omitempty"`
}

type MarketData struct {
	Indices []Quote `json:"indices"`
	Stocks  []Quote `json:"stocks"` // sorted by change, highest first
}

var marketSymbolRe = regexp.MustCompile(`^\^?[A-Z0-9][A-Z0-9.=-]{0,19}$`)

func parseSpark(body []byte, names map[string]string) ([]Quote, error) {
	var r map[string]struct {
		Symbol               string    `json:"symbol"`
		Timestamp            []int64   `json:"timestamp"`
		Close                []float64 `json:"close"`
		ChartPreviousClose   *float64  `json:"chartPreviousClose"`
		FulldayPrice         *float64  `json:"fulldayPrice"`
		FulldayChangePercent *float64  `json:"fulldayChangePercent"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("yahoo: %w", err)
	}
	var out []Quote
	for sym, v := range r {
		name, ok := names[sym]
		if !ok {
			continue
		}
		q := Quote{Symbol: sym, Name: name}
		switch {
		case v.FulldayPrice != nil:
			q.Price = *v.FulldayPrice
		case len(v.Close) > 0:
			q.Price = v.Close[len(v.Close)-1]
		default:
			continue
		}
		if v.ChartPreviousClose != nil && *v.ChartPreviousClose > 0 {
			q.PrevClose = *v.ChartPreviousClose
		}
		switch {
		case v.FulldayChangePercent != nil:
			q.ChangePct = *v.FulldayChangePercent
		case q.PrevClose > 0:
			q.ChangePct = (q.Price - q.PrevClose) / q.PrevClose * 100
		}
		if q.Price <= 0 || math.IsNaN(q.Price) || math.Abs(q.ChangePct) > 90 {
			continue
		}
		q.Price, q.ChangePct = math.Round(q.Price*1e4)/1e4, math.Round(q.ChangePct*100)/100
		if n := len(v.Timestamp); n > 0 && v.Timestamp[n-1] > 0 {
			q.Time = time.Unix(v.Timestamp[n-1], 0).UTC()
		}
		out = append(out, q)
	}
	return out, nil
}

func (a *App) runMarkets(ctx context.Context) error {
	const key = "yahoo:markets"
	cfg := a.config().Markets
	fetch := func(list []MarketSymbol) ([]Quote, error) {
		names := map[string]string{}
		var all []Quote
		var lastErr error
		for i := 0; i < len(list); i += 10 {
			batch := list[i:min(i+10, len(list))]
			var syms []string
			for _, s := range batch {
				names[s.Symbol] = s.Name
				syms = append(syms, s.Symbol)
			}
			u := cfg.URL + "?" + url.Values{"symbols": {strings.Join(syms, ",")}, "range": {"1d"}, "interval": {"5m"}}.Encode()
			resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
			if err != nil {
				lastErr = err
				continue
			}
			q, err := parseSpark(resp.Body, names)
			if err != nil {
				lastErr = err
				continue
			}
			all = append(all, q...)
		}
		if len(all) == 0 && lastErr != nil {
			return nil, lastErr
		}
		return all, nil
	}
	idx, err := fetch(cfg.Indices)
	var stocks []Quote
	if err == nil {
		stocks, err = fetch(cfg.Stocks)
	}
	if err != nil {
		err = fmt.Errorf("yahoo: %w", err)
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	order := map[string]int{}
	for i, s := range cfg.Indices {
		order[s.Symbol] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return order[idx[i].Symbol] < order[idx[j].Symbol] })
	sort.SliceStable(stocks, func(i, j int) bool { return stocks[i].ChangePct > stocks[j].ChangePct })
	a.threats.ok(key, MarketData{Indices: idx, Stocks: stocks}, "", "")
	return nil
}

func (a *App) handleMarkets(w http.ResponseWriter, r *http.Request) {
	if !a.config().Markets.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("yahoo:markets")
	e["enabled"] = true
	if v, ok := a.threats.get("yahoo:markets").Data.(MarketData); ok {
		e["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 60, e)
}
