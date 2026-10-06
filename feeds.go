package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Fetcher: polite outbound HTTP with global and per-host concurrency limits.

const (
	maxBody      = 5 << 20
	perHostLimit = 2
	feedAccept   = "application/rss+xml, application/atom+xml, application/rdf+xml, application/feed+json, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.5"
)

// tls12Hosts: servers that never answer a Go TLS 1.3 handshake (the connection stalls until the
// timeout) but work over TLS 1.2. IODA: checked 2026-09-26, curl and Python are fine over TLS 1.3.
var tls12Hosts = map[string]bool{"api.ioda.inetintel.cc.gatech.edu": true}

type Fetcher struct {
	client   *http.Client
	client12 *http.Client // TLS 1.2 only, for tls12Hosts
	global   chan struct{}
	mu       sync.Mutex
	perHost  map[string]chan struct{}
	ua       func() string
	timeout  func() time.Duration
}

func newFetcher(maxConcurrent int, ua func() string, timeout func() time.Duration) *Fetcher {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = perHostLimit
	tr.IdleConnTimeout = 60 * time.Second
	tr12 := tr.Clone()
	tr12.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}
	redirects := func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("more than 3 redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("redirect to non-http URL")
		}
		return nil
	}
	return &Fetcher{
		client:   &http.Client{Transport: tr, CheckRedirect: redirects},
		client12: &http.Client{Transport: tr12, CheckRedirect: redirects},
		global:   make(chan struct{}, maxConcurrent),
		perHost:  map[string]chan struct{}{},
		ua:       ua,
		timeout:  timeout,
	}
}

type FetchReq struct {
	URL, Method, Accept string
	ETag, LastModified  string
	Header              map[string]string
	Body                []byte
	Timeout             time.Duration // overrides fetch.timeout for a slow source (0 = the configured timeout)
	MaxBody             int           // overrides the 5 MB response limit for a known large file (0 = 5 MB)
}

type FetchResp struct {
	Status       int
	Body         []byte
	Header       http.Header
	NotModified  bool
	ETag, LastMo string
}

// acquire takes a per-host slot first, then a global one, so goroutines queued
// behind a busy host never hold a global slot.
func (f *Fetcher) acquire(ctx context.Context, host string) (func(), error) {
	f.mu.Lock()
	h := f.perHost[host]
	if h == nil {
		h = make(chan struct{}, perHostLimit)
		f.perHost[host] = h
	}
	f.mu.Unlock()
	select {
	case h <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case f.global <- struct{}{}:
	case <-ctx.Done():
		<-h
		return nil, ctx.Err()
	}
	return func() { <-f.global; <-h }, nil
}

func (f *Fetcher) Do(ctx context.Context, fr FetchReq) (*FetchResp, error) {
	u, err := url.Parse(fr.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid URL %q", fr.URL)
	}
	release, err := f.acquire(ctx, u.Hostname())
	if err != nil {
		return nil, err
	}
	defer release()

	timeout := f.timeout()
	if fr.Timeout > 0 {
		timeout = fr.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	method := fr.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if fr.Body != nil {
		body = bytes.NewReader(fr.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fr.URL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.ua())
	accept := fr.Accept
	if accept == "" {
		accept = "*/*"
	}
	req.Header.Set("Accept", accept)
	if fr.ETag != "" {
		req.Header.Set("If-None-Match", fr.ETag)
	}
	if fr.LastModified != "" {
		req.Header.Set("If-Modified-Since", fr.LastModified)
	}
	for k, v := range fr.Header {
		req.Header.Set(k, v)
	}
	client := f.client
	if tls12Hosts[req.URL.Hostname()] {
		client = f.client12
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, shortErr(err)
	}
	defer resp.Body.Close()
	out := &FetchResp{Status: resp.StatusCode, Header: resp.Header,
		ETag: resp.Header.Get("ETag"), LastMo: resp.Header.Get("Last-Modified")}
	if resp.StatusCode == http.StatusNotModified {
		out.NotModified = true
		return out, nil
	}
	limit := maxBody
	if fr.MaxBody > 0 {
		limit = fr.MaxBody
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", shortErr(err))
	}
	if len(b) > limit {
		return nil, fmt.Errorf("response larger than %d MB", limit>>20)
	}
	out.Body = b
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return out, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return out, nil
}

func shortErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("timeout")
	}
	return err
}

// ---------------------------------------------------------------------------
// Scheduler: one loop, per-job interval with ±10 % jitter and exponential backoff.

type Job struct {
	Key      string
	Sig      string // jobs with an unchanged Sig keep their schedule across reloads
	Interval time.Duration
	Run      func(ctx context.Context) error
}

type jobState struct {
	job      Job
	next     time.Time
	failures int
	running  bool
}

type Scheduler struct {
	mu   sync.Mutex
	jobs map[string]*jobState
}

func newScheduler() *Scheduler { return &Scheduler{jobs: map[string]*jobState{}} }

