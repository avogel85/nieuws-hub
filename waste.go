package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Afvalkalender providers. The list and the request formats follow the Home
// Assistant integration "afvalwijzer" by xirixiz (MIT licence,
// github.com/xirixiz/homeassistant-afvalwijzer), rewritten in Go and checked
// against real addresses (September 2026).
//
// A visitor's address is looked up like this: PDOK (the government's address
// service) gives the municipality; then the providers of that municipality and
// all regional providers are asked in parallel; the first one (in list order)
// that knows the address is remembered for that address.
//
// App providers (App: true) use the key of the vendor's own app or a guest
// login; they are off unless waste.app_providers is true.

type wasteProvider struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"-"` // opzet, ximmio, mijnafvalwijzer, rd4, rova, irado, amsterdam, reinis, rwm, kliko, straatbeeld, ical, burgerportaal, omrin, circulus
	URL       string   `json:"-"` // base URL or template
	Code      string   `json:"-"` // Ximmio company code, Burgerportaal organisation, Kliko id
	Home      string   `json:"-"` // shown as the source link
	Gemeenten []string `json:"-"` // PDOK municipality names; empty = regional, always asked
	App       bool     `json:"app,omitempty"`
}

const ximmio1, ximmio2 = "https://wasteapi.ximmio.com", "https://wasteprod2api.ximmio.com"

var wasteProviders = []wasteProvider{
	// municipal calendars with the "opzet" REST API
	{ID: "denhaag", Name: "Den Haag", Kind: "opzet", URL: "https://huisvuilkalender.denhaag.nl", Gemeenten: []string{"'s-Gravenhage"}},
	{ID: "alphenaandenrijn", Name: "Alphen aan den Rijn", Kind: "opzet", URL: "https://afvalkalender.alphenaandenrijn.nl", Gemeenten: []string{"Alphen aan den Rijn"}},
	{ID: "cranendonck", Name: "Cranendonck", Kind: "opzet", URL: "https://afvalkalender.cranendonck.nl", Gemeenten: []string{"Cranendonck"}},
	{ID: "defryskemarren", Name: "De Fryske Marren", Kind: "opzet", URL: "https://afvalkalender.defryskemarren.nl", Gemeenten: []string{"De Fryske Marren"}},
	{ID: "geertruidenberg", Name: "Geertruidenberg", Kind: "opzet", URL: "https://afval.geertruidenberg.nl", Gemeenten: []string{"Geertruidenberg"}},
	{ID: "lingewaard", Name: "Lingewaard", Kind: "opzet", URL: "https://afvalwijzer.lingewaard.nl", Gemeenten: []string{"Lingewaard"}},
	{ID: "middelburg-vlissingen", Name: "Middelburg en Vlissingen", Kind: "opzet", URL: "https://afvalwijzer.middelburgvlissingen.nl", Gemeenten: []string{"Middelburg", "Vlissingen"}},
	{ID: "peelenmaas", Name: "Peel en Maas", Kind: "opzet", URL: "https://afvalkalender.peelenmaas.nl", Gemeenten: []string{"Peel en Maas"}},
	{ID: "purmerend", Name: "Purmerend", Kind: "opzet", URL: "https://afvalkalender.purmerend.nl", Gemeenten: []string{"Purmerend"}},
	{ID: "schouwen-duiveland", Name: "Schouwen-Duiveland", Kind: "opzet", URL: "https://afvalkalender.schouwen-duiveland.nl", Gemeenten: []string{"Schouwen-Duiveland"}},
	{ID: "sliedrecht", Name: "Sliedrecht", Kind: "opzet", URL: "https://afvalkalender.sliedrecht.nl", Gemeenten: []string{"Sliedrecht"}},
	{ID: "spaarnelanden", Name: "Spaarnelanden", Kind: "opzet", URL: "https://afvalwijzer.spaarnelanden.nl", Gemeenten: []string{"Haarlem", "Zandvoort"}},
	{ID: "sudwestfryslan", Name: "Súdwest-Fryslân", Kind: "opzet", URL: "https://afvalkalender.sudwestfryslan.nl", Gemeenten: []string{"Súdwest-Fryslân"}},
	{ID: "venray", Name: "Venray", Kind: "opzet", URL: "https://afvalkalender.venray.nl", Gemeenten: []string{"Venray"}},
	{ID: "voorschoten", Name: "Voorschoten", Kind: "opzet", URL: "https://afvalkalender.voorschoten.nl", Gemeenten: []string{"Voorschoten"}},
	{ID: "waalre", Name: "Waalre", Kind: "opzet", URL: "https://afvalkalender.waalre.nl", Gemeenten: []string{"Waalre"}},
	{ID: "afvalstoffendienst", Name: "Afvalstoffendienst", Kind: "opzet", URL: "https://afvalstoffendienst.nl"},
	{ID: "cyclus", Name: "Cyclus", Kind: "opzet", URL: "https://cyclusnv.nl"},
	{ID: "dar", Name: "DAR", Kind: "opzet", URL: "https://afvalkalender.dar.nl"},
	{ID: "gad", Name: "GAD", Kind: "opzet", URL: "https://inzamelkalender.gad.nl"},
	{ID: "hvc", Name: "HVC", Kind: "opzet", URL: "https://inzamelkalender.hvcgroep.nl"},
	{ID: "offalkalinder", Name: "Offalkalinder", Kind: "opzet", URL: "https://www.offalkalinder.nl"},
	{ID: "prezero", Name: "PreZero", Kind: "opzet", URL: "https://inzamelwijzer.prezero.nl"},
	{ID: "saver", Name: "Saver", Kind: "opzet", URL: "https://saver.nl"},
	{ID: "zrd", Name: "ZRD", Kind: "opzet", URL: "https://www.zrd.nl"},
	// Ximmio (one API for many waste companies, told apart by a company code)
	{ID: "almere", Name: "Almere", Kind: "ximmio", URL: ximmio1, Code: "53d8db94-7945-42fd-9742-9bbc71dbe4c1", Home: "https://www.almere.nl", Gemeenten: []string{"Almere"}},
	{ID: "hellendoorn", Name: "Hellendoorn", Kind: "ximmio", URL: ximmio1, Code: "24434f5b-7244-412b-9306-3a2bd1e22bc1", Home: "https://www.hellendoorn.nl", Gemeenten: []string{"Hellendoorn"}},
	{ID: "oostzaan", Name: "Oostzaan", Kind: "ximmio", URL: ximmio2, Code: "6eb81e8f-ca5a-4bad-af0a-667650325511", Home: "https://www.oostzaan.nl", Gemeenten: []string{"Oostzaan"}},
	{ID: "venlo", Name: "Venlo", Kind: "ximmio", URL: ximmio1, Code: "280affe9-1428-443b-895a-b90431b8ca31", Home: "https://www.venlo.nl", Gemeenten: []string{"Venlo"}},
	{ID: "woerden", Name: "Woerden", Kind: "ximmio", URL: ximmio2, Code: "06856f74-6826-4c6a-aabf-69bc9d20b5a6", Home: "https://www.woerden.nl", Gemeenten: []string{"Woerden"}},
	{ID: "acv", Name: "ACV", Kind: "ximmio", URL: ximmio1, Code: "f8e2844a-095e-48f9-9f98-71fceb51d2c3", Home: "https://www.acv-groep.nl"},
	{ID: "areareiniging", Name: "Area Reiniging", Kind: "ximmio", URL: ximmio1, Code: "adc418da-d19b-11e5-ab30-625662870761", Home: "https://www.area-afval.nl"},
	{ID: "avalex", Name: "Avalex", Kind: "ximmio", URL: ximmio2, Code: "f7a74ad1-fdbf-4a43-9f91-44644f4d4222", Home: "https://www.avalex.nl"},
	{ID: "avri", Name: "Avri", Kind: "ximmio", URL: ximmio1, Code: "78cd4156-394b-413d-8936-d407e334559a", Home: "https://www.avri.nl"},
	{ID: "blink", Name: "Blink", Kind: "ximmio", URL: ximmio2, Code: "252d30d0-2e74-469c-8f1e-c0e2e434eb58", Home: "https://mijnblink.nl"},
	{ID: "meerlanden", Name: "Meerlanden", Kind: "ximmio", URL: ximmio2, Code: "800bf8d7-6dd1-4490-ba9d-b419d6dc8a45", Home: "https://meerlanden.nl"},
	{ID: "rad", Name: "RAD Hoeksche Waard", Kind: "ximmio", URL: ximmio2, Code: "13a2cad9-36d0-4b01-b877-efcb421a864d", Home: "https://radhw.nl"},
	{ID: "twentemilieu", Name: "Twente Milieu", Kind: "ximmio", URL: ximmio1, Code: "8d97bb56-5afd-4cbc-a651-b4f7314264b4", Home: "https://www.twentemilieu.nl"},
	{ID: "waardlanden", Name: "Waardlanden", Kind: "ximmio", URL: ximmio1, Code: "942abcf6-3775-400d-ae5d-7380d728b23c", Home: "https://www.waardlanden.nl"},
	// own APIs
	{ID: "amsterdam", Name: "Amsterdam", Kind: "amsterdam", URL: "https://api.data.amsterdam.nl/v1/afvalwijzer/afvalwijzer/", Home: "https://www.amsterdam.nl/afval/", Gemeenten: []string{"Amsterdam"}},
	{ID: "maassluis", Name: "Maassluis", Kind: "kliko", URL: "https://cp-maassluis.klikocontainermanager.com", Code: "505", Home: "https://www.maassluis.nl", Gemeenten: []string{"Maassluis"}},
	{ID: "oudeijsselstreek", Name: "Oude IJsselstreek", Kind: "kliko", URL: "https://cp-oudeijsselstreek.klikocontainermanager.com", Code: "454", Home: "https://www.ok.nl", Gemeenten: []string{"Oude IJsselstreek"}},
	{ID: "drimmelen", Name: "Drimmelen", Kind: "straatbeeld", URL: "https://drimmelen.api.straatbeeld.online", Home: "https://www.drimmelen.nl", Gemeenten: []string{"Drimmelen"}},
	{ID: "borsele", Name: "Borsele", Kind: "ical", URL: "https://afvalkalender.borsele.nl/afval/afvalkalender/{year}/{postcode}-{number}{suffix}.ics", Home: "https://afvalkalender.borsele.nl", Gemeenten: []string{"Borsele"}},
	{ID: "goes", Name: "Goes", Kind: "ical", URL: "https://afvalkalender.goes.nl/{year}/{postcode}-{number}{suffix}.ics", Home: "https://afvalkalender.goes.nl", Gemeenten: []string{"Goes"}},
	{ID: "edam-volendam", Name: "Edam-Volendam", Kind: "ical", URL: "https://www.edam-volendam.nl/trash-calendar/download/{postcode}/{number}/{suffix}?year={year}", Home: "https://www.edam-volendam.nl", Gemeenten: []string{"Edam-Volendam"}},
	{ID: "irado", Name: "Irado", Kind: "irado", URL: "https://www.irado.nl/wp-json/wsa/v1/location/address/calendar/pickups", Home: "https://www.irado.nl"},
	{ID: "rd4", Name: "RD4", Kind: "rd4", URL: "https://data.rd4.nl/api/v1/waste-calendar", Home: "https://www.rd4.nl"},
	{ID: "reinis", Name: "Reinis", Kind: "reinis", URL: "https://reinis.nl", Home: "https://reinis.nl"},
	{ID: "rova", Name: "ROVA", Kind: "rova", URL: "https://www.rova.nl", Home: "https://www.rova.nl"},
	{ID: "rwm", Name: "RWM", Kind: "rwm", URL: "https://rwm.nl", Home: "https://rwm.nl"},
	// app providers (off by default)
	{ID: "mijnafvalwijzer", Name: "Mijn Afvalwijzer", Kind: "mijnafvalwijzer", URL: "https://api.mijnafvalwijzer.nl/webservices/appsinput/", Home: "https://www.mijnafvalwijzer.nl", App: true},
	{ID: "groningen", Name: "Groningen", Kind: "burgerportaal", Code: "452048812597326549", Home: "https://gemeente.groningen.nl", Gemeenten: []string{"Groningen"}, App: true},
	{ID: "tilburg", Name: "Tilburg", Kind: "burgerportaal", Code: "452048812597339353", Home: "https://www.tilburg.nl", Gemeenten: []string{"Tilburg"}, App: true},
	{ID: "assen", Name: "Assen", Kind: "burgerportaal", Code: "138204213565303512", Home: "https://www.assen.nl", Gemeenten: []string{"Assen"}, App: true},
	{ID: "nijkerk", Name: "Nijkerk", Kind: "burgerportaal", Code: "138204213565304094", Home: "https://www.nijkerk.eu", Gemeenten: []string{"Nijkerk"}, App: true},
	{ID: "bar", Name: "BAR-Afvalbeheer", Kind: "burgerportaal", Code: "138204213564933497", Home: "https://www.bar-afvalbeheer.nl", Gemeenten: []string{"Barendrecht", "Albrandswaard", "Ridderkerk"}, App: true},
	{ID: "rmn", Name: "RMN", Kind: "burgerportaal", Code: "138204213564933597", Home: "https://www.rmn.nl", App: true},
	{ID: "omrin", Name: "Omrin", Kind: "omrin", URL: "https://api.omrinafvalapp.nl", Home: "https://www.omrin.nl", App: true},
	{ID: "circulus", Name: "Circulus", Kind: "circulus", URL: "https://mijn.circulus.nl", Home: "https://www.circulus.nl", App: true},
}

func isHTTPSURL(u string) bool { return isHTTPURL(u) && strings.HasPrefix(u, "https://") }

// wasteProviderList: id and name of the enabled providers, for the choice in the settings.
func wasteProviderList(c *Config) []map[string]string {
	if !c.Waste.Enabled {
		return nil
	}
	var out []map[string]string
	for _, p := range enabledWasteProviders(c) {
		out = append(out, map[string]string{"id": p.ID, "name": p.Name})
	}
	slices.SortFunc(out, func(a, b map[string]string) int {
		return strings.Compare(strings.ToLower(a["name"]), strings.ToLower(b["name"]))
	})
	return out
}

func wasteProviderByID(id string) (wasteProvider, bool) {
	for _, p := range wasteProviders {
		if p.ID == id {
			return p, true
		}
	}
	return wasteProvider{}, false
}

// enabledWasteProviders: the built-in list filtered by waste.providers (ids) and
// waste.app_providers, plus extra opzet calendars given as https URLs.
func enabledWasteProviders(c *Config) []wasteProvider {
	var ids []string
	var out []wasteProvider
	for _, e := range c.Waste.Providers {
		if isHTTPURL(e) { // https only in config.yaml (validate); http is allowed for tests
			u, _ := url.Parse(e)
			out = append(out, wasteProvider{ID: "opzet:" + u.Host, Name: u.Hostname(), Kind: "opzet", URL: strings.TrimSuffix(e, "/")})
		} else {
			ids = append(ids, e)
		}
	}
	for _, p := range wasteProviders {
		if (len(c.Waste.Providers) > 0 && !slices.Contains(ids, p.ID)) || (p.App && !c.Waste.AppProviders) { // a list replaces the default
			continue
		}
		out = append(out, p)
	}
	return out
}

// ---------------------------------------------------------------------------
// Address, result, cache

type wasteAddr struct {
	Postcode string `json:"postcode"`
	Number   int    `json:"number"`
	Suffix   string `json:"suffix,omitempty"`
	Provider string `json:"provider,omitempty"` // chosen by the visitor; empty = find automatically
}

func (w wasteAddr) key() string {
	return fmt.Sprintf("%s-%d-%s-%s", w.Postcode, w.Number, strings.ToUpper(w.Suffix), w.Provider)
}

// WasteResult is what a visitor with an own address gets.
type WasteResult struct {
	Pickups  []WastePickup `json:"pickups"`
	Provider string        `json:"provider"` // provider id
	Name     string        `json:"name"`
	Home     string        `json:"home"`
}

var (
	wasteSuffixRe     = regexp.MustCompile(`^[A-Za-z0-9-]{0,6}$`)
	wasteProviderIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9:.-]{1,60}$`) // built-in ids, or "opzet:host[:port]" for extra calendars
	errWasteNotFound  = errors.New("adres niet gevonden bij de ondersteunde afvalkalenders")
)

