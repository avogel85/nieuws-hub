package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Security-adviezen, tab Exploits: the newest public exploits (Exploit-DB) and the CVEs whose
// EPSS score (FIRST: the chance of exploitation in the next 30 days) rose most in a week.

type Exploit struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Kind      string    `json:"kind,omitempty"` // remote, webapps, local, dos
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
}

var exploitTitleRe = regexp.MustCompile(`^\[(\w+)\]\s*(.+)$`)

func parseExploitDB(body []byte, now time.Time) ([]Exploit, error) {
	items, err := parseFeed(body, "")
	if err != nil {
		return nil, fmt.Errorf("exploit-db: %w", err)
	}
	out := []Exploit{}
	for _, it := range items {
		t, ok := parseDate(it.Date)
		title := strings.Join(strings.Fields(plainText(it.Title)), " ")
		if !ok || title == "" || !strings.HasPrefix(it.Link, "https://www.exploit-db.com/") || now.Sub(t) > 30*24*time.Hour {
			continue
		}
		e := Exploit{Title: title, URL: it.Link, Published: t}
		if m := exploitTitleRe.FindStringSubmatch(title); m != nil {
			e.Kind, e.Title = strings.ToLower(m[1]), m[2]
		}
		if i := strings.LastIndex(it.Link, "/"); i >= 0 {
			e.ID = it.Link[i+1:]
		}
		out = append(out, e)
		if len(out) == 15 {
			break
		}
	}
	return out, nil
}

type EPSSRiser struct {
	CVE        string  `json:"cve"`
	EPSS       float64 `json:"epss"`       // now, 0-1
	Prev       float64 `json:"prev"`       // a week ago
	Percentile float64 `json:"percentile"` // now
}

type EPSSData struct {
	Date   string      `json:"date"`  // score date of the newest file
	Since  string      `json:"since"` // score date of the older file
	Risers []EPSSRiser `json:"risers"`
}

// readEPSS streams an EPSS daily file (gzip CSV: "#model_version:…,score_date:2026-10-03T…",
// "cve,epss,percentile", rows) and calls fn per row; it returns the score date.
func readEPSS(gz []byte, fn func(cve string, epss, pct float64)) (string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return "", fmt.Errorf("epss: %w", err)
	}
	sc := bufio.NewScanner(io.LimitReader(zr, 128<<20))
	date, rows := "", 0
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			if i := strings.Index(line, "score_date:"); i >= 0 && len(line) >= i+21 {
				date = line[i+11 : i+21]
			}
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 3 || !strings.HasPrefix(f[0], "CVE-") {
			continue
		}
		e, err1 := strconv.ParseFloat(f[1], 64)
		p, err2 := strconv.ParseFloat(f[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		fn(f[0], e, p)
		rows++
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("epss: %w", err)
	}
	if rows < 1000 {
		return "", fmt.Errorf("epss: only %d rows", rows)
	}
	return date, nil
}

// epssRisers: the CVEs with the biggest rise between two files, ending at least at 10 %.
func epssRisers(older, newer []byte, n int) (EPSSData, error) {
	prev := map[string]float32{}
	since, err := readEPSS(older, func(c string, e, _ float64) { prev[c] = float32(e) })
	if err != nil {
		return EPSSData{}, err
	}
	var all []EPSSRiser
	date, err := readEPSS(newer, func(c string, e, p float64) {
		if e < 0.1 {
			return
		}
		if d := e - float64(prev[c]); d >= 0.05 {
			all = append(all, EPSSRiser{CVE: c, EPSS: e, Prev: float64(prev[c]), Percentile: p})
		}
	})
	if err != nil {
		return EPSSData{}, err
	}
	sort.Slice(all, func(i, j int) bool {
		di, dj := all[i].EPSS-all[i].Prev, all[j].EPSS-all[j].Prev
		if di != dj {
			return di > dj
		}
		return all[i].CVE > all[j].CVE
	})
	if len(all) > n {
		all = all[:n]
	}
	if all == nil {
		all = []EPSSRiser{}
	}
	return EPSSData{Date: date, Since: since, Risers: all}, nil
}

