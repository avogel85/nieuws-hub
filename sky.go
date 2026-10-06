package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"
)

// ---------------------------------------------------------------------------
// Vanavond aan de hemel: when it gets dark, the moon, the planets you can see
// tonight, the chance of northern lights and the clouds. Positions are computed
// here with low-precision formulas (a few tenths of a degree, plenty to say
// "Jupiter, southeast, from 22:10"): the sun and moon after the Astronomical
// Almanac / Meeus, the planets from JPL's Keplerian elements (valid 1800-2050).
// Northern lights: NOAA SWPC's planetary Kp forecast (public domain). Clouds:
// Open-Meteo. Meteor showers: the IMO calendar of the major showers.

const deg = math.Pi / 180

func julianDay(t time.Time) float64 { return float64(t.UnixNano())/86400e9 + 2440587.5 }

func norm360(x float64) float64 {
	x = math.Mod(x, 360)
	if x < 0 {
		x += 360
	}
	return x
}

// eclToEq converts ecliptic longitude/latitude (degrees) to right ascension/declination (degrees).
func eclToEq(lon, lat, eps float64) (ra, dec float64) {
	l, b, e := lon*deg, lat*deg, eps*deg
	ra = math.Atan2(math.Sin(l)*math.Cos(e)-math.Tan(b)*math.Sin(e), math.Cos(l)) / deg
	dec = math.Asin(math.Sin(b)*math.Cos(e)+math.Cos(b)*math.Sin(e)*math.Sin(l)) / deg
	return norm360(ra), dec
}

// altAz gives altitude and azimuth (degrees, azimuth from north through east).
func altAz(t time.Time, ra, dec, lat, lon float64) (alt, az float64) {
	d := julianDay(t) - 2451545.0
	gmst := norm360(280.46061837 + 360.98564736629*d)
	ha := (gmst + lon - ra) * deg
	φ, δ := lat*deg, dec*deg
	alt = math.Asin(math.Sin(φ)*math.Sin(δ)+math.Cos(φ)*math.Cos(δ)*math.Cos(ha)) / deg
	az = norm360(math.Atan2(-math.Sin(ha), math.Tan(δ)*math.Cos(φ)-math.Sin(φ)*math.Cos(ha)) / deg)
	return
}

func sunRADec(t time.Time) (ra, dec float64) {
	n := julianDay(t) - 2451545.0
	L := norm360(280.460 + 0.9856474*n)
	g := norm360(357.528+0.9856003*n) * deg
	lambda := L + 1.915*math.Sin(g) + 0.020*math.Sin(2*g)
	return eclToEq(lambda, 0, 23.439-0.0000004*n)
}

// moonRADec: low-precision lunar position (~0.3°), plus the horizontal parallax.
func moonRADec(t time.Time) (ra, dec, parallax float64) {
	T := (julianDay(t) - 2451545.0) / 36525
	s := func(a, b float64) float64 { return math.Sin((a + b*T) * deg) }
	c := func(a, b float64) float64 { return math.Cos((a + b*T) * deg) }
	lambda := 218.32 + 481267.881*T + 6.29*s(135.0, 477198.87) - 1.27*s(259.3, -413335.36) + 0.66*s(235.7, 890534.22) +
		0.21*s(269.9, 954397.74) - 0.19*s(357.5, 35999.05) - 0.11*s(186.5, 966404.03)
	beta := 5.13*s(93.3, 483202.02) + 0.28*s(228.2, 960400.89) - 0.28*s(318.3, 6003.15) - 0.17*s(217.6, -407332.21)
	parallax = 0.9508 + 0.0518*c(135.0, 477198.87) + 0.0095*c(259.3, -413335.36) + 0.0078*c(235.7, 890534.22) + 0.0028*c(269.9, 954397.74)
	ra, dec = eclToEq(norm360(lambda), beta, 23.439-0.0130*T)
	return
}

// planetElements: JPL "Approximate Positions of the Planets", Table 1 (J2000,
// 1800-2050): a, e, I, L, long. perihelion, long. ascending node, then rates per century.
var planetElements = map[string][12]float64{
	"earth":   {1.00000261, 0.01671123, -0.00001531, 100.46457166, 102.93768193, 0.0, 0.00000562, -0.00004392, -0.01294668, 35999.37244981, 0.32327364, 0.0},
	"mercury": {0.38709927, 0.20563593, 7.00497902, 252.25032350, 77.45779628, 48.33076593, 0.00000037, 0.00001906, -0.00594749, 149472.67411175, 0.16047689, -0.12534081},
	"venus":   {0.72333566, 0.00677672, 3.39467605, 181.97909950, 131.60246718, 76.67984255, 0.00000390, -0.00004107, -0.00078890, 58517.81538729, 0.00268329, -0.27769418},
	"mars":    {1.52371034, 0.09339410, 1.84969142, -4.55343205, -23.94362959, 49.55953891, 0.00001847, 0.00007882, -0.00813131, 19140.30268499, 0.44441088, -0.29257343},
	"jupiter": {5.20288700, 0.04838624, 1.30439695, 34.39644051, 14.72847983, 100.47390909, -0.00011607, -0.00013253, -0.00183714, 3034.74612775, 0.21252668, 0.20469106},
	"saturn":  {9.53667594, 0.05386179, 2.48599187, 49.95424423, 92.59887831, 113.66242448, -0.00125060, -0.00050991, 0.00193609, 1222.49362201, -0.41897216, -0.28867794},
}

