package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// ---------------------------------------------------------------------------
// Wikipedia context for trending topics: a short summary from the Dutch
// Wikipedia (REST API, open, no key). Only terms that were trending chips in the
// last hour can be looked up, so the endpoint is no general Wikipedia proxy; results
// (including "nothing found") are cached for 24 hours per term.
//
// Lookup: the page summary for the term itself; for a disambiguation page or no
// page, the best search hit whose title contains every word of the term plus at
// most one more ("Trump" → "Donald Trump", not "Grand Prix" → "Grand Prix van
// Spanje"). Only a normal article is shown, never a disambiguation page.

type WikiSummary struct {
	Found       bool   `json:"found"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Extract     string `json:"extract,omitempty"`
	URL         string `json:"url,omitempty"`
	Thumb       string `json:"thumb,omitempty"`
}

var errWikiNotFound = errors.New("not found")

func wikiWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// wikiTitleFits: the title holds every word of the term and at most one more.
func wikiTitleFits(term, title string) bool {
	tw, have := wikiWords(term), map[string]bool{}
	ti := wikiWords(title)
	for _, w := range ti {
		have[w] = true
	}
	for _, w := range tw {
		if !have[w] {
			return false
		}
	}
	return len(tw) > 0 && len(ti) <= len(tw)+1
}

// parseWikiSummary returns errWikiNotFound for anything but a normal article.
func parseWikiSummary(body []byte, base string) (WikiSummary, error) {
	var d struct {
		Type        string `json:"type"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Extract     string `json:"extract"`
		Thumbnail   struct {
			Source string `json:"source"`
		} `json:"thumbnail"`
		ContentURLs struct {
			Desktop struct {
				Page string `json:"page"`
			} `json:"desktop"`
		} `json:"content_urls"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return WikiSummary{}, err
	}
	if d.Type != "standard" || strings.TrimSpace(d.Extract) == "" {
		return WikiSummary{}, errWikiNotFound
	}
	s := WikiSummary{Found: true, Title: truncate(plainText(d.Title), 100), Description: truncate(plainText(d.Description), 120),
		Extract: truncate(plainText(d.Extract), 420)}
	// The link must point to the configured Wikipedia, the photo to Wikimedia.
	if u, err := url.Parse(d.ContentURLs.Desktop.Page); err == nil && base != "" && strings.HasPrefix(u.String(), base+"/") {
		s.URL = u.String()
	}
	if u, err := url.Parse(d.Thumbnail.Source); err == nil && u.Scheme == "https" && u.Host == "upload.wikimedia.org" {
		s.Thumb = u.String()
	}
	return s, nil
}

func (a *App) wikiSummary(ctx context.Context, base, title string) (WikiSummary, error) {
	u := base + "/api/rest_v1/page/summary/" + url.PathEscape(strings.ReplaceAll(title, " ", "_")) + "?redirect=true"
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
	if resp != nil && resp.Status == http.StatusNotFound {
		return WikiSummary{}, errWikiNotFound
	}
	if err != nil {
		return WikiSummary{}, err
	}
	return parseWikiSummary(resp.Body, base)
}

func (a *App) wikiLookup(ctx context.Context, term string) (WikiSummary, error) {
	base := strings.TrimRight(a.config().Trending.Wikipedia.URL, "/")
	s, err := a.wikiSummary(ctx, base, term)
	if !errors.Is(err, errWikiNotFound) {
		return s, err
	}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "/w/rest.php/v1/search/page?limit=5&q=" + url.QueryEscape(term), Accept: "application/json"})
	if err != nil {
		return WikiSummary{}, err
	}
	var res struct {
		Pages []struct {
			Key   string `json:"key"`
			Title string `json:"title"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(resp.Body, &res); err != nil {
		return WikiSummary{}, err
	}
	for _, p := range res.Pages {
		if p.Key == "" || strings.EqualFold(p.Title, term) || !wikiTitleFits(term, p.Title) {
			continue
		}
		if s, err := a.wikiSummary(ctx, base, p.Key); !errors.Is(err, errWikiNotFound) {
			return s, err
		}
	}
	return WikiSummary{}, errWikiNotFound
}

func (a *App) handleWiki(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Trending.Enabled || !cfg.Trending.Wikipedia.Enabled {
		writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": false})
		return
	}
	match, ok := a.trendedRecently(r.URL.Query().Get("term"), time.Now())
	key := strings.ToLower(match)
	if match == "" || (!ok && !a.wikiCache.has(key)) { // only terms that were trending chips in the last hour
		writeError(w, r, http.StatusNotFound, "geen trending onderwerp")
		return
	}
	ip := a.clientIP(r)
	ctx := context.WithoutCancel(r.Context())
	s, _, _, err := a.wikiCache.get(key, 24*time.Hour, func() (WikiSummary, error) {
		if !a.wx.limiter.allow(ip) {
			return WikiSummary{}, errRateLimited
		}
		s, err := a.wikiLookup(ctx, match)
		if errors.Is(err, errWikiNotFound) {
			return WikiSummary{}, nil // cached as "nothing found"
		}
		return s, err
	})
	switch {
	case errors.Is(err, errRateLimited):
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
		return
	case err != nil:
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": true, "term": match, "error": fmt.Sprintf("Wikipedia: %v", shortErr(err))})
		return
	}
	if s.Thumb != "" {
		if cfg.Features.ProxyImages { // the photo comes from this server, never directly from Wikimedia
			s.Thumb = "api/img?u=" + base64.RawURLEncoding.EncodeToString([]byte(s.Thumb)) + "&s=" + a.images.sign(s.Thumb)
		} else {
			s.Thumb = ""
		}
	}
	search := strings.TrimRight(cfg.Trending.Wikipedia.URL, "/") + "/w/index.php?search=" + url.QueryEscape(match)
	writeJSON(w, r, http.StatusOK, 3600, map[string]any{"enabled": true, "term": match, "summary": s, "search": search})
}