func parseWasteAddr(pc, nr, sfx string) (wasteAddr, bool) {
	w := wasteAddr{Postcode: normPostcode(pc), Suffix: strings.TrimSpace(sfx)}
	n, err := strconv.Atoi(strings.TrimSpace(nr))
	w.Number = n
	return w, err == nil && postcodeRe.MatchString(w.Postcode) && n >= 1 && n <= 99999 && wasteSuffixRe.MatchString(w.Suffix)
}

// wasteIndex remembers which provider knows an address, so it is searched only once.
type wasteIndex struct {
	mu sync.Mutex
	m  map[string]string // address key -> provider id
}

func (x *wasteIndex) get(k string) (string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	v, ok := x.m[k]
	return v, ok
}

func (x *wasteIndex) set(k, id string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.m == nil || len(x.m) >= 5000 {
		x.m = map[string]string{}
	}
	if id == "" {
		delete(x.m, k)
		return
	}
	x.m[k] = id
}

// wasteForAddress returns the pickups for an address: the visitor's chosen
// provider, the remembered one, or the first provider that knows the address.
func (a *App) wasteForAddress(ctx context.Context, w wasteAddr) (WasteResult, error) {
	cfg := a.config()
	enabled := enabledWasteProviders(cfg)
	find := func(id string) (wasteProvider, bool) {
		for _, p := range enabled {
			if p.ID == id {
				return p, true
			}
		}
		return wasteProvider{}, false
	}
	var p wasteProvider
	var list []WastePickup
	id := w.Provider
	if id == "" {
		id, _ = a.wasteIdx.get(w.key())
	}
	if id != "" {
		var ok bool
		if p, ok = find(id); !ok {
			return WasteResult{}, fmt.Errorf("afvalkalender %q is niet beschikbaar", id)
		}
		var found bool
		var err error
		if list, found, err = a.fetchWaste(ctx, p, w); err != nil {
			return WasteResult{}, err
		}
		if !found {
			a.wasteIdx.set(w.key(), "")
			if w.Provider != "" {
				return WasteResult{}, errWasteNotFound
			}
			id = "" // moved to another provider: search again
		}
	}
	if id == "" {
		var err error
		if p, list, err = a.discoverWaste(ctx, w, enabled); err != nil {
			return WasteResult{}, err
		}
		a.wasteIdx.set(w.key(), p.ID)
	}
	home := p.Home
	if home == "" {
		home = p.URL
	}
	return WasteResult{Pickups: sortPickups(list, time.Now()), Provider: p.ID, Name: p.Name, Home: home}, nil
}