// helio returns heliocentric ecliptic J2000 coordinates (AU).
func helio(name string, T float64) (x, y, z float64) {
	e := planetElements[name]
	a, ec, I := e[0]+e[6]*T, e[1]+e[7]*T, (e[2]+e[8]*T)*deg
	L, peri, node := e[3]+e[9]*T, e[4]+e[10]*T, e[5]+e[11]*T
	M := norm360(L-peri) * deg
	w, O := (peri-node)*deg, node*deg
	E := M
	for i := 0; i < 8; i++ {
		E -= (E - ec*math.Sin(E) - M) / (1 - ec*math.Cos(E))
	}
	xp, yp := a*(math.Cos(E)-ec), a*math.Sqrt(1-ec*ec)*math.Sin(E)
	x = (math.Cos(w)*math.Cos(O)-math.Sin(w)*math.Sin(O)*math.Cos(I))*xp + (-math.Sin(w)*math.Cos(O)-math.Cos(w)*math.Sin(O)*math.Cos(I))*yp
	y = (math.Cos(w)*math.Sin(O)+math.Sin(w)*math.Cos(O)*math.Cos(I))*xp + (-math.Sin(w)*math.Sin(O)+math.Cos(w)*math.Cos(O)*math.Cos(I))*yp
	z = math.Sin(w)*math.Sin(I)*xp + math.Cos(w)*math.Sin(I)*yp
	return
}

func planetRADec(name string, t time.Time) (ra, dec float64) {
	T := (julianDay(t) - 2451545.0) / 36525
	px, py, pz := helio(name, T)
	ex, ey, ez := helio("earth", T)
	x, y, z := px-ex, py-ey, pz-ez
	lon := math.Atan2(y, x) / deg
	lat := math.Atan2(z, math.Hypot(x, y)) / deg
	return eclToEq(norm360(lon), lat, 23.43928)
}

// ---------------------------------------------------------------------------

type SkyPlanet struct {
	Name  string    `json:"name"`
	From  time.Time `json:"from"`
	Until time.Time `json:"until"`
	Best  time.Time `json:"best"`
	Alt   int       `json:"alt"` // degrees at Best
	Dir   string    `json:"dir"` // compass direction at Best (N, NO, ...)
}

type SkyMoon struct {
	Phase        string     `json:"phase"`
	Illumination float64    `json:"illumination"`
	Rise         *time.Time `json:"rise,omitempty"`
	Set          *time.Time `json:"set,omitempty"`
}

type SkyMeteor struct {
	Name string `json:"name"`
	Peak string `json:"peak"` // YYYY-MM-DD
	ZHR  int    `json:"zhr"`
}

type SkyData struct {
	Date    string      `json:"date"`
	Sunset  *time.Time  `json:"sunset,omitempty"`
	Dark    *time.Time  `json:"dark,omitempty"`    // sun 12° below the horizon (nautical dusk): stars and planets
	Dawn    *time.Time  `json:"dawn,omitempty"`    // nautical dawn
	Sunrise *time.Time  `json:"sunrise,omitempty"` // tomorrow
	Moon    SkyMoon     `json:"moon"`
	Planets []SkyPlanet `json:"planets"`
	Kp      *float64    `json:"kp,omitempty"`     // highest predicted Kp tonight
	Aurora  int         `json:"aurora"`           // 0 none, 1 camera only, 2 small chance, 3 good chance
	Clouds  *int        `json:"clouds,omitempty"` // average cloud cover tonight, %
	Meteor  *SkyMeteor  `json:"meteor,omitempty"`
}

var compass8 = []string{"N", "NO", "O", "ZO", "Z", "ZW", "W", "NW"}

func compass(az float64) string { return compass8[int(math.Round(norm360(az)/45))%8] }

