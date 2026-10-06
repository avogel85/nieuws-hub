package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"time"
)

// ---------------------------------------------------------------------------
// Zee en getij (in Weer): the predicted high and low tides at the coastal station nearest to
// the visitor's weather location (Rijkswaterstaat water data, astronomical tide), and the
// sea temperature and waves there (Open-Meteo Marine). Both are open and need no key.

type coastStation struct {
	ID, Name string
	Lat, Lon float64
}

// Beaches and harbours along the coast and the Wadden Sea that publish an astronomical tide.
var coastStations = []coastStation{
	{"cadzand.2", "Cadzand", 51.379, 3.376}, {"vlissingen", "Vlissingen", 51.442, 3.6}, {"westkapelle", "Westkapelle", 51.521, 3.44},
	{"stellendam.buitenhaven", "Stellendam", 51.827, 4.033}, {"hoekvanholland", "Hoek van Holland", 51.977, 4.12},
	{"scheveningen", "Scheveningen", 52.099, 4.264}, {"noordwijk.meetpost", "Noordwijk", 52.273, 4.295},
	{"ijmuiden.buitenhaven", "IJmuiden", 52.463, 4.555}, {"petten.zuid", "Petten", 52.773, 4.65},
	{"denhelder.marsdiep", "Den Helder", 52.964, 4.745}, {"denoever.waddenzee.voorhaven", "Den Oever", 52.932, 5.046},
	{"texel.noordzee", "Texel", 53.121, 4.733}, {"vlieland.haven", "Vlieland", 53.296, 5.091},
	{"harlingen.waddenzee", "Harlingen", 53.176, 5.409}, {"terschelling.noordzee", "Terschelling", 53.442, 5.333},
	{"ameland.nes", "Ameland", 53.43, 5.759}, {"lauwersoog.waddenzee", "Lauwersoog", 53.408, 6.196},
	{"schiermonnikoog.waddenzee", "Schiermonnikoog", 53.469, 6.203}, {"delfzijl", "Delfzijl", 53.328, 6.931},
}

func nearestCoast(lat, lon float64) (coastStation, float64) {
	best, bd := coastStations[0], math.Inf(1)
	for _, s := range coastStations {
		if d := distanceKm(lat, lon, s.Lat, s.Lon); d < bd {
			best, bd = s, d
		}
	}
	return best, bd
}

type Tide struct {
	Time time.Time `json:"time"`
	High bool      `json:"high"`
	CM   int       `json:"cm"` // relative to NAP
}

type SeaNow struct {
	Temp    *float64 `json:"temp,omitempty"`     // °C
	Wave    *float64 `json:"wave,omitempty"`     // significant wave height, m
	WaveDir *float64 `json:"wave_dir,omitempty"` // degrees, where the waves come from
	WaveMax *float64 `json:"wave_max,omitempty"` // today's highest, m
}

// parseTides reads OphalenWaarnemingen (WATHTE, GETETBRKD2, astronomisch): the computed tide
// extremes; a value above both neighbours is high water.
func parseTides(body []byte) ([]Tide, error) {
	var r struct {
		Succesvol         bool   `json:"Succesvol"`
		Foutmelding       string `json:"Foutmelding"`
		WaarnemingenLijst []struct {
			MetingenLijst []struct {
				Tijdstip   string `json:"Tijdstip"`
				Meetwaarde struct {
					Waarde *float64 `json:"Waarde_Numeriek"`
				} `json:"Meetwaarde"`
			} `json:"MetingenLijst"`
		} `json:"WaarnemingenLijst"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("rijkswaterstaat tides: %w", err)
	}
	if !r.Succesvol || len(r.WaarnemingenLijst) == 0 {
		return nil, fmt.Errorf("rijkswaterstaat tides: %s", firstNonEmpty(r.Foutmelding, "no data"))
	}
	type pt struct {
		t time.Time
		v float64
	}
	var ps []pt
	for _, m := range r.WaarnemingenLijst[0].MetingenLijst {
		t, err := time.Parse("2006-01-02T15:04:05.000-07:00", m.Tijdstip)
		if err != nil || m.Meetwaarde.Waarde == nil || math.Abs(*m.Meetwaarde.Waarde) > 2000 {
			continue
		}
		ps = append(ps, pt{t, *m.Meetwaarde.Waarde})
	}
	if len(ps) < 2 {
		return nil, errors.New("rijkswaterstaat tides: too few values")
	}
	out := make([]Tide, len(ps))
	for i, p := range ps {
		high := false
		switch {
		case i > 0 && i < len(ps)-1:
			high = p.v > ps[i-1].v && p.v > ps[i+1].v
		case i == 0:
			high = p.v > ps[1].v
		default:
			high = p.v > ps[i-1].v
		}
		out[i] = Tide{Time: p.t.UTC(), High: high, CM: int(math.Round(p.v))}
	}
	return out, nil
}

func parseMarine(body []byte) (SeaNow, error) {
	var r struct {
		Current struct {
			Temp    *float64 `json:"sea_surface_temperature"`
			Wave    *float64 `json:"wave_height"`
			WaveDir *float64 `json:"wave_direction"`
		} `json:"current"`
		Daily struct {
			WaveMax []*float64 `json:"wave_height_max"`
		} `json:"daily"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return SeaNow{}, fmt.Errorf("open-meteo marine: %w", err)
	}
	s := SeaNow{Temp: r.Current.Temp, Wave: r.Current.Wave, WaveDir: r.Current.WaveDir}
	if len(r.Daily.WaveMax) > 0 {
		s.WaveMax = r.Daily.WaveMax[0]
	}
	if s.Temp == nil && s.Wave == nil {
		return SeaNow{}, errors.New("open-meteo marine: no values")
	}
	return s, nil
}