func (a *App) runEPSS(ctx context.Context) error {
	base := strings.TrimRight(a.config().Exploits.EPSSURL, "/")
	get := func(name string) ([]byte, error) {
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "/" + name, Accept: "application/gzip, application/octet-stream", Timeout: 60 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("epss %s: %w", name, err)
		}
		return resp.Body, nil
	}
	newer, err := get("epss_scores-current.csv.gz")
	var older []byte
	if err == nil {
		// a week back from the newest file's date (files appear a few hours after midnight UTC)
		day := time.Now().UTC().AddDate(0, 0, -7)
		older, err = get("epss_scores-" + day.Format("2006-01-02") + ".csv.gz")
		if err != nil {
			older, err = get("epss_scores-" + day.AddDate(0, 0, -1).Format("2006-01-02") + ".csv.gz")
		}
	}
	if err == nil {
		var d EPSSData
		if d, err = epssRisers(older, newer, 10); err == nil {
			a.threats.ok("epss:risers", d, "", "")
			return nil
		}
	}
	a.threats.fail("epss:risers", err)
	return err
}

// handleExploits: GET /api/exploits.
func (a *App) handleExploits(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Exploits.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	ed := a.feedEntry("exploitdb")
	if v, ok := a.threats.get("exploitdb").Data.([]Exploit); ok {
		ed["items"] = v
	}
	ep := a.feedEntry("epss:risers")
	if v, ok := a.threats.get("epss:risers").Data.(EPSSData); ok {
		ep["date"], ep["since"], ep["risers"] = v.Date, v.Since, v.Risers
		kev := map[string]bool{}
		for _, k := range asSlice[KEVItem](a.threats.get("cisa:kev").Data) {
			kev[k.CVE] = true
		}
		if len(kev) > 0 {
			var in []string
			for _, x := range v.Risers {
				if kev[x.CVE] {
					in = append(in, x.CVE)
				}
			}
			ep["kev"] = in
		}
	}
	writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": true, "exploitdb": ed, "epss": ep})
}

// ---------------------------------------------------------------------------
// Cyberdreigingen, tabs Malware NL and IOC's (abuse.ch). Malware URLs and indicators are only
// shown defanged (hxxp://, example[.]com) and never as links.

func defang(s string) string {
	s = strings.Replace(s, "http://", "hxxp://", 1)
	s = strings.Replace(s, "https://", "hxxps://", 1)
	if i := strings.LastIndex(s, "."); i > 0 {
		s = s[:i] + "[.]" + s[i+1:]
	}
	return s
}

type URLhausNL struct {
	Online  int            `json:"online"`  // active malware URLs hosted in the Netherlands
	Week    int            `json:"week"`    // added in the last 7 days (online or not)
	Threats map[string]int `json:"threats"` // online, per threat type
	ASNs    []ASNCount     `json:"asns"`    // online, top networks
	Newest  []URLhausItem  `json:"newest"`
}

type ASNCount struct {
	ASN   int    `json:"asn"`
	Name  string `json:"name,omitempty"`
	Count int    `json:"count"`
}

type URLhausItem struct {
	Added  time.Time `json:"added"`
	URL    string    `json:"url"` // defanged
	Threat string    `json:"threat"`
}

// parseURLhausNL reads the URLhaus country feed (CSV; dateadded,url,url_status,threat,host,ip,asn,country).
func parseURLhausNL(body []byte, now time.Time) (URLhausNL, error) {
	d := URLhausNL{Threats: map[string]int{}}
	asn := map[int]int{}
	rows := 0
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, `"`) {
			continue
		}
		f := strings.Split(strings.Trim(line, `"`), `","`)
		if len(f) < 8 {
			continue
		}
		rows++
		added, err := time.Parse("2006-01-02 15:04:05", f[0])
		if err != nil {
			continue
		}
		if now.Sub(added) <= 7*24*time.Hour {
			d.Week++
		}
		if f[2] != "online" {
			continue
		}
		d.Online++
		d.Threats[f[3]]++
		if n, err := strconv.Atoi(f[6]); err == nil && n > 0 {
			asn[n]++
		}
		if len(d.Newest) < 5 {
			d.Newest = append(d.Newest, URLhausItem{Added: added, URL: defang(f[1]), Threat: f[3]})
		}
	}
	if rows == 0 {
		return URLhausNL{}, errors.New("urlhaus: no rows")
	}
	for n, c := range asn {
		d.ASNs = append(d.ASNs, ASNCount{ASN: n, Count: c})
	}
	sort.Slice(d.ASNs, func(i, j int) bool {
		if d.ASNs[i].Count != d.ASNs[j].Count {
			return d.ASNs[i].Count > d.ASNs[j].Count
		}
		return d.ASNs[i].ASN < d.ASNs[j].ASN
	})
	if len(d.ASNs) > 5 {
		d.ASNs = d.ASNs[:5]
	}
	return d, nil
}