// crossing finds the first time in [from, to) when f crosses level upward (up=true) or downward.
func crossing(from, to time.Time, step time.Duration, f func(time.Time) float64, level float64, up bool) *time.Time {
	prev := f(from)
	for t := from.Add(step); !t.After(to); t = t.Add(step) {
		v := f(t)
		if (up && prev < level && v >= level) || (!up && prev >= level && v < level) {
			lo, hi := t.Add(-step), t // refine to the minute
			for hi.Sub(lo) > time.Minute {
				mid := lo.Add(hi.Sub(lo) / 2)
				if (f(mid) >= level) == up {
					hi = mid
				} else {
					lo = mid
				}
			}
			r := hi.Truncate(time.Minute)
			return &r
		}
		prev = v
	}
	return nil
}

// meteorShowers: the major annual showers (IMO), peak month/day and ZHR.
var meteorShowers = []struct {
	Name       string
	Month, Day int
	ZHR        int
}{
	{"Quadrantiden", 1, 3, 80}, {"Lyriden", 4, 22, 18}, {"Eta Aquariiden", 5, 6, 50}, {"Perseïden", 8, 12, 100},
	{"Draconiden", 10, 8, 10}, {"Orioniden", 10, 21, 20}, {"Leoniden", 11, 17, 15}, {"Geminiden", 12, 14, 150}, {"Ursiden", 12, 22, 10},
}

// computeSky works out tonight for a place: from noon today to noon tomorrow (local time).
func computeSky(lat, lon float64, now time.Time) SkyData {
	loc := amsterdam
	local := now.In(loc)
	noon := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, loc)
	if local.Hour() < 6 { // after midnight: still "tonight" of the previous day
		noon = noon.AddDate(0, 0, -1)
	}
	end := noon.Add(24 * time.Hour)
	d := SkyData{Date: noon.Format("2006-01-02"), Planets: []SkyPlanet{}}
	sunAlt := func(t time.Time) float64 { ra, dec := sunRADec(t); a, _ := altAz(t, ra, dec, lat, lon); return a }
	d.Sunset = crossing(noon, end, 10*time.Minute, sunAlt, -0.833, false)
	d.Dark = crossing(noon, end, 10*time.Minute, sunAlt, -12, false)
	d.Dawn = crossing(noon, end, 10*time.Minute, sunAlt, -12, true)
	d.Sunrise = crossing(noon, end, 10*time.Minute, sunAlt, -0.833, true)

	mi := moonInfo(now)
	d.Moon = SkyMoon{Phase: mi.Phase, Illumination: mi.Illumination}
	moonAlt := func(t time.Time) float64 {
		ra, dec, par := moonRADec(t)
		a, _ := altAz(t, ra, dec, lat, lon)
		return a - (0.7275*par - 0.5667) // horizon for the moon: parallax and refraction
	}
	d.Moon.Rise = crossing(noon, end, 10*time.Minute, moonAlt, 0, true)
	d.Moon.Set = crossing(noon, end, 10*time.Minute, moonAlt, 0, false)

	// planets: visible when at least 8° high while the sun is at least 8° below the horizon
	names := map[string]string{"mercury": "Mercurius", "venus": "Venus", "mars": "Mars", "jupiter": "Jupiter", "saturn": "Saturnus"}
	for _, key := range []string{"mercury", "venus", "mars", "jupiter", "saturn"} {
		var p *SkyPlanet
		bestAlt := -90.0
		var bestAz float64
		for t := noon; t.Before(end); t = t.Add(10 * time.Minute) {
			if sunAlt(t) > -8 {
				continue
			}
			ra, dec := planetRADec(key, t)
			alt, az := altAz(t, ra, dec, lat, lon)
			if alt < 8 {
				continue
			}
			if p == nil {
				p = &SkyPlanet{Name: names[key], From: t}
			}
			p.Until = t
			if alt > bestAlt {
				bestAlt, bestAz, p.Best = alt, az, t
			}
		}
		if p != nil && p.Until.Sub(p.From) >= 20*time.Minute {
			p.Alt, p.Dir = int(math.Round(bestAlt)), compass(bestAz)
			d.Planets = append(d.Planets, *p)
		}
	}
	sort.SliceStable(d.Planets, func(i, j int) bool { return d.Planets[i].From.Before(d.Planets[j].From) })

	today := noon
	for _, m := range meteorShowers {
		for _, y := range []int{today.Year() - 1, today.Year(), today.Year() + 1} {
			peak := time.Date(y, time.Month(m.Month), m.Day, 12, 0, 0, 0, loc)
			if diff := today.Sub(peak).Hours() / 24; diff >= -3 && diff <= 2 {
				d.Meteor = &SkyMeteor{Name: m.Name, Peak: peak.Format("2006-01-02"), ZHR: m.ZHR}
			}
		}
	}
	return d
}