// Set replaces the job list. New jobs start within 20 s (staggered), existing ones keep their timing.
func (s *Scheduler) Set(jobs []Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]*jobState, len(jobs))
	now := time.Now()
	for _, j := range jobs {
		if old, ok := s.jobs[j.Key]; ok && old.job.Sig == j.Sig {
			old.job = j
			next[j.Key] = old
			continue
		}
		next[j.Key] = &jobState{job: j, next: now.Add(time.Duration(rand.Int64N(int64(20 * time.Second))))}
	}
	s.jobs = next
}

func (s *Scheduler) Loop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.mu.Lock()
			for _, st := range s.jobs {
				if !st.running && !now.Before(st.next) {
					st.running = true
					go s.run(ctx, st)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *Scheduler) run(ctx context.Context, st *jobState) {
	err := st.job.Run(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	st.running = false
	if err != nil {
		st.failures++
		st.next = time.Now().Add(backoff(st.failures))
		return
	}
	st.failures = 0
	st.next = time.Now().Add(jitter(st.job.Interval))
}

func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.9 + 0.2*rand.Float64()))
}

// backoff: 2, 4, 8, 16, 32 min, then capped at 1 h.
func backoff(failures int) time.Duration {
	d := 2 * time.Minute
	for i := 1; i < failures && d < time.Hour; i++ {
		d *= 2
	}
	return min(jitter(d), time.Hour)
}

// ---------------------------------------------------------------------------
// News cache

type Item struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary,omitempty"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
	Image     string    `json:"image,omitempty"`
	Related   []Related `json:"related,omitempty"` // the same story at other outlets

	canon     string
	normTitle string
	tokens    []string // sorted content words of title + start of summary
	titleTok  []string // sorted content words of the title
}

// Related is another outlet's article about the same story.
type Related struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
}

type SourceState struct {
	Items        []Item
	FetchedAt    time.Time // last successful response (200 or 304)
	LastAttempt  time.Time
	ETag         string
	LastModified string
	Error        string
	ErrorCount   int
	ErrorSince   time.Time
}