// discoverWaste asks the municipality's own providers first, then the regional
// ones (at most 6 requests at a time); list order decides between two hits.
func (a *App) discoverWaste(ctx context.Context, w wasteAddr, enabled []wasteProvider) (wasteProvider, []WastePickup, error) {
	gem, _ := a.pdokGemeente(ctx, w)
	var own, regional []wasteProvider
	for _, p := range enabled {
		switch {
		case len(p.Gemeenten) == 0:
			regional = append(regional, p)
		case gem == "" || slices.ContainsFunc(p.Gemeenten, func(g string) bool { return strings.EqualFold(g, gem) }):
			own = append(own, p)
		}
	}
	failed, asked := 0, 0
	for _, group := range [][]wasteProvider{own, regional} {
		if len(group) == 0 {
			continue
		}
		type res struct {
			list  []WastePickup
			found bool
			err   error
		}
		out := make([]res, len(group))
		sem := make(chan struct{}, 6)
		var wg sync.WaitGroup
		for i, p := range group {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				l, f, err := a.fetchWaste(ctx, p, w)
				out[i] = res{l, f, err}
			}()
		}
		wg.Wait()
		for i, r := range out {
			asked++
			if r.found {
				return group[i], r.list, nil
			}
			if r.err != nil {
				failed++
			}
		}
	}
	if asked > 0 && failed == asked {
		return wasteProvider{}, nil, errors.New("de afvalkalenders zijn niet bereikbaar")
	}
	return wasteProvider{}, nil, errWasteNotFound
}