// auroraLevel: seen from the Netherlands (about 52° N) the northern lights need a
// strong geomagnetic storm; Kp 5 shows up on camera, 6 now and then, 7+ often.
func auroraLevel(kp float64) int {
	switch {
	case kp >= 7:
		return 3
	case kp >= 6:
		return 2
	case kp >= 5:
		return 1
	}
	return 0
}

type kpPoint struct {
	Time time.Time
	Kp   float64
}

func parseKpForecast(body []byte) ([]kpPoint, error) {
	var rows []struct {
		Time string  `json:"time_tag"`
		Kp   float64 `json:"kp"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	var out []kpPoint
	for _, r := range rows {
		if t, err := time.Parse("2006-01-02T15:04:05", r.Time); err == nil {
			out = append(out, kpPoint{t.UTC(), r.Kp})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("noaa kp: empty")
	}
	return out, nil
}

// maxKp: the highest Kp for 3-hour blocks overlapping [from, to).
func maxKp(pts []kpPoint, from, to time.Time) *float64 {
	var best *float64
	for _, p := range pts {
		if p.Time.Before(to) && p.Time.Add(3*time.Hour).After(from) {
			v := p.Kp
			if best == nil || v > *best {
				best = &v
			}
		}
	}
	return best
}

func (a *App) handleSky(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Sky.Enabled {
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
	now := time.Now()
	d := computeSky(lat, lon, now)
	e := map[string]any{"enabled": true}
	if kp, ok := a.threats.get("noaa:kp").Data.([]kpPoint); ok && d.Dark != nil {
		to := end(d)
		if d.Kp = maxKp(kp, *d.Dark, to); d.Kp != nil {
			d.Aurora = auroraLevel(*d.Kp)
		}
	}
	// clouds: Open-Meteo, cached per ~10 km cell for an hour, shared with nobody else
	ip := a.clientIP(r)
	ctx := context.WithoutCancel(r.Context())
	cl, _, _, err := a.skyClouds.get(fmt.Sprintf("%.1f,%.1f", lat, lon), time.Hour, func() ([]cloudPoint, error) {
		if !a.wx.limiter.allow(ip) {
			return nil, errRateLimited
		}
		return a.fetchClouds(ctx, lat, lon)
	})
	if err == nil && d.Dark != nil {
		sum, n := 0, 0
		for _, c := range cl {
			if !c.Time.Before(*d.Dark) && c.Time.Before(end(d)) {
				sum += c.Cover
				n++
			}
		}
		if n > 0 {
			v := int(math.Round(float64(sum) / float64(n)))
			d.Clouds = &v
		}
	}
	e["data"] = d
	if cfg.Sky.Launches {
		l := a.feedEntry("ll2:launches")
		if v, ok := a.threats.get("ll2:launches").Data.([]Launch); ok {
			l["items"] = upcomingLaunches(v, cfg.Sky.LaunchesHours, time.Now())
		}
		l["hours"] = cfg.Sky.LaunchesHours
		e["launches"] = l
	}
	if cfg.Sky.SpaceWeather {
		sw := a.feedEntry("noaa:space")
		if v, ok := a.threats.get("noaa:space").Data.(SpaceWeather); ok {
			sw["days"], sw["flare"] = v.Days, v.Flare
		}
		e["space"] = sw
	}
	fe := a.feedEntry("noaa:kp")
	if v, ok := fe["fetched_at"]; ok {
		e["fetched_at"] = v
	} else {
		e["fetched_at"] = now.UTC().Truncate(time.Second)
	}
	writeJSON(w, r, http.StatusOK, 600, e)
}

// end of the dark part of the night (nautical dawn), or 6 hours after dark.
func end(d SkyData) time.Time {
	if d.Dawn != nil {
		return *d.Dawn
	}
	return d.Dark.Add(6 * time.Hour)
}

type cloudPoint struct {
	Time  time.Time
	Cover int
}

func (a *App) fetchClouds(ctx context.Context, lat, lon float64) ([]cloudPoint, error) {
	u := fmt.Sprintf("%s?latitude=%.1f&longitude=%.1f&hourly=cloud_cover&forecast_days=2&timeformat=unixtime", a.config().Sky.CloudsURL, lat, lon)
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
	if err != nil {
		return nil, fmt.Errorf("open-meteo clouds: %w", err)
	}
	var r struct {
		Hourly struct {
			Time  []int64    `json:"time"`
			Cover []*float64 `json:"cloud_cover"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	var out []cloudPoint
	for i, t := range r.Hourly.Time {
		if i < len(r.Hourly.Cover) && r.Hourly.Cover[i] != nil {
			out = append(out, cloudPoint{time.Unix(t, 0), int(math.Round(*r.Hourly.Cover[i]))})
		}
	}
	return out, nil
}