type SourceStatus struct {
	OK         bool       `json:"ok"`
	Pending    bool       `json:"pending,omitempty"`
	Items      int        `json:"items"`
	FetchedAt  *time.Time `json:"fetched_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	ErrorSince *time.Time `json:"error_since,omitempty"`
	ErrorCount int        `json:"error_count,omitempty"`
}

type NewsCache struct {
	mu      sync.RWMutex
	sources map[string]*SourceState
}

func newNewsCache() *NewsCache { return &NewsCache{sources: map[string]*SourceState{}} }

func (c *NewsCache) state(id string) SourceState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if s := c.sources[id]; s != nil {
		return *s
	}
	return SourceState{}
}

func (c *NewsCache) update(id string, fn func(s *SourceState)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sources[id]
	if s == nil {
		s = &SourceState{}
		c.sources[id] = s
	}
	fn(s)
}

func (c *NewsCache) prune(keep map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.sources {
		if !keep[id] {
			delete(c.sources, id)
		}
	}
}

// collect returns the item lists (shared, read-only) and status for the given ids.
func (c *NewsCache) collect(ids []string) ([][]Item, map[string]SourceStatus) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	lists := make([][]Item, 0, len(ids))
	status := make(map[string]SourceStatus, len(ids))
	for _, id := range ids {
		s := c.sources[id]
		if s == nil {
			status[id] = SourceStatus{Pending: true}
			continue
		}
		lists = append(lists, s.Items)
		st := SourceStatus{Items: len(s.Items), OK: s.Error == "" && !s.FetchedAt.IsZero(), Error: s.Error, ErrorCount: s.ErrorCount}
		if !s.FetchedAt.IsZero() {
			t := s.FetchedAt.UTC().Truncate(time.Second)
			st.FetchedAt = &t
		}
		if !s.ErrorSince.IsZero() {
			t := s.ErrorSince.UTC().Truncate(time.Second)
			st.ErrorSince = &t
		}
		status[id] = st
	}
	return lists, status
}

func (a *App) fetchSource(ctx context.Context, src Source) error {
	cfg := a.config()
	prev := a.news.state(src.ID)
	now := time.Now()
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: src.URL, Accept: feedAccept, ETag: prev.ETag, LastModified: prev.LastModified})
	if err == nil {
		if resp.NotModified {
			a.news.update(src.ID, func(s *SourceState) {
				s.Items = pruneOld(s.Items, now.Add(-cfg.maxAge(src)))
				s.FetchedAt, s.LastAttempt = now, now
				s.Error, s.ErrorCount, s.ErrorSince = "", 0, time.Time{}
			})
			slog.Debug("feed not modified", "source", src.ID)
			return nil
		}
		var raws []rawItem
		if raws, err = parseFeed(resp.Body, src.Type); err == nil {
			known := make(map[string]time.Time, len(prev.Items))
			for _, it := range prev.Items {
				known[it.canon] = it.Published
			}
			items := normalizeItems(src.ID, src.URL, raws, normOpts{
				now: now, known: known, maxAge: cfg.maxAge(src),
				maxItems: cfg.Cache.MaxItemsPerSource, images: cfg.Features.ShowImages,
			})
			a.news.update(src.ID, func(s *SourceState) {
				s.Items = items
				s.FetchedAt, s.LastAttempt = now, now
				s.ETag, s.LastModified = resp.ETag, resp.LastMo
				s.Error, s.ErrorCount, s.ErrorSince = "", 0, time.Time{}
			})
			slog.Debug("feed fetched", "source", src.ID, "items", len(items))
			return nil
		}
	}
	a.news.update(src.ID, func(s *SourceState) {
		s.LastAttempt = now
		s.Error = err.Error()
		s.ErrorCount++
		if s.ErrorSince.IsZero() {
			s.ErrorSince = now
		}
		s.Items = pruneOld(s.Items, now.Add(-cfg.maxAge(src)))
	})
	slog.Warn("fetch failed", "source", src.ID, "err", err)
	return err
}

func pruneOld(items []Item, cutoff time.Time) []Item {
	out := items[:0:0]
	for _, it := range items {
		if !it.Published.Before(cutoff) {
			out = append(out, it)
		}
	}
	return out
}

// mergeItems combines per-source lists, newest first, de-duplicated by canonical
// URL (across sources) and by normalised title within the same source. With a
// non-nil lang map, articles about the same story from different outlets are
// grouped under the newest one (Item.Related).
func mergeItems(lists [][]Item, since time.Time, limit int, lang map[string]string) []Item {
	var all []Item
	for _, l := range lists {
		for _, it := range l {
			if since.IsZero() || it.Published.After(since) {
				all = append(all, it)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].Published.Equal(all[j].Published) {
			return all[i].Published.After(all[j].Published)
		}
		return all[i].Source < all[j].Source
	})
	seenURL := map[string]bool{}
	seenTitle := map[string]bool{}
	// With grouping, look a bit beyond the limit so groups can absorb items; without, stop at the limit.
	window := limit
	if lang != nil {
		window = limit*2 + 100
	}
	out := make([]Item, 0, min(window, len(all)))
	for _, it := range all {
		tk := it.Source + "\x00" + it.normTitle
		if seenURL[it.canon] || (it.normTitle != "" && seenTitle[tk]) {
			continue
		}
		seenURL[it.canon], seenTitle[tk] = true, true
		out = append(out, it)
		if len(out) >= window {
			break
		}
	}
	if lang != nil {
		out = groupStories(out, lang)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// topUpPerSource adds older articles so every source has at least minPer in the
// list (counting grouped articles too). Without it, a slow source (investigative
// outlets publish a few pieces a week) falls outside the newest N articles of 80
// busy sources, and its category looks empty. The result stays newest first.
func topUpPerSource(out []Item, lists [][]Item, since time.Time, minPer int) []Item {
	if minPer <= 0 {
		return out
	}
	have := map[string]int{}
	seenURL, seenTitle := map[string]bool{}, map[string]bool{}
	for _, it := range out {
		have[it.Source]++
		seenURL[it.canon], seenTitle[it.Source+"\x00"+it.normTitle] = true, true
		for _, r := range it.Related {
			have[r.Source]++
			seenURL[canonicalURL(r.URL)] = true
		}
	}
	added := false
	for _, l := range lists {
		list := slices.Clone(l)
		sort.SliceStable(list, func(i, j int) bool { return list[i].Published.After(list[j].Published) })
		for _, it := range list {
			if have[it.Source] >= minPer {
				break
			}
			tk := it.Source + "\x00" + it.normTitle
			if (!since.IsZero() && !it.Published.After(since)) || seenURL[it.canon] || (it.normTitle != "" && seenTitle[tk]) {
				continue
			}
			seenURL[it.canon], seenTitle[tk] = true, true
			have[it.Source]++
			out = append(out, it)
			added = true
		}
	}
	if added {
		sort.SliceStable(out, func(i, j int) bool {
			if !out[i].Published.Equal(out[j].Published) {
				return out[i].Published.After(out[j].Published)
			}
			return out[i].Source < out[j].Source
		})
	}
	return out
}

// groupStories folds articles about the same story into the newest one. Each
// candidate is compared with the group's lead only (no chaining A≈B≈C), and a
// group holds at most one article per source.
func groupStories(items []Item, lang map[string]string) []Item {
	used := make([]bool, len(items))
	out := make([]Item, 0, len(items))
	for i := range items {
		if used[i] {
			continue
		}
		lead := items[i]
		lead.Related = nil
		srcs := map[string]bool{lead.Source: true}
		for j := i + 1; j < len(items) && len(lead.Related) < 8; j++ {
			if used[j] || srcs[items[j].Source] {
				continue
			}
			if items[i].Published.Sub(items[j].Published) > storyWindow {
				break // sorted newest first: everything further is older still
			}
			if sameStory(&items[i], &items[j], lang) {
				used[j], srcs[items[j].Source] = true, true
				r := items[j]
				lead.Related = append(lead.Related, Related{ID: r.ID, Source: r.Source, Title: r.Title, URL: r.URL, Published: r.Published})
			}
		}
		out = append(out, lead)
	}
	return out
}

const storyWindow = 36 * time.Hour

// sameStory: same language, published within 36 h, and either ≥ 25 % word overlap
// (Jaccard, ≥ 3 shared words) on title + summary start, or ≥ 60 % of the shorter
// title's words shared (≥ 3). Thresholds tuned on a day of Dutch headlines.
func sameStory(a, b *Item, lang map[string]string) bool {
	if lang[a.Source] != lang[b.Source] {
		return false
	}
	if d := a.Published.Sub(b.Published); d > storyWindow || d < -storyWindow {
		return false
	}
	if in := intersectCount(a.tokens, b.tokens); in >= 3 {
		if float64(in)/float64(len(a.tokens)+len(b.tokens)-in) >= 0.25 {
			return true
		}
	}
	in := intersectCount(a.titleTok, b.titleTok)
	short := min(len(a.titleTok), len(b.titleTok))
	return in >= 3 && short > 0 && float64(in)/float64(short) >= 0.6
}

func intersectCount(a, b []string) int {
	n, i, j := 0, 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			n++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return n
}

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`de het een en van in op te voor met is dat die niet aan er om bij zijn ook als naar door
		over nog wordt worden meer dan uit tot na al of zo maar hij zij ze we wij je jij was waren heeft hebben had
		kan kunnen moet moeten zal zullen gaat gaan wil willen deze dit hun haar hem mijn ons onze jaar jaren eerste
		nieuwe nieuw tegen onder tussen zich nu weer geen veel alle wel waar wat wie hoe toch even komt komen
		the and for are was were with that this from has have had not but its his her they their will would
		after over into about more than new says said amid what when who how why can could been being`) {
		m[w] = true
	}
	return m
}()

// contentWords returns the sorted, unique words of s that carry meaning (≥ 3 letters, no stopwords).
func contentWords(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(w) < 3 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

func setTokens(it *Item) {
	sum := it.Summary
	if r := []rune(sum); len(r) > 200 {
		sum = string(r[:200])
	}
	it.titleTok = contentWords(it.Title)
	it.tokens = contentWords(it.Title + " " + sum)
}

// ---------------------------------------------------------------------------
// Snapshot (optional warm start; the only code path that writes to disk)

type snapshotFile struct {
	Version int                     `json:"version"`
	Saved   time.Time               `json:"saved"`
	Sources map[string]snapshotData `json:"sources"`
}

type snapshotData struct {
	Items        []Item    `json:"items"`
	FetchedAt    time.Time `json:"fetched_at"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
}