var pdokURL = "https://api.pdok.nl/bzk/locatieserver/search/v3_1/free"

// pdokGemeente looks up the municipality of an address (PDOK Locatieserver, no key).
func (a *App) pdokGemeente(ctx context.Context, w wasteAddr) (string, error) {
	q := url.Values{"q": {fmt.Sprintf("postcode:%s and huisnummer:%d", w.Postcode, w.Number)}, "fq": {"type:adres"}, "fl": {"gemeentenaam"}, "rows": {"1"}}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: pdokURL + "?" + q.Encode(), Accept: "application/json"})
	if err != nil {
		return "", err
	}
	var r struct {
		Response struct {
			Docs []struct {
				Gemeente string `json:"gemeentenaam"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil || len(r.Response.Docs) == 0 {
		return "", errors.New("pdok: address not found")
	}
	return r.Response.Docs[0].Gemeente, nil
}

// ---------------------------------------------------------------------------
// Fetching per provider kind. found=false means the provider does not know the address.

func (a *App) fetchWaste(ctx context.Context, p wasteProvider, w wasteAddr) (list []WastePickup, found bool, err error) {
	switch p.Kind {
	case "opzet":
		return a.wasteOpzet(ctx, p, w)
	case "ximmio":
		return a.wasteXimmio(ctx, p, w)
	case "mijnafvalwijzer":
		return a.wasteMijnAfvalwijzer(ctx, p, w)
	case "amsterdam":
		return a.wasteAmsterdam(ctx, p, w)
	case "rd4":
		return a.wasteRD4(ctx, p, w)
	case "rova":
		return a.wasteRova(ctx, p, w)
	case "irado":
		return a.wasteIrado(ctx, p, w)
	case "reinis", "rwm":
		return a.wasteReinis(ctx, p, w)
	case "kliko":
		return a.wasteKliko(ctx, p, w)
	case "straatbeeld":
		return a.wasteStraatbeeld(ctx, p, w)
	case "ical":
		return a.wasteICal(ctx, p, w)
	case "burgerportaal":
		return a.wasteBurgerportaal(ctx, p, w)
	case "omrin":
		return a.wasteOmrin(ctx, p, w)
	case "circulus":
		return a.wasteCirculus(ctx, p, w)
	}
	return nil, false, fmt.Errorf("waste: unknown provider kind %q", p.Kind)
}

func (a *App) getJSON(ctx context.Context, u string, v any, hdr map[string]string) (int, error) {
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "application/json", Header: hdr})
	if resp != nil && (resp.Status == http.StatusNotFound || resp.Status == http.StatusBadRequest) {
		return resp.Status, nil // many providers answer an unknown address with 404/400
	}
	if err != nil {
		return 0, err
	}
	if len(strings.TrimSpace(string(resp.Body))) == 0 {
		return resp.Status, nil
	}
	return resp.Status, json.Unmarshal(resp.Body, v)
}

func pickup(typ, date string) (WastePickup, bool) {
	if len(date) >= 10 {
		date = date[:10]
	}
	if _, err := time.Parse("2006-01-02", date); err != nil || strings.TrimSpace(typ) == "" {
		return WastePickup{}, false
	}
	return WastePickup{Type: wasteLabel(typ), Date: date}, true
}

func (a *App) wasteOpzet(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	var addrs []struct {
		BagID      string `json:"bagId"`
		Letter     string `json:"huisletter"`
		Toevoeging string `json:"huisnummerToevoeging"`
	}
	if _, err := a.getJSON(ctx, fmt.Sprintf("%s/rest/adressen/%s-%d", p.URL, w.Postcode, w.Number), &addrs, nil); err != nil {
		return nil, false, err
	}
	bag := ""
	for _, ad := range addrs {
		if w.Suffix != "" && strings.EqualFold(ad.Letter+ad.Toevoeging, w.Suffix) {
			bag = ad.BagID
		}
	}
	if bag == "" && len(addrs) > 0 {
		bag = addrs[0].BagID // no or unknown suffix: the first address with this number
	}
	if bag == "" {
		return nil, false, nil
	}
	if !bagIDRe.MatchString(bag) {
		return nil, false, errors.New("waste: unexpected address id")
	}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: p.URL + "/rest/adressen/" + bag + "/afvalstromen", Accept: "application/json"})
	if err != nil {
		return nil, true, err
	}
	list, err := parseOpzetStreams(resp.Body)
	for i := range list {
		list[i].Type = wasteLabel(list[i].Type)
	}
	return list, true, err
}

func (a *App) wasteXimmio(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	post := func(path string, form url.Values, v any) error {
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: p.URL + path, Method: http.MethodPost, Accept: "application/json", Body: []byte(form.Encode()),
			Header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}})
		if err != nil {
			return err
		}
		return json.Unmarshal(resp.Body, v)
	}
	form := url.Values{"postCode": {w.Postcode}, "houseNumber": {strconv.Itoa(w.Number)}, "companyCode": {p.Code}}
	if w.Suffix != "" {
		form.Set("HouseLetter", strings.ToUpper(w.Suffix))
	}
	var addr struct {
		DataList []struct {
			UniqueID  json.RawMessage `json:"UniqueId"`
			Community string          `json:"Community"`
		} `json:"dataList"`
	}
	if err := post("/api/FetchAdress", form, &addr); err != nil {
		return nil, false, err
	}
	if len(addr.DataList) == 0 {
		return nil, false, nil
	}
	uid := strings.Trim(string(addr.DataList[0].UniqueID), `"`)
	now := time.Now().In(amsterdam)
	var cal struct {
		DataList []struct {
			Type  string   `json:"_pickupTypeText"`
			Dates []string `json:"pickupDates"`
		} `json:"dataList"`
	}
	if err := post("/api/GetCalendar", url.Values{"companyCode": {p.Code}, "startDate": {now.Format("2006-01-02")}, "endDate": {now.AddDate(0, 3, 0).Format("2006-01-02")},
		"community": {addr.DataList[0].Community}, "uniqueAddressID": {uid}}, &cal); err != nil {
		return nil, true, err
	}
	var out []WastePickup
	for _, it := range cal.DataList {
		for _, d := range it.Dates {
			if pk, ok := pickup(it.Type, d); ok {
				out = append(out, pk)
			}
		}
	}
	return out, true, nil
}

// mijnAfvalwijzerKey is the key of the Mijn Afvalwijzer web app (the same one the
// Home Assistant integration uses); only used with waste.app_providers.
const mijnAfvalwijzerKey = "5ef443e778f41c4f75c69459eea6e6ae0c2d92de729aa0fc61653815fbd6a8ca"

func (a *App) wasteMijnAfvalwijzer(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	q := url.Values{"apikey": {mijnAfvalwijzerKey}, "method": {"postcodecheck"}, "postcode": {w.Postcode}, "street": {""}, "huisnummer": {strconv.Itoa(w.Number)},
		"toevoeging": {w.Suffix}, "app_name": {"afvalwijzer"}, "platform": {"web"}, "langs": {"nl"}, "afvaldata": {time.Now().In(amsterdam).Format("2006-01-02")}}
	var raw json.RawMessage
	if _, err := a.getJSON(ctx, p.URL+"?"+q.Encode(), &raw, nil); err != nil {
		return nil, false, err
	}
	var r struct {
		Now struct {
			Data []struct{ Type, Date string } `json:"data"`
		} `json:"ophaaldagen"`
		Next struct {
			Data []struct{ Type, Date string } `json:"data"`
		} `json:"ophaaldagenNext"`
	}
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &r) != nil {
		return nil, false, nil // an unknown address answers with an empty list or a message
	}
	var out []WastePickup
	for _, it := range append(r.Now.Data, r.Next.Data...) {
		if pk, ok := pickup(it.Type, it.Date); ok {
			out = append(out, pk)
		}
	}
	return out, len(out) > 0, nil
}

func (a *App) wasteRD4(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	var out []WastePickup
	found := false
	now := time.Now().In(amsterdam)
	years := []int{now.Year()}
	if now.Month() >= 11 {
		years = append(years, now.Year()+1)
	}
	for _, y := range years {
		q := url.Values{"postal_code": {w.Postcode}, "house_number": {strconv.Itoa(w.Number)}, "house_number_extension": {w.Suffix}, "year": {strconv.Itoa(y)}}
		var r struct {
			Success bool `json:"success"`
			Data    struct {
				Items [][]struct{ Type, Date string } `json:"items"`
			} `json:"data"`
		}
		if _, err := a.getJSON(ctx, p.URL+"?"+q.Encode(), &r, nil); err != nil {
			return nil, found, err
		}
		if !r.Success || len(r.Data.Items) == 0 {
			continue
		}
		found = true
		for _, it := range r.Data.Items[0] {
			if pk, ok := pickup(it.Type, it.Date); ok {
				out = append(out, pk)
			}
		}
	}
	return out, found, nil
}

func (a *App) wasteRova(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	q := url.Values{"houseNumber": {strconv.Itoa(w.Number)}, "addition": {w.Suffix}, "postalcode": {w.Postcode}, "take": {"20"}}
	var r []struct {
		Date      string `json:"date"`
		WasteType struct {
			Title string `json:"title"`
		} `json:"wasteType"`
	}
	if _, err := a.getJSON(ctx, p.URL+"/api/waste-calendar/upcoming?"+q.Encode(), &r, nil); err != nil {
		return nil, false, err
	}
	var out []WastePickup
	for _, it := range r {
		if pk, ok := pickup(it.WasteType.Title, it.Date); ok {
			out = append(out, pk)
		}
	}
	return out, len(out) > 0, nil
}

func (a *App) wasteIrado(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	q := url.Values{"zipcode": {w.Postcode}, "number": {strconv.Itoa(w.Number)}, "extention": {w.Suffix}}
	var r struct {
		Valid bool `json:"valid"`
		Data  struct {
			Pickups json.RawMessage `json:"pickups"`
		} `json:"calendar_data"`
	}
	if _, err := a.getJSON(ctx, p.URL+"?"+q.Encode(), &r, nil); err != nil {
		return nil, false, err
	}
	if !r.Valid {
		return nil, false, nil
	}
	var byYear map[string]map[string]map[string][]struct{ Date, Type string } // year -> month -> day -> pickups
	var out []WastePickup
	if json.Unmarshal(r.Data.Pickups, &byYear) == nil {
		for _, months := range byYear {
			for _, days := range months {
				for _, items := range days {
					for _, it := range items {
						if t, err := time.Parse("02/01/2006", it.Date); err == nil {
							if pk, ok := pickup(it.Type, t.Format("2006-01-02")); ok {
								out = append(out, pk)
							}
						}
					}
				}
			}
		}
	}
	return out, true, nil
}

// wasteReinis: Reinis and RWM (an opzet variant with "postcode:number" lookups).
func (a *App) wasteReinis(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	var addrs []struct {
		BagID string `json:"bagid"`
	}
	if _, err := a.getJSON(ctx, fmt.Sprintf("%s/adressen/%s:%d%s", p.URL, w.Postcode, w.Number, url.PathEscape(w.Suffix)), &addrs, nil); err != nil {
		return nil, false, err
	}
	if len(addrs) == 0 || addrs[0].BagID == "" {
		return nil, false, nil
	}
	if !bagIDRe.MatchString(addrs[0].BagID) {
		return nil, false, errors.New("waste: unexpected address id")
	}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: p.URL + "/rest/adressen/" + addrs[0].BagID + "/afvalstromen", Accept: "application/json"})
	if err != nil {
		return nil, true, err
	}
	list, err := parseOpzetStreams(resp.Body)
	for i := range list {
		list[i].Type = wasteLabel(list[i].Type)
	}
	return list, true, err
}

