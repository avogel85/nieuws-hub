package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Oplichting en phishing (in Datalekken): the warnings of the Fraudehelpdesk about current
// scams (fake calls, text messages, websites), from its public RSS feed of alerts.

type ScamAlert struct {
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
}

// parseScamAlerts keeps the warnings of the last 60 days, newest first (at most 10).
func parseScamAlerts(body []byte, now time.Time) ([]ScamAlert, error) {
	items, err := parseFeed(body, "")
	if err != nil {
		return nil, fmt.Errorf("fraudehelpdesk: %w", err)
	}
	out := []ScamAlert{}
	for _, it := range items {
		t, ok := parseDate(it.Date)
		title := strings.TrimSpace(html.UnescapeString(it.Title))
		if !ok || title == "" || !strings.HasPrefix(it.Link, "https://") || now.Sub(t) > 60*24*time.Hour {
			continue
		}
		out = append(out, ScamAlert{Title: title, URL: it.Link, Published: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	if len(out) > 10 {
		out = out[:10]
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Op deze dag (in Vandaag): a few events on today's date from the Dutch Wikipedia page of
// the day ("3 oktober", section Gebeurtenissen). Dutch Wikipedia has no "on this day"
// feed, so the page's wikitext is read once an hour through the standard API.

type DayEvent struct {
	Year int    `json:"year"`
	Text string `json:"text"`
}

type OnThisDay struct {
	Date   string     `json:"date"` // 2006-01-02, Amsterdam
	Title  string     `json:"title"`
	URL    string     `json:"url"`
	Events []DayEvent `json:"events"`
}

var dutchMonthNames = [12]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}

func dayPageTitle(t time.Time) string {
	return fmt.Sprintf("%d %s", t.Day(), dutchMonthNames[t.Month()-1])
}

var (
	wikiRefRe      = regexp.MustCompile(`(?s)<ref[^>]*/>|<ref[^>]*>.*?</ref>`)
	wikiTplRe      = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	wikiLinkRe     = regexp.MustCompile(`\[\[([^\]|]*\|)?([^\]]*)\]\]`)
	wikiExtLinkRe  = regexp.MustCompile(`\[https?://\S+ ([^\]]*)\]`)
	wikiTagRe      = regexp.MustCompile(`<[^>]+>`)
	wikiEventRe    = regexp.MustCompile(`^\[\[(\d{1,4})\]\]\s*[–—-]\s*(.+)$`)
	wikiSectionRe  = regexp.MustCompile(`(?m)^==\s*Gebeurtenissen\s*==\s*$`)
	wikiNextHeadRe = regexp.MustCompile(`(?m)^==[^=]`)
	dutchTopicRe   = regexp.MustCompile(`(?i)\b(nederland\w*|holland\w*|amsterdam|rotterdam|den haag|utrecht|fries\w*|zeeland|zeeuw\w*|limburg|groning\w*|brabant|gelder\w*|overijssel|drenthe|flevoland|eindhoven|leiden|delft|haarlem|nijmegen|maastricht|arnhem|oranje|willem|beatrix|juliana|wilhelmina|ajax|psv|feyenoord|elfstedentocht|afsluitdijk|deltawerken)\b`)
)

func cleanWikitext(s string) string {
	s = wikiRefRe.ReplaceAllString(s, "")
	for i := 0; i < 4 && wikiTplRe.MatchString(s); i++ { // nested templates, innermost first
		s = wikiTplRe.ReplaceAllString(s, "")
	}
	s = wikiLinkRe.ReplaceAllString(s, "$2")
	s = wikiExtLinkRe.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("'''", "", "''", "").Replace(s)
	s = wikiTagRe.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// parseDayEvents reads the list under "== Gebeurtenissen ==": lines like
// "** [[1990]] – [[Duitse hereniging|Duitsland]] wordt herenigd.".
func parseDayEvents(wikitext string) []DayEvent {
	loc := wikiSectionRe.FindStringIndex(wikitext)
	if loc == nil {
		return nil
	}
	sec := wikitext[loc[1]:]
	if n := wikiNextHeadRe.FindStringIndex(sec); n != nil {
		sec = sec[:n[0]]
	}
	var out []DayEvent
	for _, line := range strings.Split(sec, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "*:"))
		m := wikiEventRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		y, _ := strconv.Atoi(m[1])
		if txt := cleanWikitext(m[2]); len([]rune(txt)) >= 12 {
			if r := []rune(txt); len(r) > 220 {
				txt = string(r[:219]) + "…"
			}
			out = append(out, DayEvent{Year: y, Text: txt})
		}
	}
	return out
}

// pickDayEvents: up to n events, Dutch ones first (at most half), then the most recent
// others; shown oldest first.
func pickDayEvents(all []DayEvent, n int) []DayEvent {
	byRecent := append([]DayEvent(nil), all...)
	sort.SliceStable(byRecent, func(i, j int) bool { return byRecent[i].Year > byRecent[j].Year })
	var pick []DayEvent
	used := map[int]bool{}
	for i, e := range byRecent {
		if len(pick) < (n+1)/2 && dutchTopicRe.MatchString(e.Text) {
			pick, used[i] = append(pick, e), true
		}
	}
	for i, e := range byRecent {
		if len(pick) < n && !used[i] {
			pick = append(pick, e)
		}
	}
	sort.SliceStable(pick, func(i, j int) bool { return pick[i].Year < pick[j].Year })
	return pick
}

func (a *App) runOnThisDay(ctx context.Context) error {
	now := time.Now().In(amsterdam)
	base := strings.TrimRight(a.config().Today.WikiURL, "/")
	title := dayPageTitle(now)
	u := base + "/w/api.php?action=parse&prop=wikitext&format=json&formatversion=2&page=" + url.QueryEscape(strings.ReplaceAll(title, " ", "_"))
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
	if err == nil {
		var r struct {
			Parse struct {
				Wikitext string `json:"wikitext"`
			} `json:"parse"`
		}
		if err = json.Unmarshal(resp.Body, &r); err == nil {
			ev := parseDayEvents(r.Parse.Wikitext)
			if len(ev) < 3 {
				err = errors.New("wikipedia: no events on the day page")
			} else {
				a.threats.ok("wiki:onthisday", OnThisDay{Date: now.Format("2006-01-02"), Title: title,
					URL: base + "/wiki/" + url.PathEscape(strings.ReplaceAll(title, " ", "_")), Events: pickDayEvents(ev, 4)}, "", "")
				return nil
			}
		}
	}
	a.threats.fail("wiki:onthisday", err)
	return err
}