func (c *NewsCache) saveSnapshot(path string) error {
	c.mu.RLock()
	snap := snapshotFile{Version: 1, Saved: time.Now().UTC(), Sources: make(map[string]snapshotData, len(c.sources))}
	for id, s := range c.sources {
		if len(s.Items) > 0 {
			snap.Sources[id] = snapshotData{Items: s.Items, FetchedAt: s.FetchedAt, ETag: s.ETag, LastModified: s.LastModified}
		}
	}
	c.mu.RUnlock()

	tmp, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	zw := gzip.NewWriter(tmp)
	if err := json.NewEncoder(zw).Encode(snap); err != nil {
		tmp.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (c *NewsCache) loadSnapshot(path string, cfg *Config) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	var snap snapshotFile
	if err := json.NewDecoder(io.LimitReader(zr, 256<<20)).Decode(&snap); err != nil {
		return 0, err
	}
	n := 0
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, d := range snap.Sources {
		src, ok := cfg.sourceByID(id)
		if !ok {
			continue
		}
		items := pruneOld(d.Items, time.Now().Add(-cfg.maxAge(src)))
		for i := range items {
			items[i].canon = canonicalURL(items[i].URL)
			items[i].normTitle = normalizeTitle(items[i].Title)
			setTokens(&items[i])
		}
		c.sources[id] = &SourceState{Items: items, FetchedAt: d.FetchedAt, ETag: d.ETag, LastModified: d.LastModified}
		n++
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Feed parsing: RSS 2.0, RSS 1.0 (RDF), Atom and JSON Feed.

type rawItem struct {
	Title, Link, Summary, GUID, Date, Image string
}

type xmlLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

type xmlMedia struct {
	URL    string `xml:"url,attr"`
	Type   string `xml:"type,attr"`
	Medium string `xml:"medium,attr"`
	Inner  string `xml:",innerxml"` // Atom <content> body
}

type xmlItem struct {
	Title       string     `xml:"title"`
	Links       []xmlLink  `xml:"link"`
	Description string     `xml:"description"`
	Summary     string     `xml:"summary"`
	Encoded     string     `xml:"encoded"`
	Content     []xmlMedia `xml:"content"` // atom:content or media:content
	PubDate     string     `xml:"pubDate"`
	DCDate      string     `xml:"date"`
	Published   string     `xml:"published"`
	Updated     string     `xml:"updated"`
	Issued      string     `xml:"issued"`
	GUID        string     `xml:"guid"`
	ID          string     `xml:"id"`
	About       string     `xml:"about,attr"`
	Enclosures  []xmlMedia `xml:"enclosure"`
	Thumbnails  []xmlMedia `xml:"thumbnail"`
	Groups      []struct {
		Content    []xmlMedia `xml:"content"`
		Thumbnails []xmlMedia `xml:"thumbnail"`
	} `xml:"group"`
}

type xmlDoc struct {
	XMLName xml.Name
	Channel struct {
		Items []xmlItem `xml:"item"`
	} `xml:"channel"`
	Items   []xmlItem `xml:"item"`  // RDF: items are siblings of <channel>
	Entries []xmlItem `xml:"entry"` // Atom
}

func parseFeed(body []byte, typ string) ([]rawItem, error) {
	body = bytes.TrimPrefix(bytes.TrimSpace(body), []byte("\xef\xbb\xbf"))
	if len(body) == 0 {
		return nil, errors.New("empty response")
	}
	if typ == "json" || (typ == "" && body[0] == '{') {
		return parseJSONFeed(body)
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = charsetReader
	var doc xmlDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var items []xmlItem
	isAtom := false
	switch strings.ToLower(doc.XMLName.Local) {
	case "rss":
		items = doc.Channel.Items
	case "rdf":
		items = doc.Items
	case "feed":
		items, isAtom = doc.Entries, true
	case "html":
		return nil, errors.New("not a feed (got an HTML page)")
	default:
		return nil, fmt.Errorf("not a feed (root element <%s>)", doc.XMLName.Local)
	}
	out := make([]rawItem, 0, len(items))
	for _, x := range items {
		r := rawItem{Title: x.Title, GUID: firstNonEmpty(x.GUID, x.ID)}
		r.Link = pickLink(x.Links, isAtom)
		if r.Link == "" && x.About != "" {
			r.Link = x.About
		}
		if r.Link == "" && strings.HasPrefix(r.GUID, "http") {
			r.Link = r.GUID
		}
		r.Summary = firstNonEmpty(x.Description, x.Summary)
		if r.Summary == "" {
			r.Summary = x.Encoded
		}
		if r.Summary == "" {
			for _, c := range x.Content {
				if c.URL == "" && strings.TrimSpace(c.Inner) != "" {
					r.Summary = html.UnescapeString(c.Inner) // raw innerxml: entities still escaped
					break
				}
			}
		}
		r.Date = firstNonEmpty(x.PubDate, x.DCDate, x.Published, x.Updated, x.Issued)
		r.Image = pickImage(x)
		out = append(out, r)
	}
	return out, nil
}

func pickLink(links []xmlLink, atom bool) string {
	for _, l := range links {
		if t := strings.TrimSpace(l.Text); t != "" && l.Href == "" {
			return t
		}
	}
	for _, l := range links {
		if l.Href != "" && (l.Rel == "" || l.Rel == "alternate") {
			return strings.TrimSpace(l.Href)
		}
	}
	if atom {
		for _, l := range links {
			if l.Href != "" && l.Rel != "self" && l.Rel != "enclosure" {
				return strings.TrimSpace(l.Href)
			}
		}
	}
	return ""
}

func pickImage(x xmlItem) string {
	isImg := func(m xmlMedia) bool {
		if m.URL == "" {
			return false
		}
		if strings.HasPrefix(m.Type, "image/") || m.Medium == "image" {
			return true
		}
		p := strings.ToLower(m.URL)
		if i := strings.IndexByte(p, '?'); i >= 0 {
			p = p[:i]
		}
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif"} {
			if strings.HasSuffix(p, ext) {
				return true
			}
		}
		return false
	}
	cands := append([]xmlMedia{}, x.Content...)
	cands = append(cands, x.Thumbnails...)
	cands = append(cands, x.Enclosures...)
	for _, g := range x.Groups {
		cands = append(cands, g.Content...)
		cands = append(cands, g.Thumbnails...)
	}
	for _, m := range cands {
		if isImg(m) {
			return strings.TrimSpace(m.URL)
		}
	}
	return ""
}

func parseJSONFeed(body []byte) ([]rawItem, error) {
	var f struct {
		Version string `json:"version"`
		Items   []struct {
			ID            any    `json:"id"`
			URL           string `json:"url"`
			ExternalURL   string `json:"external_url"`
			Title         string `json:"title"`
			Summary       string `json:"summary"`
			ContentText   string `json:"content_text"`
			ContentHTML   string `json:"content_html"`
			DatePublished string `json:"date_published"`
			DateModified  string `json:"date_modified"`
			Image         string `json:"image"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("parse JSON feed: %w", err)
	}
	if !strings.Contains(f.Version, "jsonfeed.org") && f.Items == nil {
		return nil, errors.New("not a JSON Feed")
	}
	out := make([]rawItem, 0, len(f.Items))
	for _, it := range f.Items {
		out = append(out, rawItem{
			Title:   it.Title,
			Link:    firstNonEmpty(it.URL, it.ExternalURL),
			Summary: firstNonEmpty(it.Summary, it.ContentText, it.ContentHTML),
			GUID:    fmt.Sprint(it.ID),
			Date:    firstNonEmpty(it.DatePublished, it.DateModified),
			Image:   it.Image,
		})
	}
	return out, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// charsetReader handles the non-UTF-8 encodings seen in the wild (Latin-1, CP1252).
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return in, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "iso-8859-15", "windows-1252", "cp1252":
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		var sb strings.Builder
		sb.Grow(len(b) + len(b)/8)
		for _, c := range b {
			if c >= 0x80 && c <= 0x9f {
				sb.WriteRune(cp1252[c-0x80])
			} else {
				sb.WriteRune(rune(c))
			}
		}
		return strings.NewReader(sb.String()), nil
	}
	return nil, fmt.Errorf("unsupported charset %q", label)
}

var cp1252 = [32]rune{
	'€', '\u0081', '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', '\u008d', 'Ž', '\u008f',
	'\u0090', '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', '\u009d', 'ž', 'Ÿ',
}

// ---------------------------------------------------------------------------
// Normalisation: plain text, safe URLs, dates, de-duplication.

type normOpts struct {
	now      time.Time
	known    map[string]time.Time // canonical URL -> previously assigned date
	maxAge   time.Duration
	maxItems int
	images   bool
}

func normalizeItems(sourceID, feedURL string, raws []rawItem, o normOpts) []Item {
	base, _ := url.Parse(feedURL)
	cutoff := o.now.Add(-o.maxAge)
	seenURL, seenTitle := map[string]bool{}, map[string]bool{}
	items := make([]Item, 0, len(raws))
	for _, r := range raws {
		link := safeURL(r.Link, base)
		title := truncate(plainText(r.Title), 300)
		if link == "" || title == "" {
			continue
		}
		it := Item{Source: sourceID, Title: title, URL: link, canon: canonicalURL(link), normTitle: normalizeTitle(title)}
		if seenURL[it.canon] || seenTitle[it.normTitle] {
			continue
		}
		seenURL[it.canon], seenTitle[it.normTitle] = true, true
		it.Summary = truncate(plainText(r.Summary), 300)
		if it.Summary == it.Title {
			it.Summary = ""
		}
		setTokens(&it)
		if t, ok := parseDate(r.Date); ok {
			it.Published = t
		} else if t, ok := o.known[it.canon]; ok {
			it.Published = t
		} else {
			it.Published = o.now
		}
		if it.Published.After(o.now.Add(5 * time.Minute)) {
			it.Published = fixFutureDate(it.Published, o.now, o.known[it.canon])
		}
		it.Published = it.Published.UTC().Truncate(time.Second)
		if it.Published.Before(cutoff) {
			continue
		}
		if o.images {
			if img := safeURL(r.Image, base); strings.HasPrefix(img, "https://") {
				it.Image = img
			}
		}
		sum := sha1.Sum([]byte(it.canon))
		it.ID = hex.EncodeToString(sum[:6])
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Published.After(items[j].Published) })
	if len(items) > o.maxItems {
		items = items[:o.maxItems]
	}
	return items
}

// fixFutureDate handles feeds that publish Dutch local time labelled as UTC
// ("13:31 Z" at 11:31 UTC): reinterpret the wall clock as Europe/Amsterdam.
// Otherwise keep the date assigned on an earlier fetch, so the item does not
// jump back to the top on every refresh.
func fixFutureDate(t, now, known time.Time) time.Time {
	local := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, amsterdam)
	if !local.After(now.Add(5 * time.Minute)) {
		return local
	}
	if !known.IsZero() {
		return known
	}
	return now
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// safeURL resolves raw against base and returns it only if it is http(s).
func safeURL(raw string, base *url.URL) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
}

var trackingParams = map[string]bool{"fbclid": true, "gclid": true, "mc_cid": true, "mc_eid": true, "ocid": true}

// canonicalURL drops the fragment and tracking parameters, lowercases scheme/host.
func canonicalURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	u.Fragment, u.RawFragment = "", ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if strings.HasPrefix(strings.ToLower(k), "utm_") || trackingParams[strings.ToLower(k)] {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
	}
	if len(u.Path) > 1 {
		u.Path = strings.TrimSuffix(u.Path, "/")
		u.RawPath = ""
	}
	return u.String()
}

func normalizeTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		} else {
			space = true
		}
	}
	return b.String()
}

// plainText strips all HTML and returns collapsed plain text. The output is
// still treated as untrusted text by the frontend (textContent only).
func plainText(s string) string {
	// drop bidirectional overrides/isolates: they can make a title display differently than it reads
	s = strings.Map(func(r rune) rune {
		if (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') || r == '\u200e' || r == '\u200f' {
			return -1
		}
		return r
	}, s)
	s = stripTags(s)
	s = html.UnescapeString(s)
	if strings.Contains(s, "<") { // double-escaped markup
		s = stripTags(s)
	}
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\u200b' || r == '\ufeff' || (unicode.IsControl(r))
	}), " ")
}

// blockTags separate words; inline tags (b, a, em, span…) are removed without a space.
var blockTags = map[string]bool{
	"p": true, "br": true, "div": true, "li": true, "ul": true, "ol": true, "tr": true, "td": true, "th": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "blockquote": true, "hr": true,
	"img": true, "figure": true, "figcaption": true, "section": true, "article": true, "table": true, "pre": true,
}

var rawTextTags = []string{"script", "style", "noscript", "iframe", "template", "textarea", "svg", "math", "object"}

func stripTags(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '<' || i+1 >= len(s) || !isTagStart(s[i+1]) {
			b.WriteByte(c)
			i++
			continue
		}
		rest := s[i:]
		if strings.HasPrefix(rest, "<!--") {
			end := strings.Index(rest, "-->")
			if end < 0 {
				break
			}
			i += end + 3
			continue
		}
		lower := strings.ToLower(rest[:min(len(rest), 16)])
		skipped := false
		for _, t := range rawTextTags {
			if strings.HasPrefix(lower, "<"+t) && len(lower) > len(t)+1 && !isNameChar(lower[len(t)+1]) {
				end := strings.Index(strings.ToLower(rest), "</"+t)
				if end < 0 {
					i = len(s)
				} else if gt := strings.IndexByte(rest[end:], '>'); gt >= 0 {
					i += end + gt + 1
				} else {
					i = len(s)
				}
				skipped = true
				break
			}
		}
		if skipped {
			b.WriteByte(' ')
			continue
		}
		gt := strings.IndexByte(rest, '>')
		if gt < 0 {
			break // unterminated tag: drop the remainder
		}
		name := strings.TrimLeft(lower, "</!?")
		if j := strings.IndexFunc(name, func(r rune) bool { return !isNameChar(byte(r)) }); j >= 0 {
			name = name[:j]
		}
		if blockTags[name] {
			b.WriteByte(' ')
		}
		i += gt + 1
	}
	return b.String()
}

func isTagStart(c byte) bool {
	return c == '/' || c == '!' || c == '?' || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isNameChar(c byte) bool {
	return c == '-' || (c|0x20 >= 'a' && c|0x20 <= 'z') || (c >= '0' && c <= '9')
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:-–") + "…"
}

var amsterdam = func() *time.Location {
	l, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		return time.UTC
	}
	return l
}()

var dateLayouts = []string{
	time.RFC1123Z, time.RFC1123, time.RFC3339Nano, time.RFC3339,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 Z",
	"Mon, 02 Jan 2006 15:04:05 Z", "Mon, 2 Jan 2006 15:04 -0700", "Mon, 2 Jan 2006 15:04 MST",
	"Mon, 2 January 2006 15:04:05 -0700", "Monday, 2 January 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04 -0700",
	"2006-01-02T15:04:05-0700", "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04Z07:00",
	"2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05 MST", "2006-01-02 15:04:05Z07:00",
	time.RFC850, time.RFC822Z, time.RFC822, time.ANSIC, time.UnixDate,
	"Mon, 02 Jan 06 15:04:05 -0700", "Mon, 2 Jan 06 15:04:05 -0700", // two-digit year (CISA)
	"2006-01-02T15:04:05 -0700",
}

// "2025-12-16CET13:29:41 +0100" (Drupal feeds): the zone name sits where the "T" belongs.
var zoneInDateRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})[A-Z]{3,4}(\d{2}:\d{2})`)

// layouts without zone info are interpreted as Dutch local time
var localLayouts = []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02", "02-01-2006 15:04:05"}

var zoneAbbr = map[string]string{
	"CET": "+0100", "CEST": "+0200", "MET": "+0100", "MEST": "+0200", "GMT": "+0000", "UT": "+0000", "UTC": "+0000",
	"Z": "+0000", "BST": "+0100", "EST": "-0500", "EDT": "-0400", "CST": "-0600", "CDT": "-0500",
	"PST": "-0800", "PDT": "-0700",
}

func parseDate(s string) (time.Time, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return time.Time{}, false
	}
	s = zoneInDateRe.ReplaceAllString(s, "${1}T${2}")
	// Replace a trailing zone abbreviation with a numeric offset (Go can't resolve abbreviations).
	if i := strings.LastIndexByte(s, ' '); i > 0 {
		if off, ok := zoneAbbr[strings.ToUpper(s[i+1:])]; ok {
			s = s[:i+1] + off
		}
	}
	try := func(s string) (time.Time, bool) {
		for _, l := range dateLayouts {
			if t, err := time.Parse(l, s); err == nil {
				return t, true
			}
		}
		for _, l := range localLayouts {
			if t, err := time.ParseInLocation(l, s, amsterdam); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	if t, ok := try(s); ok {
		return t, t.Year() > 1990
	}
	// Non-English weekday ("ma, 3 jun 2024 ...") — drop it and retry.
	if _, rest, ok := strings.Cut(s, ", "); ok {
		if t, ok := try(rest); ok {
			return t, t.Year() > 1990
		}
		if t, ok := try("Mon, " + rest); ok {
			return t, t.Year() > 1990
		}
	}
	return time.Time{}, false
}

// ---------------------------------------------------------------------------
// -check-feeds: verify every source and suggest alternatives for broken ones.

type checkResult struct {
	src      Source
	status   string
	items    int
	newest   time.Time
	ok       bool
	suggests []string
}

func runCheckFeeds(cfg *Config, only string) int {
	f := newFetcher(cfg.Fetch.MaxConcurrent, func() string { return cfg.Fetch.UserAgent }, func() time.Duration { return cfg.Fetch.Timeout.D() })
	want := map[string]bool{}
	for _, id := range strings.Split(only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	var srcs []Source
	for _, s := range cfg.Sources {
		if len(want) == 0 || want[s.ID] {
			srcs = append(srcs, s)
		}
	}
	// advisory feeds are RSS/Atom too; list them under category "advisory"
	for _, s := range cfg.Advisories {
		if len(want) == 0 || want[s.ID] {
			srcs = append(srcs, Source{ID: s.ID, Name: s.Name, Category: "advisory", URL: s.URL, Homepage: s.Homepage, Enabled: s.Enabled})
		}
	}
	ctx := context.Background()
	results := make([]checkResult, len(srcs))
	var wg sync.WaitGroup
	for i, s := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = checkSource(ctx, f, s)
		}()
	}
	wg.Wait()

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SOURCE\tCAT\tENABLED\tSTATUS\tITEMS\tNEWEST")
	okCount := 0
	for _, r := range results {
		en := "yes"
		if !r.src.IsEnabled() {
			en = "no"
		}
		newest := "-"
		if !r.newest.IsZero() {
			newest = r.newest.In(amsterdam).Format("2006-01-02 15:04")
		}
		if r.ok {
			okCount++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", r.src.ID, r.src.Category, en, r.status, r.items, newest)
	}
	tw.Flush()
	fmt.Printf("\n%d/%d sources OK\n", okCount, len(results))
	for _, r := range results {
		if r.ok {
			continue
		}
		fmt.Printf("\n✗ %s (%s)\n  url: %s\n", r.src.ID, r.status, r.src.URL)
		if len(r.suggests) == 0 {
			fmt.Println("  no working alternative found (autodiscovery + common paths)")
		}
		for _, s := range r.suggests {
			fmt.Println("  → try:", s)
		}
	}
	return 0
}

func checkSource(ctx context.Context, f *Fetcher, s Source) checkResult {
	r := checkResult{src: s}
	n, newest, err := probeFeed(ctx, f, s.URL, s.Type)
	r.items, r.newest = n, newest
	switch {
	case s.URL == "":
		r.status = "no url"
	case err != nil:
		r.status = err.Error()
	case n == 0:
		r.status = "0 items"
	case time.Since(newest) > 30*24*time.Hour:
		r.status = "stale (>30 days)"
	default:
		r.status, r.ok = "ok", true
	}
	if !r.ok {
		home := s.Homepage
		if home == "" && isHTTPURL(s.URL) {
			u, _ := url.Parse(s.URL)
			home = u.Scheme + "://" + u.Host + "/"
		}
		if home != "" {
			r.suggests = discoverFeeds(ctx, f, home, s.URL)
		}
	}
	return r
}

func probeFeed(ctx context.Context, f *Fetcher, u, typ string) (int, time.Time, error) {
	if u == "" {
		return 0, time.Time{}, errors.New("no url")
	}
	resp, err := f.Do(ctx, FetchReq{URL: u, Accept: feedAccept})
	if err != nil {
		return 0, time.Time{}, err
	}
	raws, err := parseFeed(resp.Body, typ)
	if err != nil {
		return 0, time.Time{}, err
	}
	var newest time.Time
	n := 0
	for _, r := range raws {
		if r.Link == "" || r.Title == "" {
			continue
		}
		n++
		if t, ok := parseDate(r.Date); ok && t.After(newest) {
			newest = t
		}
	}
	return n, newest, nil
}

var (
	linkTagRe = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attrRe    = regexp.MustCompile(`(?is)\b(rel|type|href)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
)

// discoverFeeds looks for <link rel="alternate"> feed tags on the homepage and
// tries common feed paths. It only reads <link> tags, never page content.
func discoverFeeds(ctx context.Context, f *Fetcher, home, skip string) []string {
	base, err := url.Parse(home)
	if err != nil {
		return nil
	}
	var cands []string
	if resp, err := f.Do(ctx, FetchReq{URL: home, Accept: "text/html"}); err == nil {
		for _, tag := range linkTagRe.FindAllString(string(resp.Body), 200) {
			attrs := map[string]string{}
			for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
				attrs[strings.ToLower(m[1])] = html.UnescapeString(m[2] + m[3] + m[4])
			}
			t := strings.ToLower(attrs["type"])
			if strings.Contains(strings.ToLower(attrs["rel"]), "alternate") &&
				(strings.Contains(t, "rss") || strings.Contains(t, "atom") || strings.Contains(t, "feed+json")) {
				if u := safeURL(attrs["href"], base); u != "" {
					cands = append(cands, u)
				}
			}
		}
	}
	for _, p := range []string{"/rss", "/feed", "/feed/", "/rss.xml", "/rss/index.xml", "/feed.xml", "/atom.xml", "/index.xml"} {
		cands = append(cands, base.Scheme+"://"+base.Host+p)
	}
	var out []string
	seen := map[string]bool{skip: true}
	for _, c := range cands {
		if seen[c] || len(out) >= 5 {
			continue
		}
		seen[c] = true
		if n, newest, err := probeFeed(ctx, f, c, ""); err == nil && n > 0 {
			out = append(out, fmt.Sprintf("%s  (%d items, newest %s)", c, n, newest.In(amsterdam).Format("2006-01-02")))
		}
	}
	return out
}