func (a *App) wasteKliko(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	var r struct {
		Calendar map[string]map[string]json.RawMessage `json:"calendar"` // date -> {code: {...}}
	}
	if _, err := a.getJSON(ctx, fmt.Sprintf("%s/MyKliko/wasteCalendarJSON/%s/%s/%d", p.URL, p.Code, w.Postcode, w.Number), &r,
		map[string]string{"Content-Type": "application/json"}); err != nil {
		return nil, false, err
	}
	var out []WastePickup
	for date, codes := range r.Calendar {
		for code := range codes {
			if pk, ok := pickup(code, date); ok {
				out = append(out, pk)
			}
		}
	}
	return out, len(r.Calendar) > 0, nil
}

func (a *App) wasteStraatbeeld(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	body, _ := json.Marshal(map[string]string{"postal_code": w.Postcode, "house_number": strconv.Itoa(w.Number), "house_letter": w.Suffix})
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: p.URL + "/v1/waste-calendar", Method: http.MethodPost, Accept: "application/json", Body: body,
		Header: map[string]string{"Content-Type": "application/json"}})
	if resp != nil && (resp.Status == http.StatusNotFound || resp.Status == http.StatusUnprocessableEntity || resp.Status == http.StatusBadRequest) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var r struct {
		Collections map[string]map[string][]struct {
			Date struct {
				Formatted string `json:"formatted"`
			} `json:"date"`
			Data []struct {
				Name string `json:"name"`
			} `json:"data"`
		} `json:"collections"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, false, nil
	}
	var out []WastePickup
	for _, months := range r.Collections {
		for _, days := range months {
			for _, d := range days {
				for _, it := range d.Data {
					if pk, ok := pickup(it.Name, d.Date.Formatted); ok {
						out = append(out, pk)
					}
				}
			}
		}
	}
	return out, len(out) > 0, nil
}

func (a *App) wasteICal(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	now := time.Now().In(amsterdam)
	var out []WastePickup
	found := false
	years := []int{now.Year()}
	if now.Month() >= 11 {
		years = append(years, now.Year()+1)
	}
	for _, y := range years {
		u := strings.NewReplacer("{year}", strconv.Itoa(y), "{postcode}", w.Postcode, "{number}", strconv.Itoa(w.Number), "{suffix}", url.PathEscape(w.Suffix)).Replace(p.URL)
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: u, Accept: "text/calendar, */*;q=0.5"})
		if resp != nil && resp.Status >= 400 && resp.Status < 500 {
			continue
		}
		if err != nil {
			return nil, found, err
		}
		list, err := parseWasteICS(resp.Body, now)
		if err != nil {
			continue
		}
		found = true
		for _, pk := range list {
			out = append(out, WastePickup{Type: wasteLabel(pk.Type), Date: pk.Date})
		}
	}
	return out, found, nil
}

// ---------------------------------------------------------------------------
// Amsterdam publishes weekdays and a frequency instead of dates; the next dates
// are computed here (weekly, even or odd ISO weeks, or a list of dates).

var amsterdamDays = map[string]time.Weekday{"maandag": time.Monday, "dinsdag": time.Tuesday, "woensdag": time.Wednesday, "donderdag": time.Thursday,
	"vrijdag": time.Friday, "zaterdag": time.Saturday, "zondag": time.Sunday}

func (a *App) wasteAmsterdam(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	q := url.Values{"postcode": {w.Postcode}, "huisnummer": {strconv.Itoa(w.Number)}}
	if w.Suffix != "" {
		q.Set("huisletter", strings.ToUpper(w.Suffix))
	}
	var r struct {
		Embedded struct {
			Items []amsterdamItem `json:"afvalwijzer"`
		} `json:"_embedded"`
	}
	if _, err := a.getJSON(ctx, p.URL+"?"+q.Encode(), &r, map[string]string{"Accept": "application/hal+json"}); err != nil {
		return nil, false, err
	}
	if len(r.Embedded.Items) == 0 && w.Suffix != "" { // the suffix may be a huisnummertoevoeging
		q.Del("huisletter")
		q.Set("huisnummertoevoeging", w.Suffix)
		if _, err := a.getJSON(ctx, p.URL+"?"+q.Encode(), &r, map[string]string{"Accept": "application/hal+json"}); err != nil {
			return nil, false, err
		}
	}
	if len(r.Embedded.Items) == 0 {
		return nil, false, nil
	}
	return amsterdamPickups(r.Embedded.Items, time.Now().In(amsterdam)), true, nil
}

type amsterdamItem struct {
	Code      string `json:"afvalwijzerFractieCode"`
	Name      string `json:"afvalwijzerFractieNaam"`
	Days      string `json:"afvalwijzerOphaaldagen"`
	Frequency string `json:"afvalwijzerAfvalkalenderFrequentie"`
	Where     string `json:"afvalwijzerWaar"`
}

func amsterdamPickups(items []amsterdamItem, now time.Time) []WastePickup {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var out []WastePickup
	for _, it := range items {
		freq := strings.ToLower(strings.TrimSpace(it.Frequency))
		if it.Days == "" || it.Code == "" || (freq == "" && !strings.Contains(strings.ToLower(it.Where), "stoep")) {
			continue // containers: no collection days at the door
		}
		label := firstNonEmpty(it.Name, it.Code)
		if strings.ContainsAny(freq, "0123456789") && !strings.Contains(freq, "week") { // explicit dates, e.g. "5-1, 2-2" or "05-01-26"
			for _, f := range strings.FieldsFunc(freq, func(r rune) bool { return r == ',' || r == ' ' || r == '/' }) {
				for _, layout := range []string{"2-1-06", "2-1-2006", "2-1"} {
					if t, err := time.Parse(layout, strings.Trim(f, ".")); err == nil {
						if layout == "2-1" {
							t = time.Date(today.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
						}
						if !t.Before(today) {
							out = append(out, WastePickup{Type: wasteLabel(label), Date: t.Format("2006-01-02")})
						}
						break
					}
				}
			}
			continue
		}
		parity := -1 // any week
		if strings.Contains(freq, "oneven") {
			parity = 1
		} else if strings.Contains(freq, "even") {
			parity = 0
		}
		for _, d := range strings.Split(strings.ReplaceAll(strings.ToLower(it.Days), " ", ""), ",") {
			wd, ok := amsterdamDays[d]
			if !ok {
				continue
			}
			for i := 0; i < 42; i++ {
				t := today.AddDate(0, 0, i)
				_, wk := t.ISOWeek()
				if t.Weekday() == wd && (parity < 0 || wk%2 == parity) {
					out = append(out, WastePickup{Type: wasteLabel(label), Date: t.Format("2006-01-02")})
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// App providers (waste.app_providers)

// burgerportaalKey is the public Firebase key of the Burgerportaal web app; it
// creates an anonymous session, as the app itself does.
const burgerportaalKey = "AIzaSyA6NkRqJypTfP-cjWzrZNFJzPUbBaGjOdk"

func (a *App) wasteBurgerportaal(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: "https://www.googleapis.com/identitytoolkit/v3/relyingparty/signupNewUser?key=" + burgerportaalKey,
		Method: http.MethodPost, Accept: "application/json", Body: []byte("{}"), Header: map[string]string{"Content-Type": "application/json"}})
	if err != nil {
		return nil, false, err
	}
	var su struct {
		IDToken string `json:"idToken"`
	}
	if json.Unmarshal(resp.Body, &su) != nil || su.IDToken == "" {
		return nil, false, errors.New("burgerportaal: no session")
	}
	base := "https://europe-west3-burgerportaal-production.cloudfunctions.net/exposed/organisations/" + p.Code
	hdr := map[string]string{"authorization": su.IDToken}
	var addrs []struct {
		ID       string `json:"addressId"`
		Addition string `json:"addition"`
	}
	if _, err := a.getJSON(ctx, fmt.Sprintf("%s/address?zipcode=%s&housenumber=%d", base, w.Postcode, w.Number), &addrs, hdr); err != nil {
		return nil, false, err
	}
	id := ""
	for _, ad := range addrs {
		if w.Suffix != "" && strings.EqualFold(ad.Addition, w.Suffix) {
			id = ad.ID
		}
	}
	if id == "" && len(addrs) > 0 {
		id = addrs[0].ID
	}
	if id == "" {
		return nil, false, nil
	}
	var cal []struct {
		Date     string `json:"collectionDate"`
		Fraction string `json:"fraction"`
	}
	if _, err := a.getJSON(ctx, base+"/address/"+url.PathEscape(id)+"/calendar", &cal, hdr); err != nil {
		return nil, true, err
	}
	var out []WastePickup
	for _, it := range cal {
		if pk, ok := pickup(it.Fraction, it.Date); ok {
			out = append(out, pk)
		}
	}
	return out, true, nil
}

func (a *App) wasteOmrin(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	body, _ := json.Marshal(map[string]any{"Email": nil, "Password": nil, "PostalCode": w.Postcode, "HouseNumber": w.Number, "HouseNumberExtension": w.Suffix,
		"DeviceId": newDeviceID(), "Platform": "iOS", "AppVersion": "4.0.3.273", "OsVersion": "iPhone15,3 26.2.1"})
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: p.URL + "/api/auth/login", Method: http.MethodPost, Accept: "application/json", Body: body,
		Header: map[string]string{"Content-Type": "application/json"}})
	if resp != nil && resp.Status >= 400 && resp.Status < 500 {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var login struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"accessToken"`
		} `json:"data"`
	}
	if json.Unmarshal(resp.Body, &login) != nil || !login.Success || login.Data.Token == "" {
		return nil, false, nil // the guest login fails for an address outside Omrin's area
	}
	q, _ := json.Marshal(map[string]string{"query": "query FetchCalendar { fetchCalendar { date type } }"})
	resp, err = a.fetcher.Do(ctx, FetchReq{URL: p.URL + "/graphql", Method: http.MethodPost, Accept: "application/json", Body: q,
		Header: map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + login.Data.Token}})
	if err != nil {
		return nil, true, err
	}
	var r struct {
		Data struct {
			Calendar []struct{ Date, Type string } `json:"fetchCalendar"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, true, err
	}
	var out []WastePickup
	for _, it := range r.Data.Calendar {
		if pk, ok := pickup(it.Type, it.Date); ok {
			out = append(out, pk)
		}
	}
	return out, true, nil
}

func newDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var circulusATRe = regexp.MustCompile(`__AT=([^&]*)&___TS=`)

// wasteCirculus needs a session cookie, so it uses its own short-lived client
// with a cookie jar (the shared fetcher is stateless).
func (a *App) wasteCirculus(ctx context.Context, p wasteProvider, w wasteAddr) ([]WastePickup, bool, error) {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	do := func(method, u, ctype string, body io.Reader) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", a.config().Fetch.UserAgent)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := c.Do(req)
		if err != nil {
			return nil, shortErr(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if resp.StatusCode >= 300 {
			return b, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return b, err
	}
	if _, err := do(http.MethodGet, p.URL, "", nil); err != nil {
		return nil, false, err
	}
	u, _ := url.Parse(p.URL)
	at := ""
	for _, ck := range jar.Cookies(u) {
		if ck.Name == "CB_SESSION" {
			if m := circulusATRe.FindStringSubmatch(ck.Value); m != nil {
				at = m[1]
			}
		}
	}
	form := url.Values{"authenticityToken": {at}, "zipCode": {w.Postcode}, "number": {strconv.Itoa(w.Number)}}
	b, err := do(http.MethodPost, p.URL+"/register/zipcode.json", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, false, err
	}
	var reg struct {
		Flash  string `json:"flashMessage"`
		Custom struct {
			Addresses []struct {
				Address string `json:"address"`
				AuthURL string `json:"authenticationUrl"`
			} `json:"addresses"`
		} `json:"customData"`
	}
	if json.Unmarshal(b, &reg) != nil {
		return nil, false, nil
	}
	if reg.Flash != "" {
		auth := ""
		for _, ad := range reg.Custom.Addresses {
			if w.Suffix != "" && strings.Contains(strings.ToLower(ad.Address), fmt.Sprintf(" %d %s", w.Number, strings.ToLower(w.Suffix))) {
				auth = ad.AuthURL
			}
		}
		if auth == "" && len(reg.Custom.Addresses) > 0 {
			auth = reg.Custom.Addresses[0].AuthURL
		}
		if auth == "" || !strings.HasPrefix(auth, "/") {
			return nil, false, nil
		}
		if _, err := do(http.MethodGet, p.URL+auth, "", nil); err != nil {
			return nil, false, err
		}
	}
	now := time.Now().In(amsterdam)
	b, err = do(http.MethodGet, fmt.Sprintf("%s/afvalkalender.json?from=%s&till=%s", p.URL, now.Format("2006-01-02"), now.AddDate(0, 3, 0).Format("2006-01-02")), "", nil)
	if err != nil {
		return nil, false, err
	}
	var cal struct {
		Custom struct {
			Response struct {
				Garbage []struct {
					Code  string   `json:"code"`
					Dates []string `json:"dates"`
				} `json:"garbage"`
			} `json:"response"`
		} `json:"customData"`
	}
	if json.Unmarshal(b, &cal) != nil {
		return nil, false, nil
	}
	var out []WastePickup
	for _, g := range cal.Custom.Response.Garbage {
		for _, d := range g.Dates {
			if pk, ok := pickup(g.Code, d); ok {
				out = append(out, pk)
			}
		}
	}
	return out, len(cal.Custom.Response.Garbage) > 0, nil
}

// ---------------------------------------------------------------------------
// Waste type names: providers use codes (GREEN, PAPER, pbd, rst) or long texts;
// they are mapped to short standard names (after the integration's mapping).

var wasteKeys = map[string]string{
	"best": "best-tas", "best_bag": "best-tas", "bestafr": "best-tas", "bio-afval": "gft", "biobak": "gft", "branches": "snoeiafval", "bulklitter": "grofvuil",
	"bulkygardenwaste": "tuinafval", "bulkyrestwaste": "grofvuil", "plastic+": "plastic", "pmd+ ophaal": "pmd", "papier ophaal": "papier", "gft ophaal": "gft", "chemisch afval": "kca", "chemokar": "kca", "christmas_trees": "kerstbomen", "container restafval": "restafval",
	"ga": "grofvuil", "gemengde plastics": "plastic", "gft": "gft", "gft & etensresten": "gft", "gft afval": "gft", "gft-afval": "gft", "gft en etensresten": "gft",
	"gft+e": "gft", "gfte": "gft", "glass": "glas", "glas": "glas", "green": "gft", "greenbasket": "gft", "grey": "restafval", "grijze container": "restafval",
	"groene container": "gft", "groente": "gft", "grof": "grofvuil", "grofvuil": "grofvuil", "grof tuinafval": "snoeiafval", "kca": "kca", "kerstb": "kerstbomen",
	"kerstboom": "kerstbomen", "kerstbomen": "kerstbomen", "luiers": "luiers", "mobiletransferpoint": "milieustraat", "opk": "papier", "oud papier": "papier",
	"oud papier & karton": "papier", "oud papier en karton": "papier", "packages": "pmd", "packagesbag": "pmd", "pap": "papier", "paper": "papier", "papier": "papier",
	"papier en karton": "papier", "papier-karton": "papier", "pbd": "pmd", "pbp": "pmd", "pd": "pmd", "pdb": "pmd", "plastic": "plastic", "pmd": "pmd", "pmd+": "pmd",
	"pmd-zak": "pmd", "pmdrest": "pmd-restafval", "pruning_waste": "snoeiafval", "remainder": "restafval", "residual_waste": "restafval", "rest": "restafval",
	"restafval": "restafval", "rst": "restafval", "sloop": "grofvuil", "sortibak": "restafval", "takken": "snoeiafval", "textile": "textiel", "textiel": "textiel",
	"tree": "kerstbomen", "zak_blauw": "restafval", "overig": "overig",
}

var wasteNames = map[string]string{"gft": "GFT", "pmd": "PMD", "plastic": "Plastic", "papier": "Papier", "restafval": "Restafval", "glas": "Glas",
	"textiel": "Textiel", "kerstbomen": "Kerstbomen", "grofvuil": "Grofvuil", "snoeiafval": "Snoeiafval", "tuinafval": "Tuinafval", "kca": "Klein chemisch afval",
	"luiers": "Luiers", "best-tas": "BEST-tas", "pmd-restafval": "PMD en restafval", "milieustraat": "Milieustraat op wielen", "overig": "Overig"}

var wasteWordRe = regexp.MustCompile(`^(gft|pmd|pbd|papier|rest|glas|textiel|plastic)`)

// wasteLabel turns a provider's type into a short name; unknown long texts are kept.
func wasteLabel(raw string) string {
	s := strings.TrimSpace(plainText(raw))
	low := strings.ToLower(s)
	if k, ok := wasteKeys[low]; ok {
		return wasteNames[k]
	}
	// long descriptions that start with a known word, e.g. "Rolcontainer restafval", "GFT+E"
	for _, pat := range []struct{ re, key string }{{"groente", "gft"}, {"gft", "gft"}, {"plastic, ", "pmd"}, {"plastic en ", "pmd"}, {"plastic verpakkingen", "pmd"},
		{"pmd", "pmd"}, {"pbd", "pmd"}, {"papier", "papier"}, {"oud papier", "papier"}, {"restafval", "restafval"}, {"rolcontainer restafval", "restafval"},
		{"rolcontainer gft", "gft"}, {"inzameling gft", "gft"}, {"inzameling papier", "papier"}, {"inzameling plastic", "pmd"}, {"grijze container", "restafval"},
		{"groene container", "gft"}, {"kerstbo", "kerstbomen"}, {"textiel", "textiel"}, {"glas", "glas"}} {
		if strings.HasPrefix(low, pat.re) {
			return wasteNames[pat.key]
		}
	}
	if wasteWordRe.MatchString(low) && len(low) <= 12 {
		return strings.ToUpper(low[:1]) + low[1:]
	}
	if s == strings.ToUpper(s) || s == low { // other codes: "MAAS" -> "Maas"
		return truncate(strings.ToUpper(low[:1])+low[1:], 40)
	}
	return truncate(s, 40)
}