func (a *App) fetchTides(ctx context.Context, st coastStation) ([]Tide, error) {
	now := time.Now().UTC()
	q := map[string]any{
		"Locatie": map[string]string{"Code": st.ID},
		"AquoPlusWaarnemingMetadata": map[string]any{"AquoMetadata": map[string]any{
			"Grootheid": map[string]string{"Code": "WATHTE"}, "Groepering": map[string]string{"Code": "GETETBRKD2"}, "ProcesType": "astronomisch"}},
		"Periode": map[string]string{"Begindatumtijd": now.Add(-12 * time.Hour).Format("2006-01-02T15:04:05.000+00:00"),
			"Einddatumtijd": now.Add(48 * time.Hour).Format("2006-01-02T15:04:05.000+00:00")},
	}
	body, _ := json.Marshal(q)
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: a.config().Weather.TidesURL, Method: http.MethodPost, Accept: "application/json",
		Header: map[string]string{"Content-Type": "application/json"}, Body: body, Timeout: 20 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("rijkswaterstaat tides: %w", err)
	}
	return parseTides(resp.Body)
}

func (a *App) fetchSea(ctx context.Context, st coastStation) (SeaNow, error) {
	u := fmt.Sprintf("%s?latitude=%.3f&longitude=%.3f&current=wave_height,wave_direction,sea_surface_temperature&daily=wave_height_max&timezone=%s&forecast_days=1",
		a.config().Weather.MarineURL, st.Lat, st.Lon, url.QueryEscape("Europe/Amsterdam"))
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json"})
	if err != nil {
		return SeaNow{}, fmt.Errorf("open-meteo marine: %w", err)
	}
	return parseMarine(resp.Body)
}

// handleSea: GET /api/sea?lat=&lon=. Tides are cached per station for 6 hours and the sea for an
// hour; there are only a few stations, so all visitors share these copies.
func (a *App) handleSea(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Weather.Sea {
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
	st, km := nearestCoast(lat, lon)
	ctx := r.Context()
	e := map[string]any{"enabled": true, "station": map[string]any{"id": st.ID, "name": st.Name, "km": math.Round(km)},
		"tides_url": "https://waterinfo.rws.nl/"}
	if ts, at, _, err := a.tides.get(st.ID, 6*time.Hour, func() ([]Tide, error) { return a.fetchTides(context.WithoutCancel(ctx), st) }); err == nil {
		now := time.Now()
		var next []Tide
		for _, t := range ts {
			if t.Time.After(now.Add(-90*time.Minute)) && len(next) < 4 {
				next = append(next, t)
			}
		}
		e["tides"], e["fetched_at"] = next, at.UTC().Truncate(time.Second)
	} else {
		e["tides_error"] = err.Error()
	}
	if s, _, _, err := a.sea.get(st.ID, time.Hour, func() (SeaNow, error) { return a.fetchSea(context.WithoutCancel(ctx), st) }); err == nil {
		e["sea"] = s
	} else {
		e["sea_error"] = err.Error()
	}
	writeJSON(w, r, http.StatusOK, 600, e)
}
