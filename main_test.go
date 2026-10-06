package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel>
  <title>Test</title>
  <atom:link href="https://example.nl/rss" rel="self" type="application/rss+xml"/>
  <item>
    <title>Kabinet &amp; Kamer eens</title>
    <link>https://example.nl/a?utm_source=rss#top</link>
    <description><![CDATA[<p>Eerste <b>alinea</b>.</p><script>alert(1)</script>]]></description>
    <pubDate>Tue, 03 Jun 2025 10:00:00 +0200</pubDate>
    <media:content url="https://img.example.nl/a.jpg" medium="image"/>
  </item>
  <item>
    <title>Tweede bericht</title>
    <link>/relative/b</link>
    <dc:date>2025-06-03T09:00:00Z</dc:date>
    <enclosure url="http://img.example.nl/b.jpg" type="image/jpeg"/>
  </item>
  <item>
    <title>Derde bericht</title>
    <link>https://example.nl/c</link>
    <pubDate>Tue, 03 Jun 2025 09:30:00 CEST</pubDate>
  </item>
  <item>
    <title>Kabinet &amp; Kamer eens!</title>
    <link>https://example.nl/a-copy</link>
    <pubDate>Tue, 03 Jun 2025 09:45:00 +0200</pubDate>
  </item>
  <item>
    <title>Gevaarlijke link</title>
    <link>javascript:alert(1)</link>
  </item>
</channel>
</rss>`

const atomFixture = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom test</title>
  <link rel="self" href="https://example.org/atom.xml"/>
  <entry>
    <title type="html">Atom &lt;em&gt;titel&lt;/em&gt;</title>
    <link rel="alternate" type="text/html" href="https://example.org/post/1"/>
    <link rel="enclosure" href="https://example.org/x.mp3"/>
    <id>tag:example.org,2025:1</id>
    <published>2025-06-03T08:00:00+02:00</published>
    <updated>2025-06-03T09:00:00+02:00</updated>
    <content type="html">&lt;p&gt;Inhoud &amp;amp; meer&lt;/p&gt;</content>
  </entry>
  <entry>
    <title>Zonder datum</title>
    <link href="https://example.org/post/2"/>
    <summary type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml">XHTML <b>samenvatting</b></div></summary>
  </entry>
</feed>`

const rdfFixture = `<?xml version="1.0" encoding="ISO-8859-1"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel rdf:about="https://example.de/"><title>RDF</title></channel>
  <item rdf:about="https://example.de/1">
    <title>M` + "\xfc" + `nchen news</title>
    <link>https://example.de/1</link>
    <description>Beschreibung</description>
    <dc:date>2025-06-03T07:00:00+00:00</dc:date>
  </item>
  <item rdf:about="https://example.de/2">
    <title>Only about</title>
  </item>
</rdf:RDF>`

var testNow = time.Date(2025, 6, 3, 12, 0, 0, 0, time.UTC)

func norm(t *testing.T, body, feedURL string) []Item {
	t.Helper()
	raws, err := parseFeed([]byte(body), "")
	if err != nil {
		t.Fatalf("parseFeed: %v", err)
	}
	return normalizeItems("src", feedURL, raws, normOpts{now: testNow, maxAge: 72 * time.Hour, maxItems: 50, images: true})
}

func TestParseRSS(t *testing.T) {
	items := norm(t, rssFixture, "https://example.nl/rss")
	by := map[string]Item{}
	for _, it := range items {
		by[it.Title] = it
	}
	// javascript: link dropped; "Kabinet & Kamer eens!" is a near-duplicate title in the same source.
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d: %+v", len(items), items)
	}
	a, ok := by["Kabinet & Kamer eens"]
	if !ok || a.Summary != "Eerste alinea." {
		t.Errorf("title/summary not plain text: %+v", a)
	}
	if a.Image != "https://img.example.nl/a.jpg" {
		t.Errorf("image: %q", a.Image)
	}
	if !a.Published.Equal(time.Date(2025, 6, 3, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("date: %v", a.Published)
	}
	if c := by["Derde bericht"]; !c.Published.Equal(time.Date(2025, 6, 3, 7, 30, 0, 0, time.UTC)) {
		t.Errorf("CEST date: %v", c.Published)
	}
	b := by["Tweede bericht"]
	if b.URL != "https://example.nl/relative/b" {
		t.Errorf("relative link not resolved: %q", b.URL)
	}
	if b.Image != "" {
		t.Errorf("http image must be dropped, got %q", b.Image)
	}
	if items[0].Title != "Tweede bericht" || items[2].Title != "Derde bericht" {
		t.Errorf("not sorted newest first: %v, %v, %v", items[0].Title, items[1].Title, items[2].Title)
	}
}