func (a *App) runURLhausNL(ctx context.Context) error {
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: a.config().Threats.URLhausNLURL, Accept: "text/plain", MaxBody: 40 << 20, Timeout: 60 * time.Second})
	var d URLhausNL
	if err == nil {
		d, err = parseURLhausNL(resp.Body, time.Now())
	}
	if err != nil {
		a.threats.fail("urlhaus:nl", err)
		return err
	}
	for i := range d.ASNs {
		d.ASNs[i].Name = a.asnName(ctx, d.ASNs[i].ASN)
	}
	a.threats.ok("urlhaus:nl", d, "", "")
	return nil
}

// asnName: the organisation behind an AS number, from Shadowserver's public lookup (no key),
// remembered for the life of the process.
func (a *App) asnName(ctx context.Context, asn int) string {
	a.asnMu.Lock()
	n, ok := a.asnNames[asn]
	a.asnMu.Unlock()
	if ok {
		return n
	}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: fmt.Sprintf("https://api.shadowserver.org/net/asn?query=%d", asn), Accept: "application/json"})
	if err != nil {
		return ""
	}
	var r struct {
		Name string `json:"asn_name"`
	}
	if json.Unmarshal(resp.Body, &r) != nil {
		return ""
	}
	a.asnMu.Lock()
	if len(a.asnNames) < 5000 {
		a.asnNames[asn] = r.Name
	}
	a.asnMu.Unlock()
	return r.Name
}

type ThreatFoxData struct {
	Total    int            `json:"total"` // indicators in the last 24 hours
	Families []FamilyCount  `json:"families"`
	Types    map[string]int `json:"types"` // per threat type (botnet_cc, payload_delivery, …)
	Newest   []ThreatFoxIOC `json:"newest"`
}

type FamilyCount struct {
	Name     string `json:"name"`
	Count    int    `json:"count"`
	Malpedia string `json:"malpedia,omitempty"`
}

type ThreatFoxIOC struct {
	IOC        string    `json:"ioc"` // defanged
	Type       string    `json:"type"`
	Threat     string    `json:"threat"`
	Family     string    `json:"family"`
	Confidence int       `json:"confidence"`
	FirstSeen  time.Time `json:"first_seen"`
}

