package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Sportagenda: Formula 1 from the Jolpica API (the open successor of the Ergast
// API; no key) and the important races and tournaments of road cycling, mountain
// biking, athletics and football (NK, classics, grand tours, EK, WK) from a
// calendar in config.yaml, because the UCI, World Athletics, UEFA and FIFA have no
// open calendar API.
// During and just after a championship the panel shows the latest matching
// headlines from the news the dashboard already collects.

var sportNames = map[string]string{"f1": "Formule 1", "road": "Wielrennen", "mtb": "Mountainbike", "athletics": "Atletiek", "football": "Voetbal"}

// sportUpcoming: how many coming events the panel shows per sport.
const sportUpcoming = 3

type SportEvent struct {
	Sport     string   `yaml:"sport" json:"sport"`
	Name      string   `yaml:"name" json:"name"`
	Start     string   `yaml:"start" json:"start"` // YYYY-MM-DD
	End       string   `yaml:"end" json:"end"`
	Place     string   `yaml:"place" json:"place,omitempty"`
	URL       string   `yaml:"url" json:"url,omitempty"`
	Note      string   `yaml:"note" json:"note,omitempty"`           // e.g. the race days of a championship
	Tentative bool     `yaml:"tentative" json:"tentative,omitempty"` // dates not yet confirmed by the organiser
	Keywords  []string `yaml:"keywords" json:"-"`
}

type F1Session struct {
	Name string    `json:"name"`
	Time time.Time `json:"time"`
}

type F1Race struct {
	Round    int         `json:"round"`
	Name     string      `json:"name"`
	Circuit  string      `json:"circuit"`
	Place    string      `json:"place"`
	Time     time.Time   `json:"time"`
	Sessions []F1Session `json:"sessions,omitempty"`
	Podium   []F1Result  `json:"podium,omitempty"`
}

type F1Result struct {
	Pos    int     `json:"pos"`
	Driver string  `json:"driver"`
	Team   string  `json:"team"`
	Points float64 `json:"points,omitempty"`
}

type F1Data struct {
	Season    string     `json:"season"`
	Next      *F1Race    `json:"next,omitempty"`
	Last      *F1Race    `json:"last,omitempty"`
	Standings []F1Result `json:"standings"`
}

type ergastSession struct {
	Date string `json:"date"`
	Time string `json:"time"`
}

func (s ergastSession) at() (time.Time, bool) {
	if s.Date == "" {
		return time.Time{}, false
	}
	tm := s.Time
	if tm == "" {
		tm = "12:00:00Z"
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", s.Date+"T"+tm)
	return t, err == nil
}

type ergastRace struct {
	Season  string `json:"season"`
	Round   string `json:"round"`
	Name    string `json:"raceName"`
	Circuit struct {
		Name     string `json:"circuitName"`
		Location struct {
			Locality string `json:"locality"`
			Country  string `json:"country"`
		} `json:"Location"`
	} `json:"Circuit"`
	ergastSession
	FP1     *ergastSession `json:"FirstPractice"`
	Quali   *ergastSession `json:"Qualifying"`
	Sprint  *ergastSession `json:"Sprint"`
	SprintQ *ergastSession `json:"SprintQualifying"`
	Results []struct {
		Pos    string `json:"position"`
		Driver struct {
			Given  string `json:"givenName"`
			Family string `json:"familyName"`
		} `json:"Driver"`
		Team struct {
			Name string `json:"name"`
		} `json:"Constructor"`
	} `json:"Results"`
}

func (r ergastRace) race() F1Race {
	round, _ := strconv.Atoi(r.Round)
	out := F1Race{Round: round, Name: plainText(r.Name), Circuit: plainText(r.Circuit.Name),
		Place: plainText(strings.TrimSpace(r.Circuit.Location.Locality + ", " + r.Circuit.Location.Country))}
	out.Time, _ = r.ergastSession.at()
	for _, s := range []struct {
		name string
		s    *ergastSession
	}{{"Sprintkwalificatie", r.SprintQ}, {"Sprint", r.Sprint}, {"Kwalificatie", r.Quali}} {
		if s.s != nil {
			if t, ok := s.s.at(); ok {
				out.Sessions = append(out.Sessions, F1Session{Name: s.name, Time: t})
			}
		}
	}
	sort.Slice(out.Sessions, func(i, j int) bool { return out.Sessions[i].Time.Before(out.Sessions[j].Time) })
	for _, res := range r.Results {
		pos, _ := strconv.Atoi(res.Pos)
		if pos >= 1 && pos <= 3 {
			out.Podium = append(out.Podium, F1Result{Pos: pos, Driver: plainText(res.Driver.Given + " " + res.Driver.Family), Team: plainText(res.Team.Name)})
		}
	}
	return out
}

func parseF1Schedule(body []byte, now time.Time) (*F1Race, string, error) {
	var r struct {
		MRData struct {
			RaceTable struct {
				Season string       `json:"season"`
				Races  []ergastRace `json:"Races"`
			} `json:"RaceTable"`
		} `json:"MRData"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, "", err
	}
	races := r.MRData.RaceTable.Races
	if len(races) == 0 {
		return nil, "", errors.New("f1: empty schedule")
	}
	for _, rc := range races {
		if t, ok := rc.ergastSession.at(); ok && t.Add(3*time.Hour).After(now) {
			n := rc.race()
			return &n, r.MRData.RaceTable.Season, nil
		}
	}
	return nil, r.MRData.RaceTable.Season, nil // season over
}

func parseF1Last(body []byte) (*F1Race, error) {
	var r struct {
		MRData struct {
			RaceTable struct {
				Races []ergastRace `json:"Races"`
			} `json:"RaceTable"`
		} `json:"MRData"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if len(r.MRData.RaceTable.Races) == 0 {
		return nil, nil
	}
	l := r.MRData.RaceTable.Races[0].race()
	return &l, nil
}

func parseF1Standings(body []byte) ([]F1Result, error) {
	var r struct {
		MRData struct {
			StandingsTable struct {
				Lists []struct {
					Standings []struct {
						Pos    string `json:"position"`
						Points string `json:"points"`
						Driver struct {
							Given  string `json:"givenName"`
							Family string `json:"familyName"`
						} `json:"Driver"`
						Teams []struct {
							Name string `json:"name"`
						} `json:"Constructors"`
					} `json:"DriverStandings"`
				} `json:"StandingsLists"`
			} `json:"StandingsTable"`
		} `json:"MRData"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := []F1Result{}
	if len(r.MRData.StandingsTable.Lists) == 0 {
		return out, nil
	}
	for _, s := range r.MRData.StandingsTable.Lists[0].Standings {
		pos, _ := strconv.Atoi(s.Pos)
		pts, _ := strconv.ParseFloat(s.Points, 64)
		team := ""
		if len(s.Teams) > 0 {
			team = s.Teams[len(s.Teams)-1].Name
		}
		out = append(out, F1Result{Pos: pos, Driver: plainText(s.Driver.Given + " " + s.Driver.Family), Team: plainText(team), Points: pts})
		if len(out) == 5 {
			break
		}
	}
	return out, nil
}

func (a *App) runF1(ctx context.Context) error {
	const key = "f1:jolpica"
	base := strings.TrimSuffix(a.config().Sports.F1URL, "/")
	get := func(path string) ([]byte, error) {
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + path, Accept: "application/json"})
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
	var d F1Data
	var err error
	var body []byte
	if body, err = get("/current.json"); err == nil {
		d.Next, d.Season, err = parseF1Schedule(body, time.Now())
	}
	if err == nil {
		if body, err = get("/current/last/results.json"); err == nil {
			d.Last, err = parseF1Last(body)
		}
	}
	if err == nil {
		if body, err = get("/current/driverStandings.json"); err == nil {
			d.Standings, err = parseF1Standings(body)
		}
	}
	if err != nil {
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	a.threats.ok(key, d, "", "")
	return nil
}

// ---------------------------------------------------------------------------

type SportHeadline struct {
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Source    string    `json:"source"`
	Published time.Time `json:"published"`
}

type SportEventOut struct {
	SportEvent
	Status    string          `json:"status"` // upcoming | live | done
	Headlines []SportHeadline `json:"headlines,omitempty"`
}

// sportEvents: per sport the events that are on now or just ended (14 days), and the next three.
func (a *App) sportEvents(cfg *Config, now time.Time) []SportEventOut {
	today := now.In(amsterdam).Format("2006-01-02")
	var out []SportEventOut
	for _, sp := range cfg.Sports.Sports {
		var evs []SportEvent
		for _, e := range cfg.Sports.Events {
			if e.Sport == sp {
				evs = append(evs, e)
			}
		}
		sort.Slice(evs, func(i, j int) bool { return evs[i].Start < evs[j].Start })
		var show []*SportEvent
		upcoming := 0
		for i := range evs {
			e := &evs[i]
			endT, _ := time.Parse("2006-01-02", e.End)
			switch {
			case e.Start <= today && endT.AddDate(0, 0, 14).Format("2006-01-02") >= today: // on now, or ended in the last 14 days
				show = append(show, e)
			case e.Start > today && upcoming < sportUpcoming:
				show = append(show, e)
				upcoming++
			}
		}
		for _, e := range show {
			o := SportEventOut{SportEvent: *e, Status: "upcoming"}
			if e.Start <= today {
				o.Status = "live"
				if e.End < today {
					o.Status = "done"
				}
				o.Headlines = a.sportHeadlines(cfg, *e)
			}
			out = append(out, o)
		}
	}
	return out
}

// sportHeadlines: the newest headlines that mention one of the event's keywords, from the event start on.
func (a *App) sportHeadlines(cfg *Config, e SportEvent) []SportHeadline {
	if len(e.Keywords) == 0 {
		return nil
	}
	from, _ := time.ParseInLocation("2006-01-02", e.Start, amsterdam)
	from = from.AddDate(0, 0, -1)
	var ids []string
	names := map[string]string{}
	for _, s := range cfg.Sources {
		if s.IsEnabled() {
			ids = append(ids, s.ID)
			names[s.ID] = s.Name
		}
	}
	lists, _ := a.news.collect(ids)
	var out []SportHeadline
	seen := map[string]bool{}
	for _, l := range lists {
		for _, it := range l {
			if it.Published.Before(from) || seen[it.normTitle] {
				continue
			}
			low := strings.ToLower(it.Title)
			if slices.ContainsFunc(e.Keywords, func(k string) bool { return strings.Contains(low, strings.ToLower(k)) }) {
				seen[it.normTitle] = true
				out = append(out, SportHeadline{Title: it.Title, URL: it.URL, Source: names[it.Source], Published: it.Published})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func (a *App) handleSports(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Sports.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := map[string]any{"enabled": true, "sports": cfg.Sports.Sports, "events": a.sportEvents(cfg, time.Now())}
	if slices.Contains(cfg.Sports.Sports, "f1") {
		f := a.feedEntry("f1:jolpica")
		if v, ok := a.threats.get("f1:jolpica").Data.(F1Data); ok {
			f["data"] = v
		}
		e["f1"] = f
		if t, ok := f["fetched_at"]; ok {
			e["fetched_at"] = t
		}
	}
	if _, ok := e["fetched_at"]; !ok {
		e["fetched_at"] = time.Now().UTC().Truncate(time.Second)
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}

func validSportEvent(e SportEvent) error {
	if _, ok := sportNames[e.Sport]; !ok || e.Sport == "f1" {
		return fmt.Errorf("sports.events: sport %q must be road, mtb, athletics or football", e.Sport)
	}
	if len(e.Note) > 200 {
		return fmt.Errorf("sports.events: %q: note at most 200 characters", e.Name)
	}
	s, err1 := time.Parse("2006-01-02", e.Start)
	en, err2 := time.Parse("2006-01-02", e.End)
	if err1 != nil || err2 != nil || en.Before(s) || strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("sports.events: %q needs a name, start and end (YYYY-MM-DD, end not before start)", e.Name)
	}
	if e.URL != "" && !isHTTPURL(e.URL) {
		return fmt.Errorf("sports.events: %q: url must be http(s)", e.Name)
	}
	return nil
}