func TestParseAtom(t *testing.T) {
	items := norm(t, atomFixture, "https://example.org/atom.xml")
	if len(items) != 2 {
		t.Fatalf("want 2, got %d", len(items))
	}
	var first, second Item
	for _, it := range items {
		if strings.HasSuffix(it.URL, "/1") {
			first = it
		} else {
			second = it
		}
	}
	if first.Title != "Atom titel" || first.Summary != "Inhoud & meer" {
		t.Errorf("atom html: %q / %q", first.Title, first.Summary)
	}
	if !first.Published.Equal(time.Date(2025, 6, 3, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("atom published: %v", first.Published)
	}
	if !second.Published.Equal(testNow) {
		t.Errorf("dateless item should get fetch time, got %v", second.Published)
	}
}

func TestParseRDFLatin1(t *testing.T) {
	items := norm(t, rdfFixture, "https://example.de/")
	if len(items) != 2 {
		t.Fatalf("want 2, got %d", len(items))
	}
	if items[0].Title != "München news" && items[1].Title != "München news" {
		t.Errorf("latin-1 not decoded: %q / %q", items[0].Title, items[1].Title)
	}
	for _, it := range items {
		if it.Title == "Only about" && it.URL != "https://example.de/2" {
			t.Errorf("rdf:about fallback: %q", it.URL)
		}
	}
}

func TestParseJSONFeed(t *testing.T) {
	body := `{"version":"https://jsonfeed.org/version/1.1","items":[{"id":"1","url":"https://j.example/1","title":"JSON <i>item</i>","content_html":"<p>Hallo</p>","date_published":"2025-06-03T10:00:00Z"}]}`
	items := norm(t, body, "https://j.example/feed.json")
	if len(items) != 1 || items[0].Title != "JSON item" || items[0].Summary != "Hallo" {
		t.Fatalf("json feed: %+v", items)
	}
}

func TestNotAFeed(t *testing.T) {
	if _, err := parseFeed([]byte("<!doctype html><html><body>hi</body></html>"), ""); err == nil {
		t.Error("HTML page must not parse as a feed")
	}
}

func TestPlainTextMalicious(t *testing.T) {
	cases := map[string]string{
		`<img src=x onerror=alert(1)>Tekst`:                         "Tekst",
		`<script>alert("x")</script>Veilig`:                         "Veilig",
		`<SCRIPT type="text/javascript">evil()</SCRIPT>ok`:          "ok",
		`&lt;script&gt;alert(1)&lt;/script&gt;dubbel`:               "dubbel",
		`<style>body{display:none}</style><p>Na stijl</p>`:          "Na stijl",
		`<!-- comment <b>x</b> -->zichtbaar`:                        "zichtbaar",
		`<a href="javascript:alert(1)">klik</a> hier`:               "klik hier",
		`Ik <3 feeds & 2 < 3`:                                       "Ik <3 feeds & 2 < 3",
		`<iframe src="https://evil"></iframe>na`:                    "na",
		`<svg><script>alert(1)</script></svg>svg weg`:               "svg weg",
		`onvolledig <img src="x" onerror="alert(1)"`:                "onvolledig",
		"regel\u200b met\u00a0nbsp &nbsp; en &amp;amp; &#39;x&#39;": "regel met nbsp en &amp; 'x'",
	}
	for in, want := range cases {
		if got := plainText(in); got != want {
			t.Errorf("plainText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	s := strings.Repeat("woord ", 100)
	got := truncate(s, 300)
	if len([]rune(got)) > 301 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncate: %d runes, %q", len([]rune(got)), got[len(got)-10:])
	}
	if truncate("kort", 300) != "kort" {
		t.Error("short strings unchanged")
	}
}

func TestCanonicalURL(t *testing.T) {
	cases := map[string]string{
		"https://Example.NL/a/?utm_source=x&utm_medium=y#frag": "https://example.nl/a",
		"https://example.nl/a?id=5&fbclid=abc":                 "https://example.nl/a?id=5",
		"https://example.nl/":                                  "https://example.nl/",
	}
	for in, want := range cases {
		if got := canonicalURL(in); got != want {
			t.Errorf("canonicalURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeDedup(t *testing.T) {
	mk := func(src, title, u string, minsAgo int) Item {
		return Item{Source: src, Title: title, URL: u, canon: canonicalURL(u), normTitle: normalizeTitle(title),
			Published: testNow.Add(-time.Duration(minsAgo) * time.Minute)}
	}
	a := []Item{
		mk("nos-algemeen", "Nieuws één", "https://nos.nl/1?utm_source=rss", 10),
		mk("nos-algemeen", "Oud", "https://nos.nl/old", 500),
	}
	b := []Item{
		mk("nos-binnenland", "Nieuws één", "https://nos.nl/1", 10),      // same URL, other source
		mk("nos-binnenland", "Iets anders!", "https://nos.nl/2", 5),     // newest
		mk("nos-binnenland", "iets  ANDERS", "https://nos.nl/2-dup", 6), // same title, same source
		mk("nu-algemeen", "Iets anders", "https://nu.nl/2", 7),          // same title, other source: keep
	}
	got := mergeItems([][]Item{a, b}, time.Time{}, 60, nil)
	var urls []string
	for _, it := range got {
		urls = append(urls, it.URL)
	}
	want := "https://nos.nl/2 https://nu.nl/2 https://nos.nl/1?utm_source=rss https://nos.nl/old"
	if strings.Join(urls, " ") != want && strings.Join(urls, " ") != strings.Replace(want, "https://nos.nl/1?utm_source=rss", "https://nos.nl/1", 1) {
		t.Errorf("merge order/dedup:\n got %v\nwant %v", urls, want)
	}
	if n := len(mergeItems([][]Item{a, b}, testNow.Add(-8*time.Minute), 60, nil)); n != 2 {
		t.Errorf("since filter: want 2, got %d", n)
	}
	if n := len(mergeItems([][]Item{a, b}, time.Time{}, 1, nil)); n != 1 {
		t.Errorf("limit: want 1, got %d", n)
	}
}

func TestParseDate(t *testing.T) {
	want := time.Date(2025, 6, 3, 8, 0, 0, 0, time.UTC)
	for _, s := range []string{
		"Tue, 03 Jun 2025 10:00:00 +0200",
		"Tue, 3 Jun 2025 10:00:00 CEST",
		"2025-06-03T08:00:00Z",
		"2025-06-03T10:00:00+02:00",
		"2025-06-03 10:00:00",            // no zone → Amsterdam
		"di, 03 jun 2025 10:00:00 +0200", // Dutch weekday
		"Tue, 03 Jun 2025 08:00:00 GMT",
		"Tue, 03 Jun 25 08:00:00 +0000", // two-digit year
		"2025-06-03CEST10:00:00 +0200",  // zone name instead of "T"
	} {
		got, ok := parseDate(s)
		if !ok || !got.Equal(want) {
			t.Errorf("parseDate(%q) = %v, %v", s, got, ok)
		}
	}
	if _, ok := parseDate("gisteren"); ok {
		t.Error("garbage must not parse")
	}
}

func TestFutureDateFix(t *testing.T) {
	now := time.Date(2026, 9, 24, 11, 35, 0, 0, time.UTC)
	// Feed writes 13:31 local time as "13:31 Z".
	got := fixFutureDate(time.Date(2026, 9, 24, 13, 31, 0, 0, time.UTC), now, time.Time{})
	if !got.Equal(time.Date(2026, 9, 24, 11, 31, 0, 0, time.UTC)) {
		t.Errorf("local-as-UTC not fixed: %v", got)
	}
	known := now.Add(-time.Hour)
	if got := fixFutureDate(now.Add(24*time.Hour), now, known); !got.Equal(known) {
		t.Errorf("far future should keep known date, got %v", got)
	}
}

const validConfig = `
fetch: { user_agent: "Test/1.0 (test@example.nl)" }
categories:
  - { id: nl, name: "NL" }
sources:
  - { id: a, name: "A", category: nl, url: "https://a.example/rss" }
`

func TestConfigValid(t *testing.T) {
	c, err := parseConfig([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if c.Fetch.DefaultInterval.D() != 15*time.Minute || c.Server.BasePath != "/" || len(c.trusted) != 2 {
		t.Errorf("defaults not applied: %+v", c.Fetch)
	}
}

func TestConfigInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown field":     validConfig + "\nbogus: 1\n",
		"bad duration":      validConfig + "\ncache: { max_age: soon }\n",
		"duplicate id":      validConfig + `  - { id: a, name: "A2", category: nl, url: "https://a.example/2" }` + "\n",
		"unknown category":  validConfig + `  - { id: b, name: "B", category: xx, url: "https://b.example/" }` + "\n",
		"bad id":            validConfig + `  - { id: "B C", name: "B", category: nl, url: "https://b.example/" }` + "\n",
		"non-http url":      validConfig + `  - { id: b, name: "B", category: nl, url: "file:///etc/passwd" }` + "\n",
		"short interval":    validConfig + `  - { id: b, name: "B", category: nl, url: "https://b.example/", interval: 10s }` + "\n",
		"bad max_age":       validConfig + `  - { id: b, name: "B", category: nl, url: "https://b.example/", max_age: 10m }` + "\n",
		"bad proxy":         validConfig + "\nserver: { trusted_proxies: [nope] }\n",
		"bad lat":           validConfig + "\nweather: { location: { name: x, lat: 123, lon: 5 } }\n",
		"refresh too fast":  validConfig + "\nrefresh: { news: 10s }\n",
		"refresh unknown":   validConfig + "\nrefresh: { nieuws: 5m }\n",
		"breaches too fast": validConfig + "\nbreaches: { interval: 10m }\n",
		"breaches http":     validConfig + "\nbreaches: { url: \"http://example.com/b\" }\n",
		"energy too fast":   validConfig + "\nenergy: { interval: 5m }\n",
		"energy bad vat":    validConfig + "\nenergy: { vat: 21 }\n",
		"air too fast":      validConfig + "\nair: { interval: 1m }\n",
		"trains http":       validConfig + "\ntrains: { url: \"http://x.example/\" }\n",
		"politics too fast": validConfig + "\npolitics: { interval: 1m }\n",
		"ransomware bad cc": validConfig + "\nransomware: { countries: [Nederland] }\n",
		"ransomware no cc":  validConfig + "\nransomware: { countries: [] }\n",
	}
	for name, y := range cases {
		if _, err := parseConfig([]byte(y)); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	// A disabled source may have an empty URL.
	ok := validConfig + `  - { id: b, name: "B", category: nl, url: "", enabled: false }` + "\n"
	if _, err := parseConfig([]byte(ok)); err != nil {
		t.Errorf("disabled source without url: %v", err)
	}
}

func TestRefreshConfig(t *testing.T) {
	c, err := parseConfig([]byte(validConfig + "\nrefresh: { news: 10m, alarms: 1m }\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := refreshSeconds(c.Refresh)
	if r["news"] != 600 || r["alarms"] != 60 || r["weather"] != 900 || len(r) != len(defaultRefresh) {
		t.Errorf("refresh: %v", r)
	}
	// The shipped template must parse, and keep its intervals sensible.
	b, err := os.ReadFile("config.yaml.default")
	if err != nil {
		t.Fatal(err)
	}
	c, err = parseConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, cat := range c.Categories {
		if cat.NameEN == "" && cat.Name != "Sport" && cat.ID != "tech" {
			t.Errorf("category %s has no name_en", cat.ID)
		}
	}
}

func TestConfigWarnings(t *testing.T) {
	c, err := parseConfig([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	c.Fetch.UserAgent = "Nieuwsdashboard/1.0 (+https://nieuws.digibaro.net; beheer@digibaro.net)"
	warn, info := configWarnings(c)
	if len(warn) != 1 || !strings.Contains(warn[0], "NS API key") || len(info) != 1 || !strings.Contains(info[0], "abuse.ch") {
		t.Errorf("defaults: warn %q info %q", warn, info)
	}
	c.Keys.NSAPIKey, c.Keys.AbusechAuthKey = "k", "k"
	c.Fetch.UserAgent = "Nieuwsdashboard/1.0 (+https://example.nl; contact@example.nl)"
	warn, info = configWarnings(c)
	if len(warn) != 1 || !strings.Contains(warn[0], "user_agent") || len(info) != 0 {
		t.Errorf("example user agent: warn %q info %q", warn, info)
	}
	c.Fetch.UserAgent = "Nieuwsdashboard/1.0 (+https://nieuws.digibaro.net; beheer@digibaro.net)"
	c.Trains.Enabled = false
	c.Keys.NSAPIKey = ""
	if warn, _ = configWarnings(c); len(warn) != 0 {
		t.Errorf("trains disabled, own user agent: %q", warn)
	}
}

func TestBasePathNormalised(t *testing.T) {
	c, err := parseConfig([]byte(validConfig + "\nserver: { base_path: nieuws }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.BasePath != "/nieuws/" {
		t.Errorf("base path: %q", c.Server.BasePath)
	}
}

// ---------------------------------------------------------------------------
// Weather

func TestWMODescriptions(t *testing.T) {
	cases := []struct {
		code int
		day  bool
		desc string
		icon string
	}{
		{0, true, "Zonnig", "sun"}, {0, false, "Helder", "moon"}, {2, true, "Half bewolkt", "partly-day"},
		{3, true, "Bewolkt", "cloud"}, {45, true, "Mist", "fog"}, {53, true, "Motregen", "drizzle"},
		{61, true, "Lichte regen", "rain"}, {65, true, "Zware regen", "rain"}, {67, true, "IJzel", "sleet"},
		{73, true, "Sneeuw", "snow"}, {81, true, "Regenbuien", "showers"}, {95, true, "Onweer", "thunder"},
		{99, true, "Zwaar onweer met hagel", "thunder"}, {1234, true, "Onbekend", "cloud"},
	}
	for _, c := range cases {
		if d, i := wmoDesc(c.code, c.day); d != c.desc || i != c.icon {
			t.Errorf("wmoDesc(%d,%v) = %q,%q want %q,%q", c.code, c.day, d, i, c.desc, c.icon)
		}
	}
}

func TestBeaufort(t *testing.T) {
	// km/h → Bft, around the KNMI m/s boundaries
	cases := map[float64]int{0: 0, 1: 0, 1.1: 1, 5.7: 1, 5.8: 2, 12: 2, 12.3: 3, 19.7: 3, 19.8: 4, 28.7: 4,
		28.8: 5, 38.8: 5, 38.9: 6, 49.9: 6, 50.1: 7, 61.9: 7, 62: 8, 74.8: 8, 75: 9, 88.1: 9, 88.2: 10,
		102.4: 10, 102.7: 11, 117.7: 11, 117.8: 12, 200: 12}
	for kmh, want := range cases {
		if got := beaufort(kmh); got != want {
			t.Errorf("beaufort(%v km/h) = %d, want %d", kmh, got, want)
		}
	}
	if bftNames[4] != "matig" || bftNames[7] != "hard" || bftNames[9] != "storm" {
		t.Error("Beaufort names")
	}
}

func TestWindDir(t *testing.T) {
	cases := map[float64]string{0: "N", 11: "N", 12: "NNO", 45: "NO", 90: "O", 135: "ZO", 180: "Z", 225: "ZW",
		270: "W", 315: "NW", 337: "NNW", 349: "N", 360: "N"}
	for deg, want := range cases {
		if got := windDir(deg); got != want {
			t.Errorf("windDir(%v) = %s, want %s", deg, got, want)
		}
	}
}

func rainBody(vals ...int) string {
	var b strings.Builder
	for i, v := range vals {
		m := 5 + i*5
		fmt.Fprintf(&b, "%03d|%02d:%02d\r\n", v, 14+m/60, m%60)
	}
	return b.String()
}

func TestRainParsingAndSummary(t *testing.T) {
	dry := make([]int, 24)
	r, err := parseRainText(rainBody(dry...))
	if err != nil || len(r.Points) != 24 || r.Summary != "Droog tot 16:00." {
		t.Fatalf("dry: %v %+v", err, r)
	}
	// 77 ≈ 0.1 mm/h, 109 = 1 mm/h, 141 = 10 mm/h
	if r, err := parseRainText(rainBody(109, 141, 0, 0, 0, 0)); err != nil || r.Points[0].MMH != 1 || r.Points[1].MMH != 10 {
		t.Errorf("intensity formula: %+v", r.Points)
	}
	later := append(make([]int, 6), 100, 120, 100, 0)
	later = append(later, make([]int, 14)...)
	if r, _ := parseRainText(rainBody(later...)); r.Summary != "Matige regen tussen 14:35 en 14:50." {
		t.Errorf("later: %q", r.Summary)
	}
	now := []int{90, 90, 90, 0}
	now = append(now, make([]int, 20)...)
	if r, _ := parseRainText(rainBody(now...)); r.Summary != "Lichte regen, droog vanaf 14:20." {
		t.Errorf("now: %q", r.Summary)
	}
	all := make([]int, 24)
	for i := range all {
		all[i] = 150
	}
	if r, _ := parseRainText(rainBody(all...)); r.Summary != "Zware regen tot minstens 16:00." {
		t.Errorf("all: %q", r.Summary)
	}
	start := append(make([]int, 20), 110, 110, 110, 110)
	if r, _ := parseRainText(rainBody(start...)); r.Summary != "Matige regen vanaf 15:45." {
		t.Errorf("start: %q", r.Summary)
	}
	if _, err := parseRainText("<html>error</html>"); err == nil {
		t.Error("garbage must fail")
	}
}

// Mocked MeteoAlarm feed for the Netherlands (format as served by feeds.meteoalarm.org).
const meteoAlarmFixture = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:cap="urn:oasis:names:tc:emergency:cap:1.2">
  <entry>
    <cap:areaDesc>Utrecht</cap:areaDesc>
    <cap:event>Wind</cap:event>
    <cap:severity>Severe</cap:severity>
    <cap:onset>2026-09-24T10:00:00+00:00</cap:onset>
    <cap:expires>2026-09-24T20:00:00+00:00</cap:expires>
    <cap:message_type>Alert</cap:message_type>
    <link title="Utrecht" href="https://meteoalarm.org?geocode=EMMA_ID:NL010" hreflang="en"/>
    <title>Orange Wind Warning issued for Netherlands - Utrecht</title>
  </entry>
  <entry>
    <cap:areaDesc>Fryslân</cap:areaDesc>
    <cap:event>thunderstorm</cap:event>
    <cap:severity>Moderate</cap:severity>
    <cap:onset>2026-09-24T12:00:00+00:00</cap:onset>
    <cap:expires>2026-09-24T18:00:00+00:00</cap:expires>
    <title>Yellow Thunderstorm Warning issued for Netherlands - Fryslân</title>
  </entry>
  <entry>
    <cap:areaDesc>Zeeland</cap:areaDesc>
    <cap:event>fog</cap:event>
    <cap:severity>Moderate</cap:severity>
    <cap:expires>2026-09-24T06:00:00+00:00</cap:expires>
    <title>Yellow Fog Warning issued for Netherlands - Zeeland</title>
  </entry>
  <entry>
    <cap:areaDesc>Limburg</cap:areaDesc>
    <cap:severity>Extreme</cap:severity>
    <cap:message_type>Cancel</cap:message_type>
    <title>Red Rain Warning issued for Netherlands - Limburg</title>
  </entry>
</feed>`

func TestMeteoAlarmParsing(t *testing.T) {
	now := time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	ws, err := parseMeteoAlarm([]byte(meteoAlarmFixture), "nl", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 {
		t.Fatalf("want 2 active warnings (expired + cancelled dropped), got %d: %+v", len(ws), ws)
	}
	if w := ws[0]; w.Level != "orange" || w.Type != "Wind" || w.Area != "Utrecht" || w.URL == "" || !w.Expires.Equal(now.Add(9*time.Hour)) {
		t.Errorf("first: %+v", w)
	}
	if w := ws[1]; w.Level != "yellow" || w.Type != "Onweer" {
		t.Errorf("second: %+v", w)
	}
	if !regionMatches("Utrecht", "Utrecht") || !regionMatches("Fryslân", "Friesland") || regionMatches("Zeeland", "Utrecht") || regionMatches("Utrecht", "") {
		t.Error("regionMatches")
	}
}

func TestCountriesFor(t *testing.T) {
	cases := []struct {
		lat, lon float64
		cc, want string
	}{
		{52.09, 5.12, "", "nl"}, {50.85, 4.35, "", "be"}, {51.2, 4.4, "", "nl,be"}, {48.85, 2.35, "", ""},
		{50.85, 5.69, "", "nl,be"}, {51.44, 3.57, "", "nl"}, {52.09, 5.12, "NL", "nl"}, {51.2, 4.4, "BE", "be"}, {52.5, 6.9, "DE", ""},
	}
	for _, c := range cases {
		if got := strings.Join(countriesFor(c.lat, c.lon, c.cc), ","); got != c.want {
			t.Errorf("countriesFor(%v,%v,%q) = %q, want %q", c.lat, c.lon, c.cc, got, c.want)
		}
	}
}

func TestTTLCacheStaleFallback(t *testing.T) {
	c := newTTLCache[int](2)
	calls := 0
	ok := func() (int, error) { calls++; return calls, nil }
	fail := func() (int, error) { calls++; return 0, errors.New("down") }
	if v, _, _, _ := c.get("a", time.Hour, ok); v != 1 {
		t.Fatal("first fetch")
	}
	if v, _, _, _ := c.get("a", time.Hour, ok); v != 1 || calls != 1 {
		t.Fatal("cached value expected")
	}
	if v, _, stale, err := c.get("a", 0, fail); err != nil || !stale || v != 1 {
		t.Fatalf("stale fallback: %v %v %v", v, stale, err)
	}
	if _, _, _, err := c.get("b", 0, fail); err == nil {
		t.Fatal("no fallback without earlier value")
	}
	c.get("c", time.Hour, ok)
	c.get("d", time.Hour, ok)
	if len(c.m) > 2 {
		t.Errorf("eviction: %d entries", len(c.m))
	}
}

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(60, 3)
	ip := netip.MustParseAddr("192.0.2.1")
	for i := 0; i < 3; i++ {
		if !l.allow(ip) {
			t.Fatalf("burst request %d denied", i)
		}
	}
	if l.allow(ip) {
		t.Error("4th request in burst must be denied")
	}
	if !l.allow(netip.MustParseAddr("192.0.2.2")) {
		t.Error("other IP must have its own bucket")
	}
}

// ---------------------------------------------------------------------------
// Advisories (NCSC [kans/schade])

func TestNCSCTitleParsing(t *testing.T) {
	cases := []struct {
		title                            string
		id, ver, prob, impact, rest, sev string
		ok                               bool
	}{
		{"NCSC-2026-0123 [1.00] [M/H] Kwetsbaarheden verholpen in Microsoft Windows",
			"NCSC-2026-0123", "1.00", "M", "H", "Kwetsbaarheden verholpen in Microsoft Windows", "high", true},
		{"NCSC-2026-0389 [1.01] [M/H]  Kwetsbaarheid verholpen in WordPress", // double space, version bump
			"NCSC-2026-0389", "1.01", "M", "H", "Kwetsbaarheid verholpen in WordPress", "high", true},
		{"NCSC-2026-0386 [1.01] [H/H] Kwetsbaarheid verholpen in F5 Networks BIG-IP Access Policy Manager",
			"NCSC-2026-0386", "1.01", "H", "H", "Kwetsbaarheid verholpen in F5 Networks BIG-IP Access Policy Manager", "critical", true},
		{"NCSC-2025-0007 [2.03] [L/L] Kwetsbaarheden verholpen in Cisco IOS XE",
			"NCSC-2025-0007", "2.03", "L", "L", "Kwetsbaarheden verholpen in Cisco IOS XE", "low", true},
		{"  NCSC-2024-1001 [1.00] [H/M] Kwetsbaarheden verholpen in  Oracle Database Server ",
			"NCSC-2024-1001", "1.00", "H", "M", "Kwetsbaarheden verholpen in Oracle Database Server", "high", true},
		{"Kwetsbaarheden verholpen in Apple iOS", "", "", "", "", "", "", false},   // no NCSC prefix
		{"NCSC-2026-0001 [1.00] [X/H] Iets anders", "", "", "", "", "", "", false}, // invalid rating
	}
	for _, c := range cases {
		id, ver, p, i, rest, ok := ncscTitle(c.title)
		if ok != c.ok || id != c.id || ver != c.ver || p != c.prob || i != c.impact || rest != c.rest {
			t.Errorf("ncscTitle(%q) = %q %q %q %q %q %v", c.title, id, ver, p, i, rest, ok)
			continue
		}
		if ok && ncscSeverity(p, i) != c.sev {
			t.Errorf("severity(%s/%s) = %s, want %s", p, i, ncscSeverity(p, i), c.sev)
		}
	}
}

func TestNCSCSeverityMatrix(t *testing.T) {
	want := map[string]string{
		"H/H": "critical", "M/H": "high", "H/M": "high", "M/M": "medium", "L/H": "medium", "H/L": "medium",
		"L/M": "low", "M/L": "low", "L/L": "low", "?/H": "unknown",
	}
	for k, w := range want {
		p, i, _ := strings.Cut(k, "/")
		if got := ncscSeverity(p, i); got != w {
			t.Errorf("%s → %s, want %s", k, got, w)
		}
	}
}

func TestNormalizeAdvisories(t *testing.T) {
	src := AdvisorySource{ID: "ncsc", Format: "ncsc", URL: "https://advisories.ncsc.nl/rss/advisories"}
	raws := []rawItem{
		{Title: "NCSC-2026-0389 [1.01] [M/H]  Kwetsbaarheid verholpen in WordPress", Link: "https://advisories.ncsc.nl/advisory?id=NCSC-2026-0389",
			Summary: "Kwetsbaarheid CVE-2026-87902 verholpen. **Update:** Openbare bronnen melden dat er misbruik van de kwetsbaarheid is waargenomen.",
			Date:    "Thu, 24 Sep 2026 09:14:33 +0000"},
		{Title: "NCSC-2026-0389 [1.00] [M/H] Kwetsbaarheid verholpen in WordPress", Link: "https://advisories.ncsc.nl/advisory?id=NCSC-2026-0389",
			Date: "Wed, 23 Sep 2026 09:00:00 +0000"},
		{Title: "NCSC-2026-0392 [1.00] [M/M] Kwetsbaarheden verholpen in IBM Langflow OSS en IBM MQ Appliance",
			Link: "https://advisories.ncsc.nl/advisory?id=NCSC-2026-0392", Summary: "cve-2026-1111, CVE-2026-2222 en CVE-2026-1111.",
			Date: "Thu, 24 Sep 2026 06:00:00 +0000"},
	}
	out := normalizeAdvisories(src, raws, time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	if len(out) != 2 {
		t.Fatalf("want 2 advisories (versions merged), got %d", len(out))
	}
	wp := out[0]
	if wp.ID != "NCSC-2026-0389" || wp.Version != "1.01" || wp.Updated == nil || !wp.Exploited || wp.Severity != "high" ||
		strings.Join(wp.CVEs, ",") != "CVE-2026-87902" || strings.Join(wp.Products, "|") != "WordPress" {
		t.Errorf("wordpress: %+v", wp)
	}
	ibm := out[1]
	if ibm.Updated != nil || ibm.Exploited || strings.Join(ibm.CVEs, ",") != "CVE-2026-1111,CVE-2026-2222" ||
		strings.Join(ibm.Products, "|") != "IBM Langflow OSS|IBM MQ Appliance" || ibm.Severity != "medium" {
		t.Errorf("ibm: %+v", ibm)
	}
	// generic RSS advisory: keyword severity, id from guid
	gen := normalizeAdvisories(AdvisorySource{ID: "cert-eu", Format: "rss", URL: "https://cert.europa.eu/"},
		[]rawItem{{Title: "Critical Vulnerabilities in Fortinet Products", Link: "/publications/2026-101", GUID: "2026-101"}}, time.Now())
	if len(gen) != 1 || gen[0].Severity != "critical" || gen[0].ID != "2026-101" || gen[0].URL != "https://cert.europa.eu/publications/2026-101" {
		t.Errorf("generic: %+v", gen)
	}
	if keywordSeverity("Hoog risico op misbruik") != "high" || keywordSeverity("Update for Firefox") != "unknown" || keywordSeverity("Following up") != "unknown" {
		t.Error("keywordSeverity")
	}
}

// ---------------------------------------------------------------------------
// Threat intelligence

func TestParseTopPorts(t *testing.T) {
	body := `{"0":{"rank":2,"targetport":22,"records":500,"targets":10,"sources":300},
	          "1":{"rank":1,"targetport":23,"records":900,"targets":12,"sources":800},
	          "2":{"rank":3,"targetport":61234,"records":10,"targets":1,"sources":2},
	          "limit":10,"date":"2026-09-24"}`
	p, err := parseTopPorts([]byte(body))
	if err != nil || len(p) != 3 {
		t.Fatalf("%v %+v", err, p)
	}
	if p[0].Port != 23 || p[0].Service != "telnet" || p[1].Service != "ssh" || p[2].Service != "" || p[0].Sources != 800 {
		t.Errorf("order/services: %+v", p)
	}
	if p, _ := parseTopPorts([]byte(`{"limit":10}`)); len(p) != 0 {
		t.Error("empty day must yield no ports (triggers fallback to yesterday)")
	}
	if p, err := parseTopPorts([]byte(`[{"rank":1,"targetport":3389,"records":5,"targets":1,"sources":1}]`)); err != nil || p[0].Service != "rdp" {
		t.Errorf("array form: %v %+v", err, p)
	}
}

func TestParseDaily(t *testing.T) {
	body := `[{"date":"2026-09-21","records":1,"sources":100,"targets":1},{"date":"2026-09-23","records":1,"sources":150,"targets":1},
	          {"date":"2026-09-22","records":1,"sources":200,"targets":1},{"date":"2026-09-24","records":1,"sources":40,"targets":1}]`
	d, err := parseDaily([]byte(body), "2026-09-24")
	if err != nil {
		t.Fatal(err)
	}
	if d.Min != 100 || d.Max != 200 || d.Avg != 150 || d.Last.Date != "2026-09-23" || d.LastVsAvg != 0 || d.Today.Sources != 40 {
		t.Errorf("stats: min %d max %d avg %d last %+v vs %v today %+v", d.Min, d.Max, d.Avg, d.Last, d.LastVsAvg, d.Today)
	}
	if d.Days[0].Date != "2026-09-21" || d.Days[3].Date != "2026-09-24" {
		t.Error("days must be sorted")
	}
	if _, err := parseDaily([]byte(`[{"date":"2026-09-24","sources":40}]`), "2026-09-24"); err == nil {
		t.Error("only a partial day must be an error")
	}
}

func TestIPHelpers(t *testing.T) {
	if ip, ok := normIP("013.094.254.200"); !ok || ip != "13.94.254.200" {
		t.Errorf("normIP zero padding: %q", ip)
	}
	if ip, ok := normIP("000.000.000.000"); !ok || ip != "0.0.0.0" {
		t.Errorf("normIP zeros: %q", ip)
	}
	if _, ok := normIP("999.1.1.1"); ok {
		t.Error("invalid IP accepted")
	}
	for ip, want := range map[string]bool{"8.8.8.8": true, "10.1.2.3": false, "192.168.1.1": false, "127.0.0.1": false,
		"100.64.1.1": false, "169.254.1.1": false, "::1": false, "fe80::1": false, "2001:4860:4860::8888": true, "nope": false} {
		if publicIP(ip) != want {
			t.Errorf("publicIP(%s) = %v", ip, !want)
		}
	}
}

func TestGeoCacheLRU(t *testing.T) {
	g := newGeoCache(2)
	g.put("1.1.1.1", geoInfo{CC: "AU"})
	g.put("8.8.8.8", geoInfo{CC: "US"})
	g.get("1.1.1.1") // touch → 8.8.8.8 is now least recently used
	g.put("9.9.9.9", geoInfo{CC: "CH"})
	if _, ok := g.get("8.8.8.8"); ok {
		t.Error("LRU entry not evicted")
	}
	if v, ok := g.get("1.1.1.1"); !ok || v.CC != "AU" {
		t.Error("recently used entry evicted")
	}
}

func TestParseFeodoAndCountries(t *testing.T) {
	body := `[{"ip_address":"162.243.103.246","port":8080,"status":"offline","as_number":14061,"as_name":"DIGITALOCEAN-ASN","country":"US","first_seen":"2022-06-04 21:24:53","last_online":"2026-03-07","malware":"Emotet"},
	          {"ip_address":"50.16.16.211","port":443,"status":"online","as_number":14618,"as_name":"AMAZON-AES","country":"us","first_seen":"2025-12-30 13:56:31","last_online":"2026-03-12","malware":"QakBot"},
	          {"ip_address":"5.6.7.8","port":443,"status":"offline","country":"NL","last_online":"2026-03-10","malware":"<b>Pikabot</b>"}]`
	v, err := parseFeodo([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	c := v.([]FeodoC2)
	if c[0].IP != "50.16.16.211" || c[0].AS != "AS14618 AMAZON-AES" || c[0].CC != "US" || c[1].Malware != "Pikabot" {
		t.Errorf("feodo order/fields: %+v", c)
	}
	cc := countByCountry([]string{c[0].CC, c[1].CC, c[2].CC, ""})
	if cc[0].CC != "US" || cc[0].Count != 2 || len(cc) != 3 {
		t.Errorf("countries: %+v", cc)
	}
}

// ip-api batching: ≤100 IPs per request, wait for X-Ttl when X-Rl hits 0, cache results.
func TestGeolocateBatchingAndRateLimit(t *testing.T) {
	var mu sync.Mutex
	var calls []time.Time
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ips []string
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &ips)
		mu.Lock()
		calls = append(calls, time.Now())
		sizes = append(sizes, len(ips))
		mu.Unlock()
		w.Header().Set("X-Rl", "0") // window exhausted after every call
		w.Header().Set("X-Ttl", "1")
		out := make([]map[string]string, len(ips))
		for i, ip := range ips {
			out[i] = map[string]string{"status": "success", "query": ip, "countryCode": "nl", "as": "AS1 Test", "org": "Test"}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	old := ipAPIBatchURL
	ipAPIBatchURL = srv.URL + "/batch"
	defer func() { ipAPIBatchURL = old }()

	cfg, err := parseConfig([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, geo: newGeoCache(1000)}
	a.fetcher = newFetcher(4, func() string { return "test" }, func() time.Duration { return 5 * time.Second })

	var ips []string
	for i := 0; i < 150; i++ {
		ips = append(ips, fmt.Sprintf("11.0.%d.%d", i/250, i%250+1))
	}
	ips = append(ips, "10.0.0.1", "192.168.1.1") // private: never sent
	a.geolocate(context.Background(), ips)
	if len(sizes) != 2 || sizes[0] != 100 || sizes[1] != 50 {
		t.Fatalf("batch sizes: %v", sizes)
	}
	if gap := calls[1].Sub(calls[0]); gap < time.Second {
		t.Errorf("second batch did not wait for X-Ttl: gap %v", gap)
	}
	if g, ok := a.geo.get("11.0.0.5"); !ok || g.CC != "NL" || g.AS != "AS1 Test" {
		t.Errorf("cached result: %+v %v", g, ok)
	}
	if g, ok := a.geo.get("10.0.0.1"); !ok || g.CC != "" {
		t.Error("private IP should be cached as empty without a lookup")
	}
	a.geolocate(context.Background(), ips) // everything cached now
	if len(sizes) != 2 {
		t.Errorf("cached IPs were looked up again: %d calls", len(sizes))
	}
}

// ---------------------------------------------------------------------------
// Story grouping

func storyItem(src, title, summary string, minsAgo int) Item {
	it := Item{ID: src + title[:5], Source: src, Title: title, Summary: summary, URL: "https://" + src + ".example/" + normalizeTitle(title),
		Published: testNow.Add(-time.Duration(minsAgo) * time.Minute)}
	it.canon, it.normTitle = canonicalURL(it.URL), normalizeTitle(title)
	setTokens(&it)
	return it
}

func TestStoryGrouping(t *testing.T) {
	lang := map[string]string{"nos": "nl", "nu": "nl", "ad": "nl", "nd": "nl", "telegraaf": "nl", "rtl": "nl", "bbc": "en", "guardian": "en"}
	// Real headline pairs from 2026-09-24 (differently worded, same story).
	same := [][2]Item{
		{storyItem("nu", "Vijfde persoon met westnijlvirus overleden, zestien nieuwe besmettingen", "Het RIVM meldt zestien nieuwe besmettingen.", 10),
			storyItem("rtl", "RIVM meldt 16 nieuwe besmettingen westnijlvirus, vijfde persoon overleden", "", 30)},
		{storyItem("nos", "Station Amsterdam Centraal ligt volgend weekend grotendeels stil", "Door werkzaamheden rijden er zaterdag geen treinen.", 5),
			storyItem("nu", "Amsterdam Centraal ligt volgend weekend grotendeels stil door werkzaamheden", "", 20)},
		{storyItem("ad", "Oprah Winfrey komt volgend jaar naar Rotterdam", "", 5),
			storyItem("rtl", "Oprah Winfrey komt volgend jaar naar Rotterdam voor groot leiderschapsevent", "", 50)},
		{storyItem("nd", "Gemiddelde dieselprijs in EU op record van 2,23 euro per liter", "", 5),
			storyItem("nu", "Dieselprijs in Europa stijgt naar record van 2,23 euro per liter", "", 9)},
	}
	for _, p := range same {
		if !sameStory(&p[0], &p[1], lang) {
			t.Errorf("should group:\n  %s\n  %s", p[0].Title, p[1].Title)
		}
	}
	different := [][2]Item{
		{storyItem("nos", "Kabinet trekt 20 miljoen uit voor schuldhulp", "", 5), storyItem("nu", "Kabinet wil strengere regels voor asielzoekers", "", 6)},
		{storyItem("nos", "Verstappen wint in Bakoe", "", 5), storyItem("nu", "Feyenoord wint van Ajax in De Klassieker", "", 6)},
		{storyItem("bbc", "Oprah Winfrey comes to Rotterdam next year", "", 5), storyItem("ad", "Oprah Winfrey komt volgend jaar naar Rotterdam", "", 6)},           // other language
		{storyItem("ad", "Oprah Winfrey komt volgend jaar naar Rotterdam", "", 5), storyItem("rtl", "Oprah Winfrey komt volgend jaar naar Rotterdam", "", 5+40*60)}, // > 36 h apart
	}
	for _, p := range different {
		if sameStory(&p[0], &p[1], lang) {
			t.Errorf("should NOT group:\n  %s\n  %s", p[0].Title, p[1].Title)
		}
	}

	// Merge: grouped under the newest article, one per source, no chaining.
	lists := [][]Item{
		{storyItem("nos", "Station Amsterdam Centraal ligt volgend weekend grotendeels stil", "Door werkzaamheden rijden er zaterdag geen treinen.", 5)},
		{storyItem("nu", "Amsterdam Centraal ligt volgend weekend grotendeels stil door werkzaamheden", "", 20),
			storyItem("nu", "Amsterdam Centraal volgend weekend dicht: ProRail legt uit waarom", "werkzaamheden amsterdam centraal", 25)},
		{storyItem("ad", "Kabinet trekt 20 miljoen uit voor schuldhulp", "", 7)},
	}
	got := mergeItems(lists, time.Time{}, 10, lang)
	if len(got) < 3 || got[0].Source != "nos" || len(got[0].Related) != 1 || got[0].Related[0].Source != "nu" {
		t.Fatalf("grouping: %+v", got)
	}
	if n := len(mergeItems(lists, time.Time{}, 10, nil)); n != 4 {
		t.Errorf("group=0 must not group: %d items", n)
	}
}

func TestPresetValidation(t *testing.T) {
	ok := validConfig + "presets:\n  - { id: p1, name: \"P\", sources: [a] }\n  - { id: p2, name: \"R\", region: true }\n"
	if _, err := parseConfig([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	for name, y := range map[string]string{
		"unknown source": validConfig + "presets:\n  - { id: p1, name: \"P\", sources: [zzz] }\n",
		"empty preset":   validConfig + "presets:\n  - { id: p1, name: \"P\" }\n",
		"duplicate id":   validConfig + "presets:\n  - { id: p1, name: \"P\", sources: [a] }\n  - { id: p1, name: \"Q\", sources: [a] }\n",
	} {
		if _, err := parseConfig([]byte(y)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Image proxy and SSRF guard

func TestPublicIPRanges(t *testing.T) {
	for ip, want := range map[string]bool{
		"169.254.169.254": false, "0.1.2.3": false, "198.18.0.1": false, "192.0.2.10": false, "240.0.0.1": false,
		"255.255.255.255": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false, "64:ff9b::a00:1": false,
		"fc00::1": false, "fe80::1%eth0": false, "93.184.216.34": true, "2a00:1450:4001::1": true,
	} {
		if got := publicIP(ip); got != want {
			t.Errorf("publicIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestImageProxySignature(t *testing.T) {
	p := newImageProxy(func() string { return "test" })
	items := p.rewrite([]Item{{Image: "https://cdn.example/a.jpg"}, {Image: ""}})
	if !strings.HasPrefix(items[0].Image, "api/img?u=") || items[1].Image != "" {
		t.Fatalf("rewrite: %+v", items)
	}
	q := strings.SplitN(strings.TrimPrefix(items[0].Image, "api/img?"), "&", 2)
	enc, sig := strings.TrimPrefix(q[0], "u="), strings.TrimPrefix(q[1], "s=")
	if u, ok := p.verify(enc, sig); !ok || u != "https://cdn.example/a.jpg" {
		t.Errorf("valid signature rejected")
	}
	flip := "A"
	if strings.HasSuffix(sig, "A") {
		flip = "B"
	}
	if _, ok := p.verify(enc, sig[:len(sig)-1]+flip); ok {
		t.Error("tampered signature accepted")
	}
	other := newImageProxy(func() string { return "test" }) // different random key
	if _, ok := other.verify(enc, sig); ok {
		t.Error("signature from another key accepted")
	}
}

func TestImageProxyBlocksPrivateTargets(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	p := newImageProxy(func() string { return "test" })
	_, _, err := p.fetch(context.Background(), srv.URL+"/latest/meta-data")
	if err == nil || !strings.Contains(err.Error(), "blocked non-public address 127.0.0.1") {
		t.Errorf("loopback target must be blocked by the dialer, got %v", err)
	}
	if _, err := p.get(context.Background(), "https://localhost:1/x.png", netip.MustParseAddr("192.0.2.1")); err == nil {
		t.Error("localhost must be blocked")
	}
}

func TestThumbnail(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1200, 600)) // fully transparent → must become white
	src.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	_ = png.Encode(&buf, src)
	out, ct, err := thumbnail(buf.Bytes())
	if err != nil || ct != "image/jpeg" {
		t.Fatalf("%v %s", err, ct)
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 320 || img.Bounds().Dy() != 160 {
		t.Fatalf("size %v %v", img.Bounds(), err)
	}
	if r, g, b, _ := img.At(200, 100).RGBA(); r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Errorf("transparent area not white: %d %d %d", r>>8, g>>8, b>>8)
	}
	if _, _, err := thumbnail([]byte("<html>not an image</html>")); err == nil {
		t.Error("HTML accepted as image")
	}
	// decompression bomb: huge dimensions in a tiny file
	var bomb bytes.Buffer
	_ = png.Encode(&bomb, image.NewGray(image.Rect(0, 0, 10000, 5000)))
	if _, _, err := thumbnail(bomb.Bytes()); err == nil {
		t.Error("50-megapixel image accepted")
	}
}

func TestPWAAssets(t *testing.T) {
	assets := pwaAssets(`"abc123"`)
	var m struct {
		StartURL string `json:"start_url"`
		Display  string `json:"display"`
		Icons    []struct{ Src, Sizes, Purpose string }
	}
	if err := json.Unmarshal(assets["manifest.webmanifest"].body, &m); err != nil || m.StartURL != "./" || m.Display != "standalone" || len(m.Icons) != 3 {
		t.Fatalf("manifest: %v %+v", err, m)
	}
	for name, size := range map[string]int{"icon-192.png": 192, "icon-512.png": 512, "icon-maskable.png": 512} {
		img, err := png.Decode(bytes.NewReader(assets[name].body))
		if err != nil || img.Bounds().Dx() != size {
			t.Errorf("%s: %v %v", name, err, img.Bounds())
		}
	}
	if !strings.Contains(string(assets["sw.js"].body), "const CACHE = 'ndb-dev-abc123'") {
		t.Error("service worker cache name must carry the build version")
	}
}

// ---------------------------------------------------------------------------
// Phase 6: hardening

func newTestApp(t *testing.T, cfgYAML string) *App {
	t.Helper()
	cfg, err := parseConfig([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, level: new(slog.LevelVar), started: time.Now(), news: newNewsCache(), sched: newScheduler(),
		wx: newWeatherCaches(), threats: newStateStore(), geo: newGeoCache(100), metrics: newHTTPMetrics(),
		alarms: newTTLCache[[]Alarm](50), air: newTTLCache[[]AirComponent](20), pollen: newTTLCache[PollenData](20), p2k: newP2KCounters(), push: newPushHub(), waste: newTTLCache[WasteResult](20), insects: newTTLCache[InsectData](20), skyClouds: newTTLCache[[]cloudPoint](20), wikiCache: newTTLCache[WikiSummary](20), solar: newTTLCache[[]SolarDay](20), icons: newIconCache(), tides: newTTLCache[[]Tide](20), sea: newTTLCache[SeaNow](20), asnNames: map[int]string{}}
	a.images = newImageProxy(func() string { return "test" })
	a.fetcher = newFetcher(4, func() string { return "test" }, func() time.Duration { return 5 * time.Second })
	return a
}

func get(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSecurityHeadersOnEveryRoute(t *testing.T) {
	a := newTestApp(t, validConfig+"\nfeatures: { show_images: true, proxy_images: true }\n")
	h := a.routes("/")
	routes := map[string]int{
		"/": 200, "/api/catalog": 200, "/api/news": 200, "/api/threats": 200, "/api/advisories": 200, "/api/breaches": 200, "/api/outages": 200, "/api/energy": 200, "/api/air": 200, "/api/trains": 200, "/api/politics": 200, "/api/air?lat=x&lon=5": 400, "/api/today": 200, "/api/ransomware": 200, "/api/pollen?lat=x&lon=5": 400, "/api/utilities": 200, "/api/quakes": 200, "/api/economy": 200, "/api/markets": 200, "/api/nlalert": 200, "/api/nlalert?lat=x": 400, "/api/fuel": 200, "/api/waste": 200, "/api/trending": 200, "/api/push": 200, "/healthz": 200,
		"/api/weather?lat=abc&lon=5": 400, "/api/geocode?q=a": 400, "/api/img?u=aHR0cHM6Ly9ldmls&s=forged": 403,
		"/manifest.webmanifest": 200, "/icon-192.png": 200, "/sw.js": 200, "/metrics": 404, "/nope": 404, "/api/news/../../etc/passwd": 404,
	}
	for path, want := range routes {
		rec := get(h, "GET", path, nil)
		// the mux cleans "/api/news/../../etc/passwd" and redirects to "/etc/passwd" (a 404): nothing is served
		if rec.Code != want && !(strings.Contains(path, "..") && rec.Code == http.StatusTemporaryRedirect && rec.Header().Get("Location") == "/etc/passwd") {
			t.Errorf("%s: status %d, want %d", path, rec.Code, want)
		}
		hd := rec.Header()
		for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY"} {
			if hd.Get(k) != v {
				t.Errorf("%s: %s = %q", path, k, hd.Get(k))
			}
		}
		c := hd.Get("Content-Security-Policy")
		if !strings.Contains(c, "default-src 'self'") || !strings.Contains(c, "frame-ancestors 'none'") || !strings.Contains(hd.Get("Permissions-Policy"), "camera=()") {
			t.Errorf("%s: weak or missing CSP/Permissions-Policy: %q", path, c)
		}
	}
	if rec := get(h, "POST", "/api/news", nil); rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d %q", rec.Code, rec.Header().Get("Allow"))
	}
	// base_path: only the subfolder serves the app
	sub := newTestApp(t, validConfig+"\nserver: { base_path: /nieuws/ }\n").routes("/nieuws/")
	if get(sub, "GET", "/nieuws/api/catalog", nil).Code != 200 || get(sub, "GET", "/nieuws", nil).Code != 301 || get(sub, "GET", "/api/catalog", nil).Code != 404 {
		t.Error("base_path routing")
	}
}

func TestCSPUsesScriptHashesNotUnsafeInline(t *testing.T) {
	scripts := inlineScriptRe.FindAllSubmatch(indexHTML, -1)
	if len(scripts) != 2 {
		t.Fatalf("expected 2 inline scripts in index.html, found %d", len(scripts))
	}
	var scriptSrc string
	for _, d := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(d), "script-src") {
			scriptSrc = d
		}
	}
	if strings.Contains(scriptSrc, "unsafe-inline") || strings.Count(scriptSrc, "'sha256-") != 2 || !strings.Contains(csp, "object-src 'none'") {
		t.Errorf("script-src must list exactly the two hashes: %q", scriptSrc)
	}
	if strings.Contains(string(indexHTML), " onclick=") || strings.Contains(string(indexHTML), " onload=") {
		t.Error("inline event handler attributes would be blocked by the CSP")
	}
}

func TestGzipAndETag(t *testing.T) {
	var big strings.Builder // a realistic catalog (> 512 bytes, the gzip threshold)
	big.WriteString(validConfig)
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&big, "  - { id: s%d, name: \"Bron nummer %d\", category: nl, url: \"https://s%d.example/rss\", homepage: \"https://s%d.example\" }\n", i, i, i, i)
	}
	a := newTestApp(t, big.String())
	h := a.routes("/")
	for _, path := range []string{"/api/catalog", "/"} {
		rec := get(h, "GET", path, map[string]string{"Accept-Encoding": "br, gzip;q=0.8"})
		if rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Fatalf("%s: not gzipped (%v)", path, rec.Header())
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(zr)
		if path == "/api/catalog" && !json.Valid(body) || path == "/" && !bytes.Contains(body, []byte("<!doctype html>")) {
			t.Errorf("%s: gzip body does not decode to the content", path)
		}
		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag", path)
		}
		if r2 := get(h, "GET", path, map[string]string{"If-None-Match": etag}); r2.Code != 304 || r2.Body.Len() != 0 {
			t.Errorf("%s: conditional GET = %d with %d bytes", path, r2.Code, r2.Body.Len())
		}
		if r3 := get(h, "GET", path, map[string]string{"Accept-Encoding": "gzip;q=0"}); r3.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: gzip;q=0 must disable compression", path)
		}
		if r4 := get(h, "HEAD", path, nil); r4.Code != 200 || r4.Body.Len() != 0 {
			t.Errorf("%s: HEAD returned a body", path)
		}
	}
	if cc := get(h, "GET", "/api/catalog", nil).Header().Get("Cache-Control"); cc != "max-age=60" {
		t.Errorf("JSON Cache-Control: %q", cc)
	}
	if cc := get(h, "GET", "/healthz", nil).Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("healthz Cache-Control: %q", cc)
	}
}

// A hostile feed, fetched over HTTP and served through /api/news.
const evilFeed = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>evil</title>
<item><title><![CDATA[<img src=x onerror=alert(1)>Kop <script>alert(2)</script>één]]></title>
  <link>https://evil.example/1</link>
  <description>&lt;script&gt;alert(3)&lt;/script&gt;&lt;b onmouseover="alert(4)"&gt;tekst&lt;/b&gt;<![CDATA[<svg onload=alert(5)><iframe src="javascript:alert(6)"></iframe>]]></description>
  <enclosure url="http://evil.example/tracker.jpg" type="image/jpeg"/></item>
<item><title>JS link</title><link>javascript:alert(7)</link></item>
<item><title>Data link</title><link>data:text/html,&lt;script&gt;alert(8)&lt;/script&gt;</link></item>
<item><title>Credentials link</title><link>https://user:pass@evil.example/x</link></item>
<item><title>Bidi ‮txt.exe‬ spoof</title><link>https://evil.example/bidi</link></item>
<item><title>` + "LANG" + `</title><link>https://evil.example/long</link><description>` + "LANGSUM" + `</description></item>
MANY
</channel></rss>`

func TestMaliciousFeedEndToEnd(t *testing.T) {
	body := strings.NewReplacer("LANGSUM", strings.Repeat("woord ", 3000), "LANG", strings.Repeat("x", 10000)).Replace(evilFeed)
	var many strings.Builder
	recent := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour) // within cache.max_age, whatever the date
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&many, "<item><title>Item %d</title><link>https://evil.example/n/%d</link><pubDate>%s</pubDate></item>\n", i, i, recent.Add(time.Duration(i%60)*time.Minute).Format(time.RFC1123Z))
	}
	body = strings.Replace(body, "MANY", many.String(), 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/evil.xml":
			io.WriteString(w, body)
		case "/huge.xml":
			io.WriteString(w, "<rss><channel>"+strings.Repeat("<item><title>a</title></item>", 250000)+"</channel></rss>") // > 5 MB
		case "/laughs.xml":
			io.WriteString(w, `<?xml version="1.0"?><!DOCTYPE r [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;"><!ENTITY c "&b;&b;&b;&b;&b;&b;&b;&b;&b;&b;">]><rss><channel><item><title>&c;&c;&c;</title><link>https://evil.example/l</link></item></channel></rss>`)
		case "/deep.xml":
			io.WriteString(w, "<rss><channel>"+strings.Repeat("<a>", 200000)+"</channel></rss>")
		}
	}))
	defer srv.Close()
	cfg := strings.Replace(validConfig, `url: "https://a.example/rss" }`, `url: "`+srv.URL+`/evil.xml" }`, 1) +
		`  - { id: huge, name: "Huge", category: nl, url: "` + srv.URL + `/huge.xml" }` + "\n" +
		`  - { id: laughs, name: "Laughs", category: nl, url: "` + srv.URL + `/laughs.xml" }` + "\n" +
		`  - { id: deep, name: "Deep", category: nl, url: "` + srv.URL + `/deep.xml" }` + "\n" +
		"features: { show_images: true, proxy_images: false }\n"
	a := newTestApp(t, cfg)
	for _, s := range a.cfg.Sources {
		start := time.Now()
		_ = a.fetchSource(context.Background(), s)
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%s took %v", s.ID, d)
		}
	}
	rec := get(a.routes("/"), "GET", "/api/news?limit=500&group=0&sources=a,huge,laughs,deep", nil)
	var resp struct {
		Items  []Item                  `json:"items"`
		Status map[string]SourceStatus `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if n := resp.Status["a"].Items; n != 50 {
		t.Errorf("per-source cap: %d items cached, want 50", n)
	}
	if st := resp.Status["huge"]; st.OK || !strings.Contains(st.Error, "5 MB") {
		t.Errorf("oversized feed must be refused: %+v", st)
	}
	if st := resp.Status["deep"]; st.Items != 0 { // skipped quickly (timed above), nothing extracted
		t.Errorf("deeply nested XML must not yield items: %+v", st)
	}
	var sawHostile, sawLaughs bool
	for _, it := range resp.Items {
		for _, f := range []string{it.Title, it.Summary} {
			if strings.ContainsAny(f, "<>") || strings.Contains(strings.ToLower(f), "onerror") || strings.ContainsAny(f, "‮‬") {
				t.Errorf("unsafe text survived: %q", f)
			}
			if len([]rune(f)) > 301 {
				t.Errorf("not truncated: %d runes", len([]rune(f)))
			}
		}
		if !strings.HasPrefix(it.URL, "https://evil.example/") || strings.Contains(it.URL, "@") {
			t.Errorf("unsafe link survived: %q", it.URL)
		}
		if it.Image != "" {
			t.Errorf("http image must be dropped: %q", it.Image)
		}
		if strings.HasPrefix(it.Title, "Kop") {
			sawHostile = true
			if it.Title != "Kop alert(2) één" && it.Title != "Kop één" {
				t.Errorf("hostile title: %q", it.Title)
			}
		}
		if strings.Contains(it.Title, "aaaa") {
			sawLaughs = true
		}
		if strings.Contains(it.Title, "Bidi") && it.Title != "Bidi txt.exe spoof" {
			t.Errorf("bidi override not removed: %q", it.Title)
		}
	}
	if !sawHostile {
		t.Error("hostile item should be kept (as plain text)")
	}
	if sawLaughs {
		t.Error("entity expansion must not happen")
	}
}

// SSRF: a first hop that is allowed may not redirect to a blocked address.
func TestImageProxyRedirectToPrivateIsBlocked(t *testing.T) {
	lnB, err := net.Listen("tcp", "127.0.0.2:0") // the "internal" service
	if err != nil {
		t.Skip("127.0.0.2 not available:", err)
	}
	hitB := false
	srvB := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hitB = true }))
	srvB.Listener = lnB
	srvB.StartTLS()
	defer srvB.Close()
	img := func() []byte {
		var buf bytes.Buffer
		_ = png.Encode(&buf, image.NewGray(image.Rect(0, 0, 10, 10)))
		return buf.Bytes()
	}()
	srvA := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Write(img)
		case "/to-internal":
			http.Redirect(w, r, srvB.URL+"/latest/meta-data", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://example.com/x.png", http.StatusFound)
		}
	}))
	defer srvA.Close()

	p := newImageProxy(func() string { return "test" })
	p.allowIP = func(h string) bool { return h == "127.0.0.1" } // pretend srvA is a public host
	p.client.Transport.(*http.Transport).TLSClientConfig = srvA.Client().Transport.(*http.Transport).TLSClientConfig
	if _, _, err := p.fetch(context.Background(), srvA.URL+"/ok.png"); err != nil {
		t.Fatalf("allowed host must work: %v", err)
	}
	_, _, err = p.fetch(context.Background(), srvA.URL+"/to-internal")
	if err == nil || !strings.Contains(err.Error(), "blocked non-public address 127.0.0.2") || hitB {
		t.Errorf("redirect to internal address must be blocked before connecting: err=%v, reached=%v", err, hitB)
	}
	if _, _, err := p.fetch(context.Background(), srvA.URL+"/to-http"); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Errorf("redirect to http must be refused: %v", err)
	}
	for _, u := range []string{"https://localhost/x", "https://[::1]/x", "https://user@127.0.0.1/x", "https://169.254.169.254/latest/meta-data"} {
		if _, _, err := newImageProxy(func() string { return "t" }).fetch(context.Background(), u); err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Errorf("%s must be blocked, got %v", u, err)
		}
	}
}

func TestRateLimiterBounded(t *testing.T) {
	l := newRateLimiter(60, 10)
	for i := 0; i < 60000; i++ {
		l.allow(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
	}
	if n := len(l.buckets); n > 50001 {
		t.Errorf("rate limiter map grew to %d entries", n)
	}
}

func TestClientIPAndTrustedProxies(t *testing.T) {
	t.Setenv("NDB_TRUSTED_PROXIES", "127.0.0.1, 172.16.0.0/12")
	a := newTestApp(t, validConfig)
	ip := func(remote, xff string) string {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return a.clientIP(r).String()
	}
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.9:1234", "1.2.3.4", "203.0.113.9"},            // untrusted peer: header ignored
		{"172.17.0.1:5555", "198.51.100.7", "198.51.100.7"},       // Docker gateway (trusted) → client from header
		{"127.0.0.1:80", "6.6.6.6, 198.51.100.7", "198.51.100.7"}, // rightmost untrusted hop, not the spoofable first
		{"127.0.0.1:80", "garbage", "127.0.0.1"},
	}
	for _, c := range cases {
		if got := ip(c.remote, c.xff); got != c.want {
			t.Errorf("remote %s xff %q → %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestMetricsOptIn(t *testing.T) {
	off := newTestApp(t, validConfig)
	if get(off.routes("/"), "GET", "/metrics", nil).Code != 404 {
		t.Error("metrics must be off by default")
	}
	on := newTestApp(t, validConfig+"\nserver: { metrics: true }\n")
	h := on.routes("/")
	get(h, "GET", "/api/catalog", nil)
	rec := get(h, "GET", "/metrics", nil)
	body := rec.Body.String()
	for _, want := range []string{`ndb_build_info{version="dev"} 1`, `ndb_source_up{source="a",kind="news"} 0`,
		`ndb_http_requests_total{route="GET /api/catalog",code="200"} 1`, "# TYPE ndb_image_cache_bytes gauge", "go_goroutines "} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Error("Prometheus content type")
	}
}

// ---------------------------------------------------------------------------
// Top bar, traffic, outages

func TestNCTVParse(t *testing.T) {
	page := func(sentence string) []byte {
		return []byte(`<html><head><script>var x = "niveau 9 van 5";</script></head><body><h1>Dreigingsniveau</h1><p>` + sentence + `</p></body></html>`)
	}
	cases := []struct {
		sentence string
		level    int
		since    string
	}{
		{"Het dreigingsniveau blijft met het DTN van juni 2026 daarom gehandhaafd op niveau 4 op een schaal van 5.", 4, "juni 2026"},
		{"Het <strong>dreigingsniveau</strong> is met het DTN van december 2026 verhoogd naar niveau 5 van 5.", 5, "december 2026"},
		{"Het dreigingsniveau is verlaagd naar niveau 3 op een schaal van vijf.", 3, ""},
	}
	for _, c := range cases {
		v, err := parseNCTV(page(c.sentence))
		if err != nil {
			t.Errorf("%q: %v", c.sentence, err)
			continue
		}
		if l := v.(NCTVLevel); l.Level != c.level || l.Name != nctvNames[c.level] || l.Since != c.since {
			t.Errorf("%q → %+v", c.sentence, l)
		}
	}
	for _, bad := range []string{"Het dreigingsniveau is substantieel.", "Lees meer over het dreigingsniveau.", ""} {
		if _, err := parseNCTV(page(bad)); err == nil {
			t.Errorf("unknown wording must be an error, not a guess: %q", bad)
		}
	}
}

func TestKNMISummary(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	ws := []WxWarning{
		{Level: "yellow", Type: "Wind", Area: "Zeeland", Country: "nl", Onset: now.Add(-time.Hour)},
		{Level: "orange", Type: "Wind", Area: "Noord-Holland", Country: "nl", Onset: now.Add(6 * time.Hour)},
		{Level: "orange", Type: "Onweer", Area: "Friesland", Country: "nl", Onset: now.Add(3 * time.Hour)},
		{Level: "red", Type: "Regen", Area: "Antwerpen", Country: "be"},
	}
	s := knmiSummary(ws, now)
	if s.Level != "orange" || s.Active || s.Count != 3 || s.Onset == nil || !s.Onset.Equal(now.Add(3*time.Hour)) ||
		strings.Join(s.Types, ",") != "Wind,Onweer" || len(s.Areas) != 2 {
		t.Errorf("summary: %+v", s)
	}
	if s := knmiSummary(nil, now); s.Level != "none" || s.Count != 0 {
		t.Errorf("no warnings: %+v", s)
	}
}

// buildDBF writes a minimal dBASE III table with the VILD columns we read.
func buildDBF(rows [][]string) []byte {
	cols := []struct {
		name string
		n    int
	}{{"LOC_NR", 6}, {"LOC_DES", 30}, {"ROADNUMBER", 6}, {"FIRST_NAME", 30}, {"SECND_NAME", 30}, {"EXIT_NR", 4}, {"LIN_REF", 6}}
	rlen := 1
	for _, c := range cols {
		rlen += c.n
	}
	hlen := 32 + 32*len(cols) + 1
	var b bytes.Buffer
	hdr := make([]byte, 32)
	hdr[0] = 3
	binary.LittleEndian.PutUint32(hdr[4:], uint32(len(rows)))
	binary.LittleEndian.PutUint16(hdr[8:], uint16(hlen))
	binary.LittleEndian.PutUint16(hdr[10:], uint16(rlen))
	b.Write(hdr)
	for _, c := range cols {
		d := make([]byte, 32)
		copy(d, c.name)
		d[11], d[16] = 'C', byte(c.n)
		b.Write(d)
	}
	b.WriteByte(0x0d)
	for _, r := range rows {
		b.WriteByte(' ')
		for i, c := range cols {
			v := []byte(r[i])
			if r[i] == "Ståd" {
				v = []byte{'S', 't', 0xe5, 'd'} // Latin-1 å
			}
			b.Write(append(v, bytes.Repeat([]byte(" "), c.n-len(v))...))
		}
	}
	b.WriteByte(0x1a)
	return b.Bytes()
}

const ndwFixture = `<?xml version="1.0"?>
<d2LogicalModel xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><payloadPublication><situation>
<sit:situationRecord xsi:type="sit:AbnormalTraffic" id="1"><sit:validity><com:validityStatus>definedByValidityTimeSpec</com:validityStatus>
 <com:validityTimeSpecification><com:overallStartTime>2026-09-24T11:30:00Z</com:overallStartTime></com:validityTimeSpecification></sit:validity>
 <sit:impact><sit:delays><sit:delayTimeValue>200.0</sit:delayTimeValue></sit:delays></sit:impact><sit:abnormalTrafficType>stationaryTraffic</sit:abnormalTrafficType>
 <sit:locationReference><loc:alertCLinear><loc:alertCLocationTableNumber>6.13</loc:alertCLocationTableNumber><loc:alertCLocationTableVersion>A</loc:alertCLocationTableVersion>
 <loc:alertCDirection><loc:alertCDirectionCoded>positive</loc:alertCDirectionCoded></loc:alertCDirection>
 <loc:alertCMethod4PrimaryPointLocation><loc:alertCLocation><loc:specificLocation>8316</loc:specificLocation></loc:alertCLocation></loc:alertCMethod4PrimaryPointLocation>
 <loc:alertCMethod4SecondaryPointLocation><loc:alertCLocation><loc:specificLocation>8315</loc:specificLocation></loc:alertCLocation></loc:alertCMethod4SecondaryPointLocation></loc:alertCLinear></sit:locationReference></sit:situationRecord>
<sit:situationRecord xsi:type="sit:AbnormalTraffic" id="2"><sit:validity><com:validityStatus>active</com:validityStatus></sit:validity>
 <sit:impact><sit:delays><sit:delayTimeValue>1500.0</sit:delayTimeValue></sit:delays></sit:impact><sit:abnormalTrafficType>slowTraffic</sit:abnormalTrafficType>
 <sit:locationReference><loc:alertCDirectionCoded>negative</loc:alertCDirectionCoded><loc:specificLocation>9001</loc:specificLocation></sit:locationReference></sit:situationRecord>
<sit:situationRecord xsi:type="sit:AbnormalTraffic" id="3"><sit:validity><com:validityStatus>suspended</com:validityStatus></sit:validity></sit:situationRecord>
<sit:situationRecord xsi:type="sit:Accident" id="7"><sit:validity><com:validityStatus>active</com:validityStatus></sit:validity></sit:situationRecord>
<sit:situationRecord xsi:type="sit:RoadOrCarriagewayOrLaneManagement" id="4"><sit:validity><com:validityStatus>active</com:validityStatus></sit:validity><sit:roadOrCarriagewayOrLaneManagementType>roadClosed</sit:roadOrCarriagewayOrLaneManagementType></sit:situationRecord>
<sit:situationRecord xsi:type="sit:RoadOrCarriagewayOrLaneManagement" id="5"><sit:validity><com:validityTimeSpecification><com:overallStartTime>2026-12-01T00:00:00Z</com:overallStartTime></com:validityTimeSpecification></sit:validity><sit:roadOrCarriagewayOrLaneManagementType>roadClosed</sit:roadOrCarriagewayOrLaneManagementType></sit:situationRecord>
<sit:situationRecord xsi:type="sit:Accident" id="6"><sit:validity><com:validityStatus>active</com:validityStatus><com:validityTimeSpecification><com:overallStartTime>2026-09-24T11:50:00Z</com:overallStartTime></com:validityTimeSpecification></sit:validity>
 <loc:alertCDirectionCoded>positive</loc:alertCDirectionCoded><loc:specificLocation>8316</loc:specificLocation></sit:situationRecord>
</situation></payloadPublication></d2LogicalModel>`

func TestTrafficNDWAndVILD(t *testing.T) {
	dbf := buildDBF([][]string{
		{"8316", "Afrit", "A27", "Werkendam", "", "23", "3170"},
		{"8315", "Afrit", "A27", "Hank", "", "22", "3170"},
		{"3170", "Orde 1 segment", "A27", "Breda", "Gorinchem", "", "0"},
		{"9001", "Knooppunt", "A2", "Ståd", "", "", "9100"},
		{"9100", "Orde 1 segment", "A2", "Amsterdam", "Maastricht", "", "0"},
	})
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for _, e := range []struct {
		name string
		body []byte
	}{{"handboek.pdf", bytes.Repeat([]byte("x"), 2<<20)}, {"VILD6.13.A.dbf", dbf}, {"WGS84/vild_point.dbf", []byte("not this one")}} {
		f, _ := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store}) // stored: the zip really is > 2 MB
		f.Write(e.body)
	}
	zw.Close()
	var ranges, served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/VILD6.13.A.zip":
			ranges++
			cw := &countingWriter{ResponseWriter: w}
			http.ServeContent(cw, r, "vild.zip", time.Time{}, bytes.NewReader(zbuf.Bytes())) // honours Range
			served += cw.n
		case "/norange/VILD6.13.A.zip":
			w.Write(zbuf.Bytes())
		case "/actueel_beeld.xml.gz":
			zw := gzip.NewWriter(w)
			io.WriteString(zw, ndwFixture)
			zw.Close()
		}
	}))
	defer srv.Close()
	a := newTestApp(t, validConfig+"\ntraffic: { url: \""+srv.URL+"/actueel_beeld.xml.gz\", vild_base: \""+srv.URL+"/\" }\n")

	vt, err := a.loadVILD(context.Background(), "6.13.A")
	if err != nil {
		t.Fatal(err)
	}
	if len(vt.locs) != 5 || vt.locs[9001].Name1 != "Ståd" || vt.locs[3170].Name2 != "Gorinchem" {
		t.Errorf("vild table: %+v", vt.locs)
	}
	if ranges > 6 || served > zbuf.Len()/4 {
		t.Errorf("only the table may be fetched: %d requests, %d of %d bytes", ranges, served, zbuf.Len())
	}
	a.cfg.Traffic.VILDBase = srv.URL + "/norange/"
	if _, err := a.loadVILD(context.Background(), "6.13.A"); err == nil || !strings.Contains(err.Error(), "range") {
		t.Errorf("a server without range support must be refused (never download the whole zip): %v", err)
	}
	a.cfg.Traffic.VILDBase = srv.URL + "/"

	if err := a.runTraffic(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := a.threats.get("ndw:traffic").Data.(TrafficData)
	if d.JamCount != 2 || d.TotalDelay != 1700 || d.AccidentCount != 2 || len(d.Accidents) != 1 || d.Closures != 1 || d.Table != "6.13.A" {
		t.Fatalf("counts: %+v", d)
	}
	j := d.Jams[1] // sorted by delay: the 1500 s jam first
	if j.Road != "A27" || j.Dir != "richting Gorinchem" || j.From != "Hank" || j.To != "Werkendam" || j.Kind != "Stilstaand verkeer" || j.Delay != 200 {
		t.Errorf("jam: %+v", j)
	}
	if k := d.Jams[0]; k.Road != "A2" || k.Dir != "richting Amsterdam" || k.To != "knp. Ståd" || k.Kind != "Langzaam rijdend verkeer" {
		t.Errorf("negative direction / knooppunt: %+v", k)
	}
	if acc := d.Accidents[0]; acc.Road != "A27" || acc.To != "Werkendam" || acc.Kind != "Ongeval" {
		t.Errorf("accident: %+v", acc)
	}
}

func TestParseBreaches(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	e := func(name, domain, added, desc string, flags ...string) string {
		f := map[string]bool{"IsVerified": true}
		for _, x := range flags {
			f[x] = !f[x]
		}
		b, _ := json.Marshal(map[string]any{"Name": name, "Title": name + " <b>x</b>", "Domain": domain, "BreachDate": "2026-01-02",
			"AddedDate": added, "PwnCount": 1234, "Description": desc, "DataClasses": []string{"Email addresses", "Passwords"},
			"IsVerified": f["IsVerified"], "IsSpamList": f["IsSpamList"], "IsMalware": f["IsMalware"], "IsSensitive": f["IsSensitive"],
			"IsFabricated": f["IsFabricated"], "IsStealerLog": f["IsStealerLog"], "IsRetired": f["IsRetired"]})
		return string(b)
	}
	body := "[" + strings.Join([]string{
		e("Odido", "odido.nl", "2026-02-26T10:00:00Z", "Dutch telco"),
		e("Welhof", "welhof.com", "2025-01-22T10:00:00Z", `A <a href="x">parking provider</a> in the Netherlands`),
		e("Ticketcounter", "ticketcounter.nl", "2021-03-01T10:00:00Z", "tickets"),
		e("OldNL", "old.nl", "2019-01-01T10:00:00Z", "old"),
		e("Emotet", "", "2026-09-01T10:00:00Z", "Dutch police botnet", "IsMalware"),
		e("NLSpam", "spam.nl", "2026-09-02T10:00:00Z", "spam", "IsSpamList"),
		e("HookersNL", "hookers.nl", "2026-09-03T10:00:00Z", "adult", "IsSensitive"),
		e("Unverified", "unv.com", "2026-09-04T10:00:00Z", "who knows", "IsVerified"),
		e("NoDomain", "", "2026-09-05T10:00:00Z", "collection"),
		e("Future", "future.com", "2026-12-01T10:00:00Z", "clock skew"),
		e("Chess2026", "chess.com", "2026-09-13T10:00:00Z", "chess"),
		e("McKesson", "mckesson.com", "2026-09-10T10:00:00Z", "health"),
		e("Carhartt", "carhartt.com", "2026-08-25T10:00:00Z", "clothes"),
		e("Questel", "questel.com", "2026-09-01T10:00:00Z", "ip"),
		e("Bad Name/../x", "bad.com", "2026-09-20T10:00:00Z", "invalid name"),
	}, ",") + "]"
	v, err := parseBreaches([]byte(body), false, now)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(BreachData)
	names := func(l []Breach) (out []string) {
		for _, b := range l {
			out = append(out, b.Name)
		}
		return
	}
	if got := strings.Join(names(d.NL), ","); got != "Odido,Welhof,Ticketcounter" {
		t.Errorf("NL: %s", got)
	}
	if got := strings.Join(names(d.Other), ","); got != "Chess2026,McKesson,Questel" {
		t.Errorf("other: %s", got)
	}
	if d.Total != 15 || d.Shown != 8 {
		t.Errorf("total %d shown %d", d.Total, d.Shown)
	}
	o := d.NL[0]
	if o.Title != "Odido x" || o.URL != "https://haveibeenpwned.com/Breach/Odido" || o.Count != 1234 || o.BreachDate != "2026-01-02" ||
		len(o.DataClasses) != 2 || strings.Contains(d.NL[1].Summary, "<") {
		t.Errorf("entry: %+v", o)
	}
	// sensitive entries only when configured
	v, _ = parseBreaches([]byte(body), true, now)
	if got := names(v.(BreachData).NL); got[0] != "HookersNL" {
		t.Errorf("include_sensitive: %v", got)
	}
	for _, bad := range []string{"", "[]", "{}", "<html>"} {
		if _, err := parseBreaches([]byte(bad), false, now); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestParseEnergyZero(t *testing.T) {
	body := `{"Prices":[{"readingDate":"2026-09-25T01:00:00Z","price":0.1},{"readingDate":"2026-09-25T00:00:00Z","price":0.0616825},
		{"readingDate":"2026-09-25T02:00:00Z","price":-0.01},{"readingDate":"2026-09-25T03:00:00Z","price":99},{"readingDate":"2026-09-25T04:00:00Z"}]}`
	pts, err := parseEnergyZero([]byte(body), 0.21, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 || !pts[0].Time.Equal(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)) || pts[0].Price != 0.07464 || pts[2].Price != -0.0121 {
		t.Errorf("points: %+v", pts)
	}
	pts, _ = parseEnergyZero([]byte(body), 0.21, 0.15)
	if pts[0].Price != 0.22464 {
		t.Errorf("extra: %v", pts[0].Price)
	}
	if _, err := parseEnergyZero([]byte("<html>"), 0.21, 0); err == nil {
		t.Error("expected error")
	}
}

func TestNearestAirStation(t *testing.T) {
	st := map[string]airStation{
		"NL10643": {Number: "NL10643", Name: "Utrecht-Griftpark", Lat: 52.101, Lon: 5.128},
		"NL10445": {Number: "NL10445", Name: "Den Haag", Lat: 52.075, Lon: 4.316},
		"NL00001": {Number: "NL00001", Name: "No LKI", Lat: 52.09, Lon: 5.12},
	}
	lki := map[string]airLKI{"NL10643": {Value: 3}, "NL10445": {Value: 4}}
	s, l, d, ok := nearestAirStation(st, lki, 52.09, 5.12)
	if !ok || s.Number != "NL10643" || l.Value != 3 || d < 1 || d > 2 {
		t.Errorf("Utrecht: %v %v %.2f", s, l, d)
	}
	if s, _, _, _ := nearestAirStation(st, lki, 52.08, 4.31); s.Number != "NL10445" {
		t.Errorf("Den Haag: %v", s)
	}
	if _, _, _, ok := nearestAirStation(st, map[string]airLKI{}, 52, 5); ok {
		t.Error("no LKI anywhere: expected no station")
	}
	if d := distanceKm(52.09, 5.12, 52.37, 4.9); d < 33 || d > 36 {
		t.Errorf("Utrecht–Amsterdam: %.1f km", d)
	}
}

func TestParseNSDisruptions(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	body := `[
	 {"id":"1","type":"DISRUPTION","title":"Utrecht - Amersfoort","isActive":true,"start":"2026-09-25T10:00:00+0200","end":"2026-09-25T15:00:00+0200",
	  "impact":{"value":4},"expectedDuration":{"description":"Tot ongeveer 15:00"},"summaryAdditionalTravelTime":{"label":"30 minuten extra"},
	  "timespans":[{"situation":{"label":"Minder treinen tussen Utrecht en Amersfoort"},"cause":{"label":"<b>defecte</b> trein"}}]},
	 {"id":"2","type":"CALAMITY","title":"Landelijke storing","description":"Er rijden geen treinen","isActive":true},
	 {"id":"3","type":"MAINTENANCE","title":"Werk Zwolle","isActive":true,"start":"2026-09-25T01:00:00Z","impact":{"value":2}},
	 {"id":"4","type":"MAINTENANCE","title":"Werk later","isActive":true,"start":"2026-10-03T01:00:00Z"},
	 {"id":"5","type":"DISRUPTION","title":"Oud","isActive":false},
	 {"id":"6","type":"DISRUPTION","title":""}
	]`
	v, err := parseNSDisruptions([]byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(TrainData)
	if len(d.Calamities) != 1 || d.Calamities[0].Situation != "Er rijden geen treinen" {
		t.Errorf("calamities: %+v", d.Calamities)
	}
	if len(d.Disruptions) != 1 {
		t.Fatalf("disruptions: %+v", d.Disruptions)
	}
	x := d.Disruptions[0]
	if x.Cause != "defecte trein" || x.Extra != "30 minuten extra" || x.Impact != 4 || x.Expected != "Tot ongeveer 15:00" || x.Start == nil || x.End == nil {
		t.Errorf("disruption: %+v", x)
	}
	if d.MaintTotal != 2 || len(d.Maintenance) != 1 || d.Maintenance[0].Title != "Werk Zwolle" {
		t.Errorf("maintenance: %d %+v", d.MaintTotal, d.Maintenance)
	}
	// Live NS format (25 Sep 2026): offsets without colon, title ending in a period, cause repeated in
	// the situation, and a readable period for maintenance.
	live := `[{"type":"DISRUPTION","id":"6067873","title":"Nijmegen - 's-Hertogenbosch.","isActive":true,"start":"2026-09-25T15:19:00+0200",
	  "end":"2026-09-26T00:30:00+0200","impact":{"value":3},"expectedDuration":{"description":"Dit duurt tot ongeveer zaterdag 26 september 0:30 uur.","endTime":"2026-09-26T00:30:00+0200"},
	  "timespans":[{"start":"2026-09-25T15:19:00+0200","end":"2026-09-26T00:30:00+0200","situation":{"label":"Door een defect spoor: tussen Oss en 's-Hertogenbosch rijden er veel minder treinen."},"cause":{"label":"defect spoor"},"advices":[]}],
	  "titleSections":[[{"type":"STATION","value":"Nijmegen"}]],"publicationSections":[],"alternativeTransportTimespans":[],"local":false},
	 {"type":"MAINTENANCE","id":"7004492","title":"Groningen - Leer.","isActive":true,"start":"2024-02-01T04:00:00+0100","end":"2026-10-04T23:58:00+0200","impact":{"value":3},
	  "summaryAdditionalTravelTime":{"label":"De extra reistijd verschilt per reis.","shortLabel":"x"},"period":"Donderdag 1 februari 2024 4:00 uur t/m zondag 4 oktober 23:58 uur.",
	  "timespans":[{"situation":{"label":"Door een aangepaste dienstregeling: tussen Bad Nieuweschans en Weener rijden er bussen."},"cause":{"label":"aangepaste dienstregeling"},"additionalTravelTime":{"label":"De extra reistijd verschilt per reis."}}]}]`
	v, err = parseNSDisruptions([]byte(live), now)
	if err != nil {
		t.Fatal(err)
	}
	d = v.(TrainData)
	if x := d.Disruptions[0]; x.Title != "Nijmegen - 's-Hertogenbosch" || x.Cause != "" || x.Start == nil || !x.Start.Equal(time.Date(2026, 9, 25, 13, 19, 0, 0, time.UTC)) ||
		x.Expected != "Dit duurt tot ongeveer zaterdag 26 september 0:30 uur." {
		t.Errorf("live disruption: %+v", x)
	}
	if m := d.Maintenance[0]; m.Title != "Groningen - Leer" || m.Expected != "Donderdag 1 februari 2024 4:00 uur t/m zondag 4 oktober 23:58 uur." || m.Extra != "De extra reistijd verschilt per reis." {
		t.Errorf("live maintenance: %+v", m)
	}
	if _, err := parseNSDisruptions([]byte(`{"error":"x"}`), now); err == nil {
		t.Error("object instead of list: expected error")
	}
}

func TestParseLMLStations(t *testing.T) {
	var b strings.Builder
	b.WriteString("#export;EXP-2026-001\r\n#bron;https://data.rivm.nl/data/luchtmeetnet\r\n\r\nmeetlocatie_id;bron_id;meetlocatie_naam;meetlocatie_plaatsnaam;breedtegraad;lengtegraad;hoogte;meetlocatie_begindatumtijd;meetlocatie_einddatumtijd\r\n")
	b.WriteString("NL10643;LML;Utrecht-Griftpark;Utrecht;52.101327;5.128211;5.000;2008-09-01T00:00:00+01:00;\r\n")
	b.WriteString("NL233AA;PBP_SM;Aardenburg;Zeeland;51.270200;3.455027;3.000;2019-01-01T00:00:00+01:00;\r\n")
	b.WriteString("NL10533;LML;Aalsmeer;Aalsmeer;52.278999;4.793000;-3.000;1976-04-03T00:00:00+01:00;1986-04-01T00:00:00+01:00\r\n") // closed
	b.WriteString("NL99999;LML;Far away;X;40.0;5.0;0;2019-01-01T00:00:00+01:00;\r\nbad line\r\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "NL0000%d;LML;S%d;P;52.%d;5.%d;0;2019-01-01T00:00:00+01:00;\n", i, i, i, i)
	}
	v, err := parseLMLStations([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]airStation)
	if s := m["NL10643"]; s.Name != "Utrecht-Griftpark" || s.Municipality != "Utrecht" || s.Lat != 52.101327 || s.Lon != 5.128211 {
		t.Errorf("Utrecht: %+v", s)
	}
	if _, ok := m["NL233AA"]; !ok {
		t.Error("station ids with letters must be accepted")
	}
	if _, ok := m["NL10533"]; ok {
		t.Error("closed station included")
	}
	if _, ok := m["NL99999"]; ok || len(m) != 12 {
		t.Errorf("unexpected stations: %d", len(m))
	}
	if _, err := parseLMLStations([]byte("<html>not a csv</html>")); err == nil {
		t.Error("expected error for a non-CSV body")
	}
}

func TestDutchHolidays(t *testing.T) {
	for y, want := range map[int]string{2024: "2024-03-31", 2025: "2025-04-20", 2026: "2026-04-05", 2027: "2027-03-28"} {
		if got := easter(y).Format("2006-01-02"); got != want {
			t.Errorf("easter %d: %s, want %s", y, got, want)
		}
	}
	find := func(y int, name string) string {
		for _, h := range dutchHolidays(y) {
			if h.Name == name {
				return h.Date
			}
		}
		return ""
	}
	if find(2025, "Koningsdag") != "2025-04-26" || find(2026, "Koningsdag") != "2026-04-27" {
		t.Errorf("Koningsdag moves to the 26th when the 27th is a Sunday: %s %s", find(2025, "Koningsdag"), find(2026, "Koningsdag"))
	}
	if find(2026, "Hemelvaartsdag") != "2026-05-14" || find(2026, "Tweede Pinksterdag") != "2026-05-25" || len(dutchHolidays(2026)) != 11 {
		t.Errorf("2026: %+v", dutchHolidays(2026))
	}
}

func TestMoonPhases(t *testing.T) {
	near := func(got time.Time, want string) bool {
		w, _ := time.Parse(time.RFC3339, want)
		d := got.Sub(w)
		return d > -2*time.Hour && d < 2*time.Hour
	}
	// eclipses: the exact moments are well known
	m := moonInfo(time.Date(2024, 4, 8, 12, 0, 0, 0, time.UTC))
	if !near(m.NextNew, "2024-04-08T18:21:00Z") || m.Phase != "new" {
		t.Errorf("new moon of the 8 April 2024 eclipse: %v %s", m.NextNew, m.Phase)
	}
	m = moonInfo(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if !near(m.NextFull, "2026-03-03T11:38:00Z") {
		t.Errorf("full moon of the 3 March 2026 eclipse: %v", m.NextFull)
	}
	m = moonInfo(time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	if !near(m.NextNew, "2026-08-12T17:37:00Z") || m.Phase != "last-quarter" && m.Phase != "waning-crescent" {
		t.Errorf("new moon of the 12 August 2026 eclipse: %v %s", m.NextNew, m.Phase)
	}
	if m := moonInfo(time.Date(2026, 3, 3, 11, 0, 0, 0, time.UTC)); m.Phase != "full" || m.Illumination < 0.98 || m.Moment == nil || !near(*m.Moment, "2026-03-03T11:38:00Z") {
		t.Errorf("at full moon: %+v", m)
	}
	// 10 hours after the full moon: still "full", and the moment is the one just passed (not next month's)
	if m := moonInfo(time.Date(2026, 3, 3, 21, 30, 0, 0, time.UTC)); m.Phase != "full" || m.Moment == nil || !near(*m.Moment, "2026-03-03T11:38:00Z") || m.NextFull.Month() != time.April {
		t.Errorf("after full moon: %+v", m)
	}
	if m := moonInfo(time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)); m.Moment != nil {
		t.Errorf("a week later there is no principal moment: %+v", m)
	}
}

func TestNextClockChange(t *testing.T) {
	d, dir, ok := nextClockChange(time.Date(2026, 9, 25, 12, 0, 0, 0, amsterdam))
	if !ok || d.Format("2006-01-02") != "2026-10-25" || dir != "back" {
		t.Errorf("autumn: %v %s %v", d, dir, ok)
	}
	if _, _, ok := nextClockChange(time.Date(2026, 12, 1, 12, 0, 0, 0, amsterdam)); ok {
		t.Error("December: the March change is more than 60 days away")
	}
	d, dir, ok = nextClockChange(time.Date(2027, 3, 1, 12, 0, 0, 0, amsterdam))
	if !ok || d.Format("2006-01-02") != "2027-03-28" || dir != "forward" {
		t.Errorf("spring: %v %s %v", d, dir, ok)
	}
	if _, _, ok := nextClockChange(time.Date(2026, 10, 25, 5, 0, 0, 0, amsterdam)); ok {
		t.Error("after the change on the same day it is no longer upcoming")
	}
}

func TestParseSchoolHolidays(t *testing.T) {
	body := `[{"content":[{"schoolyear":"2026-2027","vacations":[
	 {"type":"Herfstvakantie ","regions":[{"region":"noord","startdate":"2026-10-10T00:00:00.000Z","enddate":"2026-10-18T21:59:00.000Z"},
	   {"region":"midden","startdate":"2026-10-17T00:00:00.000Z","enddate":"2026-10-25T22:59:00.000Z"},{"region":"zuid","startdate":"2026-10-17T00:00:00.000Z","enddate":"2026-10-25T22:59:00.000Z"}]},
	 {"type":"Kerstvakantie","regions":[{"region":"heel Nederland","startdate":"2026-12-19T00:00:00.000Z","enddate":"2027-01-03T22:59:00.000Z"}]}]}]},
	 {"content":[{"schoolyear":"2025-2026","vacations":[{"type":"Zomervakantie","regions":[{"region":"noord","startdate":"2026-07-04T00:00:00.000Z","enddate":"2026-08-16T21:59:00.000Z"}]}]}]}]`
	v, err := parseSchoolHolidays([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	all := v.(map[string][]SchoolHoliday)
	if len(all["noord"]) != 3 || all["noord"][0].Type != "Zomervakantie" || all["midden"][1].Type != "Kerstvakantie" || all["zuid"][0].Start != "2026-10-17" || all["noord"][1].End != "2026-10-18" {
		t.Fatalf("parsed: %+v", all)
	}
	now := schoolNow(all, "2026-10-12")
	if h := now["noord"]["holiday"].(SchoolHoliday); h.Type != "Herfstvakantie" || now["noord"]["current"] != true {
		t.Errorf("noord during autumn holiday: %+v", now["noord"])
	}
	if h := now["midden"]["holiday"].(SchoolHoliday); h.Start != "2026-10-17" || now["midden"]["current"] != false {
		t.Errorf("midden before autumn holiday: %+v", now["midden"])
	}
	if _, err := parseSchoolHolidays([]byte(`[]`)); err == nil {
		t.Error("empty feed: expected error")
	}
}

// Every panel with a status entry in /healthz must also have a scheduled job (a feed without
// a job would stay "pending" forever).
func TestEveryFeedHasAJob(t *testing.T) {
	a := newTestApp(t, validConfig+"\nkeys: { ns_api_key: k }\n")
	keys := map[string]bool{}
	for _, j := range append(a.threatJobs(a.config()), a.p2kJobs(a.config())...) {
		keys[j.Key] = true
	}
	for _, f := range a.otherFeeds(a.config()) {
		if !keys[f.ID] {
			t.Errorf("feed %s has no job", f.ID)
		}
	}
}

func TestRansomwareCounts(t *testing.T) {
	a := newTestApp(t, validConfig)
	now := time.Now().UTC()
	var v []RansomVictim
	for i := 0; i < 20; i++ { // one claim every 10 days: 3 within 30 days, 20 within 200 days
		v = append(v, RansomVictim{Name: fmt.Sprintf("org%d", i), Group: []string{"lockbit5", "qilin"}[i%2], Country: "NL", Discovered: now.AddDate(0, 0, -10*i-1)})
	}
	a.threats.ok("rw:NL", RansomData{Victims: v, Total: 20}, "", "")
	rec := get(a.routes("/"), "GET", "/api/ransomware", nil)
	var d struct {
		Last7, Last30, Last365 int
		Victims                []RansomVictim
		TopGroups              []struct {
			Name  string
			Count int
		} `json:"top_groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Last7 != 1 || d.Last30 != 3 || d.Last365 != 20 || len(d.Victims) != 8 || len(d.TopGroups) != 2 || d.TopGroups[0].Count != 5 {
		t.Errorf("counts: 7d %d 30d %d 365d %d, %d shown, groups %+v", d.Last7, d.Last30, d.Last365, len(d.Victims), d.TopGroups)
	}
}

func TestParseRansomware(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	body := `[
	 {"post_title":"www.roancampingholidays.com","website":"www.roancampingholidays.com","activity":"Hospitality","group_name":"incransom","country":"NL",
	  "discovered":"2026-09-18T12:42:56.146503+00:00","description":"Roan Luxury Camping ...","post_url":"http://incblog.onion/blog/1"},
	 {"post_title":"Adviesbureau De Beuckelaer BV","website":"","activity":"Professional Services","group_name":"BrainCipher","country":"NL","discovered":"2026-08-31T19:53:18.536093+00:00"},
	 {"post_title":"x","website":"abc.onion","activity":"Other","group_name":"lockbit5","country":"NL","discovered":"2026-09-20T10:00:00+00:00"},
	 {"post_title":"Old BV","website":"old.nl","activity":"Other","group_name":"lockbit3","country":"NL","discovered":"2021-09-28T05:41:05+00:00"}
	]`
	v, err := parseRansomware([]byte(body), "NL", now)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(RansomData)
	if d.Total != 4 || len(d.Victims) != 3 || d.Victims[0].Name != "x" || d.Victims[0].Website != "" || d.Victims[1].Group != "incransom" ||
		d.Victims[2].GroupURL != "https://www.ransomware.live/group/braincipher" || d.Victims[1].Sector != "Hospitality" {
		t.Errorf("victims: %+v", d.Victims)
	}
	out, _ := json.Marshal(d)
	if strings.Contains(string(out), "onion") || strings.Contains(string(out), "Luxury") {
		t.Errorf("leak-site links or descriptions must not be passed on: %s", out)
	}
	if _, err := parseRansomware([]byte(`{"message": "1 per 1 minute"}`), "NL", now); err == nil || !strings.Contains(err.Error(), "1 per 1 minute") {
		t.Errorf("rate-limit reply must be an error: %v", err)
	}
}

func TestParsePollen(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 30, 0, 0, amsterdam)
	body := `{"hourly":{"time":["2026-09-26T09:00","2026-09-26T10:00","2026-09-27T12:00","2026-09-28T12:00","2026-09-29T12:00"],
	 "grass_pollen":[0.7,0.5,1.2,null,0.4],"birch_pollen":[0,0,0,0,0],"mugwort_pollen":[0.1,0.1,0.1,0.1,0.1],"alder_pollen":[0,0,0,0,-1],
	 "ozone":[61.4,88.6,190.2,null,70]}}`
	d, err := parsePollen([]byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Days) != 3 || d.Days[0].Date != "2026-09-26" || d.Days[0].Max["grass"] != 0.7 || d.Days[1].Max["grass"] != 1.2 || d.Now["grass"] != 0.5 {
		t.Errorf("pollen: %+v", d)
	}
	if o := d.Days; o[0].O3 == nil || *o[0].O3 != 89 || o[1].O3 == nil || *o[1].O3 != 190 || o[2].O3 != nil {
		t.Errorf("ozone per day (highest hour, rounded): %+v", o)
	}
	if _, err := parsePollen([]byte(`{"hourly":{}}`), now); err == nil {
		t.Error("no data: expected error")
	}
}

func TestGridOperators(t *testing.T) {
	for in, want := range map[string]string{"AMSTERDAM": "Amsterdam", "'S-GRAVENHAGE": "'s-Gravenhage", "ALPHEN AAN DEN RIJN": "Alphen aan den Rijn", "BERGEN OP ZOOM": "Bergen op Zoom"} {
		if got := titleNL(in); got != want {
			t.Errorf("titleNL(%q) = %q, want %q", in, got, want)
		}
	}
	liander := `{"features":[
	 {"attributes":{"STORING_NUMMER":8369814,"STORING_TYPE":"S","STORING_ENERGIESOORT":"Elektriciteit","STORING_STATUS":"monteur ter plaatse","STORING_DATUM_GEMELD":1790406240000,
	  "STORING_DATUM_SCHATTING":1790417100000,"STORING_OORZAAK":"Spontane storing","STORING_GETROFFEN_KLANTEN":"< 25","STORING_GETROFFEN_PLAATSEN":"AMSTERDAM,DIEMEN"}},
	 {"attributes":{"STORING_NUMMER":2,"STORING_TYPE":"P","STORING_ENERGIESOORT":"Gas","STORING_STATUS":"gepland","STORING_DATUM_GEMELD":1790406240000,"STORING_OORZAAK":"Nog niet bekend"}},
	 {"attributes":{"STORING_NUMMER":3,"STORING_TYPE":"S","STORING_ENERGIESOORT":"Elektriciteit","STORING_STATUS":"opgelost","STORING_DATUM_GEMELD":1790406240000}}]}`
	l, err := parseLianderOutages([]byte(liander))
	if err != nil {
		t.Fatal(err)
	}
	act, plan := splitPlanned(l)
	if len(act) != 1 || len(plan) != 1 || act[0].Places != "Amsterdam, Diemen" || act[0].Energy != "elektriciteit" || act[0].Estimate == nil || act[0].Customers != "< 25" ||
		plan[0].Energy != "gas" || plan[0].Cause != "" {
		t.Errorf("liander: %+v / %+v", act, plan)
	}
	if _, err := parseLianderOutages([]byte(`{"error":{"code":400,"message":"Invalid query"}}`)); err == nil || !strings.Contains(err.Error(), "Invalid query") {
		t.Errorf("ArcGIS error must be reported: %v", err)
	}
	stedin := `[{"id":"6ab7722b21fe7d1a33aa41b0","incidentType":"Interruption","utility":"Electricity","startTime":"2026-09-26T07:19:48.521+00:00",
	 "timeIndication":"26-09-2026 tussen 13:15 en 13:45 uur","postalCodes":["2994 GB","2994 GA"],"hasUpdates":false},
	 {"id":"x","incidentType":"Maintenance","utility":"Gas","startTime":"2026-09-25T06:40:00+00:00","timeIndication":"28-09-2026","postalCodes":["2411 BH"]}]`
	s, err := parseStedinPlace([]byte(stedin), "Barendrecht")
	if err != nil {
		t.Fatal(err)
	}
	act, plan = splitPlanned(s)
	if len(act) != 1 || act[0].EstText != "26-09-2026 tussen 13:15 en 13:45 uur" || act[0].Postcodes != 2 || act[0].Places != "Barendrecht" || len(plan) != 1 || plan[0].Energy != "gas" {
		t.Errorf("stedin: %+v / %+v", act, plan)
	}
}

func TestParseKNMIQuakes(t *testing.T) {
	body := "#EventID|Time|Latitude|Longitude|Depth/km|Author|Catalog|Contributor|ContributorID|MagType|Magnitude|MagAuthor|EventLocationName|EventType\n" +
		"knmi2026sjgb|2026-09-16T09:12:40.64655|53.46|6.10|0.0|||KNMI|knmi2026sjgb||||Schiermonnikoog|sonic boom\n" +
		"knmi2026sawe|2026-09-11T20:18:45.5|50.84|6.247|12.0|||KNMI|knmi2026sawe|MLs|1.0479842224688232||Eschweiler (Duitsland)|earthquake\n" +
		"knmi2026qmut|2026-08-21T03:50:39.0000|53.378|6.667|3.0|||KNMI|knmi2026qmut|MLn|2.75655436993489||Zandeweer|induced or triggered event\n" +
		"knmi2026pwsh|2026-08-12T10:45:59.2|50.72|6.192|0.0|||KNMI|knmi2026pwsh||||Aken (Duitsland)|quarry blast\n"
	q, err := parseKNMIQuakes([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 2 || q[0].ID != "knmi2026sawe" || *q[0].Mag != 1 || q[1].Place != "Zandeweer" || !q[1].Induced || *q[1].Mag != 2.8 || q[1].Depth != 3 ||
		q[1].URL != "https://www.knmi.nl/nederland-nu/seismologie/aardbevingen/knmi2026qmut" {
		t.Errorf("quakes: %+v", q)
	}
	if _, err := parseKNMIQuakes([]byte("<html>error</html>")); err == nil {
		t.Error("non-FDSN response: expected error")
	}
}

func TestParseIODAEvents(t *testing.T) {
	body := `{"type":"outages.events","error":null,"data":[
	 {"location":"asn/31615","start":1790142600,"duration":10500,"datasource":"bgp","status":0,"score":2414.4},
	 {"location":"asn/31615","start":1790167800,"duration":3600,"datasource":"ping-slash24","score":1443}]}`
	ev, err := parseIODAEvents([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 || ev[0].Datasource != "ping-slash24" || ev[1].Duration != 10500 || ev[1].Score != 2414 {
		t.Errorf("events: %+v", ev)
	}
	if _, err := parseIODAEvents([]byte(`{"error":"bad entity","data":null}`)); err == nil {
		t.Error("IODA error: expected error")
	}
	if !tls12Hosts["api.ioda.inetintel.cc.gatech.edu"] {
		t.Error("IODA must use the TLS 1.2 client")
	}
}

func TestParseEconomy(t *testing.T) {
	es := `{"label":"HICP","value":{"0":2.5,"1":3.0,"2":2.8},"dimension":{"time":{"category":{"index":{"2026-07":1,"2026-06":0,"2026-08":2}}}}}`
	p, err := parseEurostat([]byte(es))
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 3 || p[0].Period != "2026-06" || p[2].Value != 2.8 {
		t.Errorf("eurostat: %+v", p)
	}
	if _, err := parseEurostat([]byte(`{"error":[{"status":400,"label":"INVALID_QUERY"}]}`)); err == nil {
		t.Error("eurostat error: expected error")
	}
	ecb := "KEY,FREQ,REF_AREA,CURRENCY,PROVIDER_FM,INSTRUMENT_FM,PROVIDER_FM_ID,DATA_TYPE_FM,TIME_PERIOD,OBS_VALUE\n" +
		"FM.D.U2.EUR.4F.KR.DFR.LEV,D,U2,EUR,4F,KR,DFR,LEV,2026-09-26,2.5\nFM.D.U2.EUR.4F.KR.DFR.LEV,D,U2,EUR,4F,KR,DFR,LEV,2026-09-15,2.25\n"
	q, err := parseECBCSV([]byte(ecb))
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 2 || q[1].Period != "2026-09-26" || q[1].Value != 2.5 || q[0].Value != 2.25 {
		t.Errorf("ecb: %+v", q)
	}
	if _, err := parseECBCSV([]byte("<html>no</html>")); err == nil {
		t.Error("ecb non-CSV: expected error")
	}
}

func TestParseSpark(t *testing.T) {
	body := `{"ADYEN.AS":{"timestamp":[1790319600],"symbol":"ADYEN.AS","chartPreviousClose":843.5,"close":[875.5],"fulldayChange":32.0,"fulldayChangePercent":3.794,"fulldayPrice":875.5},
	 "^AEX":{"timestamp":[1790352302],"symbol":"^AEX","chartPreviousClose":1106.83,"close":[1112.09]},
	 "BAD.AS":{"symbol":"BAD.AS","close":[]},
	 "OTHER.AS":{"symbol":"OTHER.AS","close":[1],"fulldayPrice":1}}`
	q, err := parseSpark([]byte(body), map[string]string{"ADYEN.AS": "Adyen", "^AEX": "AEX", "BAD.AS": "Bad"})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Quote{}
	for _, x := range q {
		by[x.Symbol] = x
	}
	if len(q) != 2 || by["ADYEN.AS"].ChangePct != 3.79 || by["ADYEN.AS"].Name != "Adyen" || by["^AEX"].Price != 1112.09 || by["^AEX"].ChangePct != 0.48 {
		t.Errorf("spark: %+v", q)
	}
	if !marketSymbolRe.MatchString("^STOXX50E") || !marketSymbolRe.MatchString("BZ=F") || !marketSymbolRe.MatchString("BTC-EUR") || marketSymbolRe.MatchString("a b") {
		t.Error("symbol validation")
	}
}

func TestParseTKActivities(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, amsterdam) // Friday
	body := `{"value":[
	 {"Nummer":"2026A00001","Soort":"Werkbezoek","Onderwerp":"Brussel","Status":"Gepland","Aanvangstijd":"2026-09-25T10:00:00+02:00"},
	 {"Nummer":"2026A00002","Soort":"Inbreng schriftelijk overleg","Onderwerp":"x","Status":"Gepland","Aanvangstijd":"2026-09-25T12:00:00+02:00"},
	 {"Nummer":"2026A00003","Soort":"Notaoverleg","Onderwerp":"Defensienota (geannuleerd)","Status":"Geannuleerd","Aanvangstijd":"2026-09-25T14:30:00+02:00"},
	 {"Nummer":"2026A00005","Soort":"Plenair debat (wetgeving)","Onderwerp":"Postwet","Status":"Gepland","Aanvangstijd":"2026-09-29T14:00:00+02:00"},
	 {"Nummer":"2026A00004","Soort":"Commissiedebat","Onderwerp":"Politie","Voortouwafkorting":"J&V","Status":"Gepland","Aanvangstijd":"2026-09-29T10:00:00+02:00"},
	 {"Nummer":"bad","Soort":"Commissiedebat","Onderwerp":"x","Aanvangstijd":"2026-09-29T10:00:00+02:00"}
	]}`
	day, acts, err := parseTKActivities([]byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	if day != "2026-09-29" || len(acts) != 2 || acts[0].Number != "2026A00004" || acts[0].Committee != "J&V" {
		t.Fatalf("next sitting day: %s %+v", day, acts)
	}
	if acts[1].URL != "https://www.tweedekamer.nl/debat_en_vergadering/plenaire_vergaderingen/details/activiteit?id=2026A00005" ||
		acts[0].URL != "https://www.tweedekamer.nl/debat_en_vergadering/commissievergaderingen/details?id=2026A00004" {
		t.Errorf("urls: %s | %s", acts[0].URL, acts[1].URL)
	}
	// a meeting today that still takes place: today is shown, cancelled ones included and marked
	body2 := strings.Replace(body, `"Soort":"Werkbezoek","Onderwerp":"Brussel"`, `"Soort":"Commissiedebat","Onderwerp":"Brussel"`, 1)
	day, acts, _ = parseTKActivities([]byte(body2), now)
	if day != "2026-09-25" || len(acts) != 2 || !acts[1].Cancelled || acts[1].Subject != "Defensienota" {
		t.Errorf("today: %s %+v", day, acts)
	}
}

func TestParseTKVotes(t *testing.T) {
	body := `{"value":[
	 {"BesluitSoort":"Stemmen - aangenomen","GewijzigdOp":"2026-09-25T08:00:00Z","Zaak":[{"Nummer":"2026Z19805","Soort":"Motie","Onderwerp":"Motie van de leden A en B over C","Document":[{"DocumentNummer":"2026D45844"}]}],
	  "Agendapunt":{"Activiteit":{"Datum":"2026-09-24T00:00:00+02:00"}}},
	 {"BesluitSoort":"Stemmen - verworpen","GewijzigdOp":"2026-09-24T08:00:00Z","Zaak":[{"Nummer":"2026Z19970","Soort":"Motie","Onderwerp":"Motie van het lid F","Document":[]}]},
	 {"BesluitSoort":"Stemmen - aangenomen","GewijzigdOp":"2026-09-24T08:00:00Z","Zaak":[]}
	]}`
	v, err := parseTKVotes([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 || v[0].Result != "aangenomen" || v[1].Result != "verworpen" ||
		v[0].URL != "https://www.tweedekamer.nl/kamerstukken/detail?id=2026Z19805&did=2026D45844" ||
		v[1].URL != "https://www.tweedekamer.nl/kamerstukken/detail?id=2026Z19970" || !v[0].Date.Equal(time.Date(2026, 9, 23, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("votes: %+v", v)
	}
}

func TestOutageParsers(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	sp, err := parseStatuspage([]byte(`{"status":{"indicator":"minor","description":"Minor Service Outage"},
	  "incidents":[{"name":"Access updates delayed","status":"monitoring","impact":"minor","shortlink":"https://stspg.io/x","updated_at":"2026-09-24T11:00:00Z"},
	               {"name":"<b>Old</b>","status":"resolved","shortlink":"javascript:alert(1)","updated_at":"2026-09-24T09:00:00Z"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	o := sp.(OutageData)
	if o.Status != "minor" || len(o.Incidents) != 2 || o.Incidents[0].Status != "hersteld, wordt gevolgd" || o.Incidents[1].Title != "Old" ||
		o.Incidents[1].URL != "" || !o.Incidents[1].Resolved {
		t.Errorf("statuspage: %+v", o)
	}
	if _, err := parseStatuspage([]byte(`{"status":{"indicator":"??"}}`)); err == nil {
		t.Error("unknown indicator must be an error")
	}
	m, err := parseM365([]byte(`[{"ServiceDisplayName":"Outlook.com","Status":"Operational"},
	  {"ServiceDisplayName":"Teams (consumer)","Status":"ServiceDegradation","Title":"Berichten vertraagd","LastUpdatedTime":"2026-09-24T11:30:00Z"}]`))
	if o := m.(OutageData); err != nil || o.Status != "minor" || len(o.Incidents) != 1 || o.Incidents[0].Title != "Teams (consumer): Berichten vertraagd" {
		t.Errorf("m365: %v %+v", err, m)
	}
	rss := `<rss><channel>
	  <item><title><![CDATA[Service impact: Increased error rates (eu-west-1)]]></title><link>https://health.aws.amazon.com/1</link><pubDate>Thu, 24 Sep 2026 10:00:00 GMT</pubDate></item>
	  <item><title><![CDATA[Service is operating normally: [RESOLVED] Elevated latency]]></title><link>https://health.aws.amazon.com/2</link><pubDate>Thu, 24 Sep 2026 09:00:00 GMT</pubDate></item>
	  <item><title>Old incident</title><link>https://health.aws.amazon.com/3</link><pubDate>Mon, 21 Sep 2026 09:00:00 GMT</pubDate></item></channel></rss>`
	r, err := parseStatusRSS([]byte(rss), now)
	if o := r.(OutageData); err != nil || o.Status != "minor" || len(o.Incidents) != 2 || o.Incidents[0].Resolved || !o.Incidents[1].Resolved {
		t.Errorf("rss: %v %+v", err, r)
	}
	empty, err := parseStatusRSS([]byte(`<rss><channel><title>Azure Status</title></channel></rss>`), now)
	if o := empty.(OutageData); err != nil || o.Status != "ok" || len(o.Incidents) != 0 {
		t.Errorf("empty feed = no incidents: %v %+v", err, empty)
	}
}

type countingWriter struct {
	http.ResponseWriter
	n int
}

func (c *countingWriter) Write(b []byte) (int, error) {
	c.n += len(b)
	return c.ResponseWriter.Write(b)
}

// Duplicate function declarations are legal JavaScript: the last one silently wins.
// That once replaced the settings' loadHealth with the Gezondheid panel loader.
func TestFrontendNoDuplicateTopLevelNames(t *testing.T) {
	scripts := inlineScriptRe.FindAllSubmatch(indexHTML, -1)
	main := string(scripts[len(scripts)-1][1])
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^(?:async\s+)?function\s+(\w+)|^(?:const|let)\s+(\w+)\s*=`).FindAllStringSubmatch(main, -1) {
		name := m[1] + m[2]
		if seen[name] {
			t.Errorf("top-level name %q is declared twice in web/index.html", name)
		}
		seen[name] = true
	}
	if len(seen) < 50 {
		t.Errorf("scanner found only %d names; pattern broken?", len(seen))
	}
}

// ---------------------------------------------------------------------------
// Alarmeringen (Zwaailicht P2000)

func zwaaiEntry(emoji, title, summary, svc, city, at string) string {
	return `<entry><title>` + emoji + ` ` + title + `</title><summary>` + summary + `</summary>
	<link href="https://zwaailicht.nl/` + city + `/x/` + at + `"/><category term="` + svc + `"/><category term="` + city + `"/><updated>` + at + `</updated></entry>`
}

func TestAlarms(t *testing.T) {
	cityFeed := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Utrecht</title>` +
		zwaaiEntry("🚑", "Spoedmelding ambulance in Utrecht", "Ambulance met spoed in Utrecht. Ingezet: Ambulance-09-125. Gemeld om 16:59.", "ambulance", "utrecht", "2026-09-24T14:59:21Z") +
		zwaaiEntry("🚑", "Ambulance naar Biltstraat in Utrecht", "Ambulance voor gepland vervoer naar Biltstraat in Utrecht. Gemeld om 16:40.", "ambulance", "utrecht", "2026-09-24T14:40:00Z") +
		zwaaiEntry("🚑", "Ambulance ingezet in Utrecht", "Ambulance zonder spoed in Utrecht. Ingezet: 09-134. Gemeld om 16:32.", "ambulance", "utrecht", "2026-09-24T14:32:20Z") +
		zwaaiEntry("🔥", "Gaslekkage bij Oudegracht in Utrecht", "Brandweer met spoed naar Oudegracht in Utrecht. Ingezet: TS 09-1831. Let op: incident met gevaarlijke stoffen. Gemeld om 16:20.", "brandweer", "utrecht", "2026-09-24T14:20:00Z") +
		zwaaiEntry("🚔", "Verkeersongeval op Waterlinieweg in Utrecht", "Politie ter plaatse naar Waterlinieweg in Utrecht (ernstig letsel). Gemeld om 16:10.", "politie", "utrecht", "2026-09-24T14:10:00Z") +
		`<entry><title>Elders</title><link href="https://evil.example/x"/><category term="politie"/><category term="utrecht"/><updated>2026-09-24T14:05:00Z</updated></entry>` +
		`</feed>`
	lifeFeed := `<feed xmlns="http://www.w3.org/2005/Atom">` +
		zwaaiEntry("🚁", "Ambulance met spoed naar Het Jaagpad in Linschoten", "Traumahelikopter met spoed naar Het Jaagpad in Linschoten. Ingezet: LifeLiner 2. Gemeld om 18:11.", "lifeliner", "linschoten", "2026-09-24T16:11:40Z") +
		zwaaiEntry("🚁", "Ambulance met spoed naar Anton Mauvelaan in Vlissingen", "Traumahelikopter met spoed. Ingezet: LifeLiner 2.", "lifeliner", "vlissingen", "2026-09-24T15:30:34Z") +
		zwaaiEntry("🚁", "Ambulance met spoed naar Halleyweg in Dordrecht", "Traumahelikopter met spoed.", "lifeliner", "dordrecht", "2026-09-24T14:06:25Z") + `</feed>`
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.URL.Path {
		case "/feed/meldingen/utrecht.xml":
			io.WriteString(w, cityFeed)
		case "/feed/meldingen/lifeliner.xml":
			io.WriteString(w, lifeFeed)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `<feed xmlns="http://www.w3.org/2005/Atom"><title>Feed niet gevonden</title></feed>`)
		}
	}))
	defer srv.Close()
	a := newTestApp(t, validConfig+"\nalarms: { city: utrecht, base: \""+srv.URL+"/feed/meldingen/\" }\n")
	h := a.routes("/")

	rec := get(h, "GET", "/api/alarms", nil)
	var resp struct {
		City   string       `json:"city"`
		Groups []AlarmGroup `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != 200 {
		t.Fatalf("%d %v %s", rec.Code, err, rec.Body.String())
	}
	g := map[string]AlarmGroup{}
	var order []string
	for _, x := range resp.Groups {
		g[x.ID] = x
		order = append(order, x.ID)
	}
	if strings.Join(order, ",") != "brandweer,ambulance,politie,lifeliner" || resp.City != "utrecht" {
		t.Fatalf("groups: %v city %q", order, resp.City)
	}
	amb := g["ambulance"].Items
	if len(amb) != 2 || amb[0].Title != "Spoedmelding ambulance in Utrecht" || amb[0].Urgency != "spoed" || amb[0].Units != "Ambulance-09-125" || amb[1].Urgency != "gepland" {
		t.Errorf("ambulance (max 2, newest first, emoji stripped): %+v", amb)
	}
	if b := g["brandweer"].Items; len(b) != 1 || b[0].Detail != "incident met gevaarlijke stoffen" || b[0].Urgency != "spoed" {
		t.Errorf("brandweer: %+v", b)
	}
	if p := g["politie"].Items; len(p) != 1 || p[0].Detail != "ernstig letsel" || p[0].URL == "" || strings.Contains(p[0].URL, "evil") {
		t.Errorf("politie (foreign link dropped): %+v", p)
	}
	if l := g["lifeliner"]; l.Scope != "national" || len(l.Items) != 2 || l.Items[0].City != "linschoten" || l.Items[0].Units != "LifeLiner 2" {
		t.Errorf("lifeliner falls back to the latest national flights: %+v", l)
	}
	before := hits
	get(h, "GET", "/api/alarms?city=utrecht", nil)
	if hits != before {
		t.Error("second request within the interval must be served from cache")
	}
	if rec := get(h, "GET", "/api/alarms?city=nergenshuizen", nil); rec.Code != 404 || !strings.Contains(rec.Body.String(), "niet gevonden") {
		t.Errorf("unknown city: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"../etc/passwd", "Den Haag", "a", "lifeliner"} {
		if rec := get(h, "GET", "/api/alarms?city="+url.QueryEscape(bad), nil); rec.Code != 400 {
			t.Errorf("city %q must be rejected: %d", bad, rec.Code)
		}
	}
	// a Lifeliner flight in the city itself takes precedence over the national list
	withLife := groupAlarms([]Alarm{{Title: "x", service: "lifeliner", Time: time.Now()}}, []Alarm{{Title: "elders"}})
	if l := withLife[3]; l.Scope != "city" || len(l.Items) != 1 || l.Items[0].Title != "x" {
		t.Errorf("local lifeliner: %+v", l)
	}
}

// Ambulance: 3 alerts a minute, the feed shows the newest 50 (~17 min), polled every 3 min.
func TestP2KHourlyCounter(t *testing.T) {
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	alertAt := func(i int) time.Time { return start.Add(time.Duration(i) * 20 * time.Second) } // 3 per minute
	feed := func(now time.Time) []Alarm {
		var out []Alarm
		for i := int(now.Sub(start) / (20 * time.Second)); i >= 0 && len(out) < 50; i-- {
			out = append(out, Alarm{id: fmt.Sprint(i), Time: alertAt(i), service: "ambulance"})
		}
		return out
	}
	c := &p2kCounter{}
	now := start.Add(60 * time.Minute)
	c.add(feed(now), now)
	if n, done := c.count(now, "ambulance"); n != 50 || done {
		t.Errorf("first poll covers ~17 min: %d complete=%v (want 50, incomplete)", n, done)
	}
	for m := 63; m <= 123; m += 3 { // an hour of polls
		now = start.Add(time.Duration(m) * time.Minute)
		c.add(feed(now), now)
	}
	if n, done := c.count(now, "ambulance"); n != 180 || !done { // 3/min × 60 min, each alert counted once
		t.Errorf("after an hour of polls: %d complete=%v (want 180, complete)", n, done)
	}
	now = now.Add(30 * time.Minute) // 30-minute outage, longer than the feed covers
	c.add(feed(now), now)
	if _, done := c.count(now, "ambulance"); done {
		t.Error("a gap longer than the feed's reach must make the count incomplete")
	}
	if _, done := c.count(now.Add(20*time.Minute), "ambulance"); done {
		t.Error("a count without a recent poll must not claim to be complete")
	}
	life := &p2kCounter{} // Lifeliner: 3 alerts in two days, feed not full
	life.add([]Alarm{{id: "a", Time: start.Add(-10 * time.Minute)}, {id: "b", Time: start.Add(-5 * time.Hour)}, {id: "c", Time: start.Add(-30 * time.Hour)}}, start)
	if n, done := life.count(start, ""); n != 1 || !done {
		t.Errorf("quiet feed: %d complete=%v (want 1, complete)", n, done)
	}
}

// An area of several cities: counts per service are summed; complete only if every city is.
func TestP2KAreaCounts(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	a := &App{p2k: newP2KCounters()}
	a.p2k["den-haag"], a.p2k["rijswijk"] = &p2kCounter{}, &p2kCounter{}
	a.p2k["den-haag"].add([]Alarm{{id: "1", service: "ambulance", Time: now.Add(-5 * time.Minute)}, {id: "2", service: "brandweer", Time: now.Add(-50 * time.Minute)},
		{id: "3", service: "ambulance", Time: now.Add(-70 * time.Minute)}}, now)
	a.p2k["rijswijk"].add([]Alarm{{id: "4", service: "ambulance", Time: now.Add(-10 * time.Minute)}}, now)
	got := map[string]P2KCount{}
	for _, c := range a.p2kCounts([]string{"den-haag", "rijswijk"}, now) {
		got[c.ID] = c
	}
	if got["ambulance"].Count != 2 || got["brandweer"].Count != 1 || got["knrm"].Count != 0 || !got["ambulance"].Complete {
		t.Errorf("area counts: %+v", got)
	}
	if c := a.p2kCounts([]string{"den-haag", "delft"}, now); c[0].Complete { // delft never polled
		t.Error("a city that was never polled makes the area count incomplete")
	}
}