// parseThreatFox reads get_iocs (days=1). Families that Feodo Tracker already shows in the
// Botnet C2 tab (Emotet/Heodo, Dridex, TrickBot, QakBot, BazarLoader) are left out of the list.
func parseThreatFox(body []byte) (ThreatFoxData, error) {
	var r struct {
		Status string          `json:"query_status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return ThreatFoxData{}, fmt.Errorf("threatfox: %w", err)
	}
	if r.Status != "ok" {
		return ThreatFoxData{}, fmt.Errorf("threatfox: %s", r.Status)
	}
	var items []struct {
		IOC        string `json:"ioc"`
		IOCType    string `json:"ioc_type"`
		Threat     string `json:"threat_type"`
		Family     string `json:"malware_printable"`
		Malpedia   string `json:"malware_malpedia"`
		Confidence int    `json:"confidence_level"`
		FirstSeen  string `json:"first_seen"`
	}
	if err := json.Unmarshal(r.Data, &items); err != nil {
		return ThreatFoxData{}, fmt.Errorf("threatfox: %w", err)
	}
	feodo := map[string]bool{"emotet": true, "heodo": true, "dridex": true, "trickbot": true, "qakbot": true, "qbot": true, "bazarloader": true}
	d := ThreatFoxData{Types: map[string]int{}}
	fam := map[string]*FamilyCount{}
	for _, it := range items {
		d.Total++
		d.Types[it.Threat]++
		name := strings.TrimSpace(it.Family)
		if name == "" || strings.HasPrefix(strings.ToLower(name), "unknown") || feodo[strings.ToLower(name)] {
			continue
		}
		if fam[name] == nil {
			fam[name] = &FamilyCount{Name: name}
			if strings.HasPrefix(it.Malpedia, "https://malpedia.caad.fkie.fraunhofer.de/") {
				fam[name].Malpedia = it.Malpedia
			}
		}
		fam[name].Count++
		if len(d.Newest) < 5 && it.Confidence >= 75 {
			t, _ := time.Parse("2006-01-02 15:04:05 MST", it.FirstSeen)
			d.Newest = append(d.Newest, ThreatFoxIOC{IOC: defang(it.IOC), Type: it.IOCType, Threat: it.Threat, Family: name, Confidence: it.Confidence, FirstSeen: t})
		}
	}
	for _, f := range fam {
		d.Families = append(d.Families, *f)
	}
	sort.Slice(d.Families, func(i, j int) bool {
		if d.Families[i].Count != d.Families[j].Count {
			return d.Families[i].Count > d.Families[j].Count
		}
		return d.Families[i].Name < d.Families[j].Name
	})
	if len(d.Families) > 8 {
		d.Families = d.Families[:8]
	}
	return d, nil
}

func (a *App) runThreatFox(ctx context.Context) error {
	cfg := a.config()
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: cfg.Threats.ThreatFoxURL, Method: http.MethodPost, Accept: "application/json",
		Header: map[string]string{"Auth-Key": cfg.Keys.AbusechAuthKey, "Content-Type": "application/json"}, Body: []byte(`{"query":"get_iocs","days":1}`)})
	var d ThreatFoxData
	if err == nil {
		d, err = parseThreatFox(resp.Body)
	}
	if err != nil {
		a.threats.fail("threatfox:iocs", err)
		return err
	}
	a.threats.ok("threatfox:iocs", d, "", "")
	return nil
}

// ---------------------------------------------------------------------------
// Dreigingsbeeld NL: DDoS attacks on the Netherlands and BGP hijacks and route leaks involving
// Dutch networks, from Cloudflare Radar (needs a free API token with Radar read access).

type RadarNL struct {
	Trend    []float64      `json:"trend"`   // layer-3 attacks per day, 0-1 relative to the week's peak
	Days     []string       `json:"days"`    // the dates of Trend
	Vectors  []RadarShare   `json:"vectors"` // layer-3 attack types, share in %
	Origins  []RadarShare   `json:"origins"` // layer-7 attacks on NL by origin country, share in %
	Hijacks  []BGPEvent     `json:"hijacks"` // newest first, confidence ≥ 5
	Leaks    []BGPEvent     `json:"leaks"`   // newest first
	HijacksN int            `json:"hijacks_n"`
	LeaksN   int            `json:"leaks_n"`
	ASNames  map[int]string `json:"as_names"` // names of the ASNs in the events
}

type RadarShare struct {
	Name  string  `json:"name"`
	Code  string  `json:"code,omitempty"`
	Share float64 `json:"share"`
}

type BGPEvent struct {
	At        time.Time `json:"at"`
	ASN       int       `json:"asn"`                // hijacker or leaking network
	Victims   []int     `json:"victims,omitempty"`  // hijack: networks whose prefixes were announced
	Prefixes  []string  `json:"prefixes,omitempty"` // hijack
	Countries []string  `json:"countries,omitempty"`
	Ongoing   bool      `json:"ongoing,omitempty"`
	Score     int       `json:"score,omitempty"` // hijack confidence (Cloudflare: higher = more certain)
}

func radarShares(m map[string]string, limit int) []RadarShare {
	var out []RadarShare
	for k, v := range m {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || k == "other" {
			continue
		}
		out = append(out, RadarShare{Name: k, Share: f})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Share > out[j].Share })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func radarTime(s string) time.Time {
	for _, l := range []string{"2006-01-02T15:04:05.000", "2006-01-02T15:04:05", time.RFC3339} {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func (a *App) radarGet(ctx context.Context, path string, v any) error {
	cfg := a.config()
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: strings.TrimRight(cfg.NLThreat.RadarURL, "/") + "/" + path, Accept: "application/json",
		Header: map[string]string{"Authorization": "Bearer " + cfg.Keys.CloudflareRadarToken}})
	if err != nil {
		if resp != nil && (resp.Status == 401 || resp.Status == 403) {
			return errors.New("Cloudflare Radar: token ongeldig of zonder Radar-rechten")
		}
		return fmt.Errorf("Cloudflare Radar: %w", err)
	}
	var env struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil || !env.Success {
		return fmt.Errorf("Cloudflare Radar: unexpected reply for %s", strings.SplitN(path, "?", 2)[0])
	}
	return json.Unmarshal(env.Result, v)
}

type radarBGP struct {
	ASNInfo []struct {
		ASN  int    `json:"asn"`
		Name string `json:"org_name"`
	} `json:"asn_info"`
	Events []struct {
		MinHijack  string   `json:"min_hijack_ts"`
		Detected   string   `json:"detected_ts"`
		Hijacker   int      `json:"hijacker_asn"`
		LeakASN    int      `json:"leak_asn"`
		Victims    []int    `json:"victim_asns"`
		Prefixes   []string `json:"prefixes"`
		VCountries []string `json:"victim_countries"`
		Countries  []string `json:"countries"`
		Ongoing    int      `json:"on_going_count"`
		Finished   *bool    `json:"finished"`
		Score      int      `json:"confidence_score"`
	} `json:"events"`
	Info struct {
		Total int `json:"total_count"`
	} `json:"result_info"`
}

func (a *App) runRadarNL(ctx context.Context) error {
	cc := a.config().NLThreat.Country
	var d RadarNL
	d.ASNames = map[int]string{}
	var ts struct {
		Serie struct {
			Timestamps []string `json:"timestamps"`
			Values     []string `json:"values"`
		} `json:"serie_0"`
	}
	err := a.radarGet(ctx, "attacks/layer3/timeseries?location="+cc+"&dateRange=7d&aggInterval=1d&normalization=MIN0_MAX", &ts)
	if err == nil {
		for i, v := range ts.Serie.Values {
			f, _ := strconv.ParseFloat(v, 64)
			d.Trend = append(d.Trend, f)
			if i < len(ts.Serie.Timestamps) && len(ts.Serie.Timestamps[i]) >= 10 {
				d.Days = append(d.Days, ts.Serie.Timestamps[i][:10])
			}
		}
		var vec struct {
			Summary map[string]string `json:"summary_0"`
		}
		if err = a.radarGet(ctx, "attacks/layer3/summary/vector?location="+cc+"&dateRange=7d", &vec); err == nil {
			d.Vectors = radarShares(vec.Summary, 5)
		}
	}
	if err == nil {
		var org struct {
			Top []struct {
				Code  string `json:"originCountryAlpha2"`
				Name  string `json:"originCountryName"`
				Value string `json:"value"`
			} `json:"top_0"`
		}
		if err = a.radarGet(ctx, "attacks/layer7/top/locations/origin?location="+cc+"&dateRange=7d&limit=5", &org); err == nil {
			for _, o := range org.Top {
				f, _ := strconv.ParseFloat(o.Value, 64)
				d.Origins = append(d.Origins, RadarShare{Name: o.Name, Code: o.Code, Share: f})
			}
		}
	}
	if err == nil {
		var h radarBGP
		if err = a.radarGet(ctx, "bgp/hijacks/events?involvedCountry="+cc+"&dateRange=7d&minConfidence=5&per_page=5&sortBy=TIME&sortOrder=DESC", &h); err == nil {
			for _, x := range h.ASNInfo {
				d.ASNames[x.ASN] = x.Name
			}
			for _, e := range h.Events {
				d.Hijacks = append(d.Hijacks, BGPEvent{At: radarTime(e.MinHijack), ASN: e.Hijacker, Victims: e.Victims, Prefixes: e.Prefixes, Countries: e.VCountries, Ongoing: e.Ongoing > 0, Score: e.Score})
			}
			d.HijacksN = max(h.Info.Total, len(d.Hijacks))
		}
	}
	if err == nil {
		var l radarBGP
		if err = a.radarGet(ctx, "bgp/leaks/events?involvedCountry="+cc+"&dateRange=7d&per_page=5&sortBy=TIME&sortOrder=DESC", &l); err == nil {
			for _, x := range l.ASNInfo {
				d.ASNames[x.ASN] = x.Name
			}
			for _, e := range l.Events {
				d.Leaks = append(d.Leaks, BGPEvent{At: radarTime(e.Detected), ASN: e.LeakASN, Countries: e.Countries, Ongoing: e.Finished != nil && !*e.Finished})
			}
			d.LeaksN = max(l.Info.Total, len(d.Leaks))
		}
	}
	if err != nil {
		a.threats.fail("radar:nl", err)
		return err
	}
	a.threats.ok("radar:nl", d, "", "")
	return nil
}

// handleNLThreat: GET /api/nlthreat. The incident timeline is built in the page from the news.
func (a *App) handleNLThreat(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.NLThreat.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := map[string]any{"enabled": true, "country": cfg.NLThreat.Country}
	rd := a.feedEntry("radar:nl")
	if cfg.Keys.CloudflareRadarToken == "" {
		rd["missing_key"] = true
	} else if v, ok := a.threats.get("radar:nl").Data.(RadarNL); ok {
		rd["data"] = v
	}
	e["radar"] = rd
	writeJSON(w, r, http.StatusOK, 300, e)
}
