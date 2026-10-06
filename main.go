// Nieuwsdashboard: a single-binary Dutch news, weather and threat dashboard.
//
// main.go   — config, app lifecycle, HTTP server and API handlers
// feeds.go  — outbound fetcher, scheduler, feed parser, news cache
// panels.go — side-panel data: weather, geocoding, threat intelligence, advisories
package main

import (
	"bytes"
	"compress/gzip"
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decoders for the image proxy
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // scratch/distroless images have no zoneinfo

	"gopkg.in/yaml.v3"
)

//go:embed web/index.html
var indexHTML []byte

// pageID identifies this build's page. The page carries it in <meta name="ndb-page"> and every API answer
// in the header X-NDB-Page, so a browser that still runs an older page notices and refreshes itself.
var pageID = func() string {
	sum := sha256.Sum256(append([]byte(version), indexHTML...))
	return hex.EncodeToString(sum[:8])
}()

var version = "dev"

// ---------------------------------------------------------------------------
// Config

// Duration is a time.Duration that unmarshals from strings like "10m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, s)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

type Location struct {
	Name    string  `yaml:"name" json:"name"`
	Lat     float64 `yaml:"lat" json:"lat"`
	Lon     float64 `yaml:"lon" json:"lon"`
	Region  string  `yaml:"region" json:"region,omitempty"`   // province, to match weather warnings
	Country string  `yaml:"country" json:"country,omitempty"` // ISO code, e.g. NL
}

type Category struct {
	ID    string `yaml:"id" json:"id"`
	Name  string `yaml:"name" json:"name"`
	Short string `yaml:"short" json:"short,omitempty"` // chip label
	// English interface: optional translations (fall back to the Dutch text)
	NameEN  string `yaml:"name_en" json:"name_en,omitempty"`
	ShortEN string `yaml:"short_en" json:"short_en,omitempty"`
}

// Preset is a named group of sources offered on the first visit and in the settings.
type Preset struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description,omitempty"`
	NameEN      string   `yaml:"name_en" json:"name_en,omitempty"`
	DescEN      string   `yaml:"description_en" json:"description_en,omitempty"`
	Sources     []string `yaml:"sources" json:"sources"`
	Region      bool     `yaml:"region" json:"region,omitempty"` // add the sources whose region matches the visitor's province
}

type Source struct {
	ID             string   `yaml:"id" json:"id"`
	Name           string   `yaml:"name" json:"name"`
	Category       string   `yaml:"category" json:"category"`
	URL            string   `yaml:"url" json:"url"`
	Homepage       string   `yaml:"homepage" json:"homepage,omitempty"`
	Lang           string   `yaml:"lang" json:"lang,omitempty"`
	Region         string   `yaml:"region" json:"region,omitempty"` // province, for the "Mijn regio" preset
	Type           string   `yaml:"type" json:"-"`                  // rss|atom|rdf|json; empty = auto-detect
	DefaultEnabled bool     `yaml:"default_enabled" json:"default_enabled"`
	Paywall        bool     `yaml:"paywall" json:"paywall,omitempty"` // (some) articles need a subscription: € label
	Icon           string   `yaml:"-" json:"icon,omitempty"`          // set by the catalog: link to the site's icon
	Enabled        *bool    `yaml:"enabled" json:"-"`                 // nil = true
	Interval       Duration `yaml:"interval" json:"-"`
	MaxAge         Duration `yaml:"max_age" json:"-"` // overrides cache.max_age, e.g. for low-volume sources
}

func (s Source) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// AdvisorySource is a security-advisory feed. Format "ncsc" parses NCSC-NL titles
// (id, version, [kans/schade]); "rss" is any RSS/Atom feed with keyword severity.
type AdvisorySource struct {
	ID       string   `yaml:"id" json:"id"`
	Name     string   `yaml:"name" json:"name"`
	URL      string   `yaml:"url" json:"-"`
	Homepage string   `yaml:"homepage" json:"homepage,omitempty"`
	Format   string   `yaml:"format" json:"-"`
	Enabled  *bool    `yaml:"enabled" json:"-"`
	Interval Duration `yaml:"interval" json:"-"`
	Group    string   `yaml:"group" json:"group,omitempty"` // "edge": vendor PSIRT feeds, shown in the tab Edge-apparaten
	// Format ghsa: the GitHub security advisories of a repository (REST API, no key), e.g. OPNsense.
}

func (s AdvisorySource) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// OutageSource is a service status page. Format: statuspage (Atlassian Statuspage
// summary.json), rss (incidents in the last 24 h) or m365 (Microsoft 365 status page).
type OutageSource struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	Homepage string `yaml:"homepage"`
	Format   string `yaml:"format"`
	Enabled  *bool  `yaml:"enabled"`
}

func (s OutageSource) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

type Config struct {
	Server struct {
		Listen         string   `yaml:"listen"`
		BasePath       string   `yaml:"base_path"`
		TrustedProxies []string `yaml:"trusted_proxies"`
		LogLevel       string   `yaml:"log_level"`
		Metrics        bool     `yaml:"metrics"` // expose /metrics (Prometheus text format)
	} `yaml:"server"`
	Fetch struct {
		UserAgent       string   `yaml:"user_agent"`
		DefaultInterval Duration `yaml:"default_interval"`
		Timeout         Duration `yaml:"timeout"`
		MaxConcurrent   int      `yaml:"max_concurrent"`
	} `yaml:"fetch"`
	Cache struct {
		MaxItemsPerSource int      `yaml:"max_items_per_source"`
		MaxAge            Duration `yaml:"max_age"`
		SnapshotPath      string   `yaml:"snapshot_path"`
		IconCachePath     string   `yaml:"icon_cache_path"` // only the site icons (rare, small writes); default <snapshot_path>.icons.json
	} `yaml:"cache"`
	Features struct {
		AllowCustomFeeds bool `yaml:"allow_custom_feeds"`
		ShowImages       bool `yaml:"show_images"`
		SourceIcons      bool `yaml:"source_icons"`  // the news site's own small icon instead of a coloured dot
		IconServices     bool `yaml:"icon_services"` // fallback: DuckDuckGo's, then Google's favicon service (site name only)
		ProxyImages      bool `yaml:"proxy_images"`
		Geolocation      bool `yaml:"geolocation"`
	} `yaml:"features"`
	// Refresh: how often an open browser tab asks this server for new data, per panel.
	// Upstream fetching is set separately (fetch/weather/threats/... intervals).
	Refresh map[string]Duration `yaml:"refresh"`
	Keys    struct {
		AbusechAuthKey       string `yaml:"abusech_auth_key"`
		NSAPIKey             string `yaml:"ns_api_key"`             // NS Disruptions API (Treinstoringen)
		CloudflareRadarToken string `yaml:"cloudflare_radar_token"` // Dreigingsbeeld NL (Cloudflare API token, Radar read)
		NVDAPIKey            string `yaml:"nvd_api_key"`            // no longer used (the Kwetsbaarheden panel was removed)
	} `yaml:"keys"`
	Weather struct {
		Location   Location          `yaml:"location"`
		Interval   Duration          `yaml:"interval"`
		Units      string            `yaml:"units"`
		MeteoAlarm map[string]string `yaml:"meteoalarm"` // country code (nl, be) -> Atom feed URL
		Sea        bool              `yaml:"sea"`        // Zee en getij: tides and sea at the nearest coastal station
		TidesURL   string            `yaml:"tides_url"`  // Rijkswaterstaat water data, OphalenWaarnemingen
		MarineURL  string            `yaml:"marine_url"` // Open-Meteo Marine
	} `yaml:"weather"`
	Threats struct {
		Enabled       bool     `yaml:"enabled"`
		Interval      Duration `yaml:"interval"`       // ISC top ports / top IPs / infocon, Feodo
		DailyInterval Duration `yaml:"daily_interval"` // ISC 30-day summary
		CISAKEV       bool     `yaml:"cisa_kev"`       // optional "actief misbruikte kwetsbaarheden"
		URLhausNL     bool     `yaml:"urlhaus_nl"`     // tab Malware NL: active malware URLs hosted in the Netherlands
		URLhausNLURL  string   `yaml:"urlhaus_nl_url"` // abuse.ch URLhaus country feed (CSV, ~10 MB, every 3 hours)
		ThreatFox     bool     `yaml:"threatfox"`      // tab IOC's: newest indicators (needs the abuse.ch key)
		ThreatFoxURL  string   `yaml:"threatfox_url"`
	} `yaml:"threats"`
	// Exploits: tab Exploits in Security-adviezen (Exploit-DB and EPSS risers).
	Exploits struct {
		Enabled      bool   `yaml:"enabled"`
		ExploitDBURL string `yaml:"exploitdb_url"`
		EPSSURL      string `yaml:"epss_url"` // folder with the daily EPSS files (epss_scores-YYYY-MM-DD.csv.gz)
	} `yaml:"exploits"`
	// NLThreat: panel Dreigingsbeeld NL (Cloudflare Radar; incidents from the news in the page).
	NLThreat struct {
		Enabled  bool     `yaml:"enabled"`
		RadarURL string   `yaml:"radar_url"`
		Country  string   `yaml:"country"`
		Interval Duration `yaml:"interval"`
	} `yaml:"nlthreat"`
	Advisories []AdvisorySource `yaml:"advisories"`
	Alerts     struct {
		NCTV struct {
			Enabled  bool     `yaml:"enabled"`
			URL      string   `yaml:"url"`
			Interval Duration `yaml:"interval"`
		} `yaml:"nctv"`
		KNMI bool `yaml:"knmi"` // KNMI code in the top bar (from the MeteoAlarm NL feed)
	} `yaml:"alerts"`
	Traffic struct {
		Enabled  bool     `yaml:"enabled"`
		Interval Duration `yaml:"interval"`
		URL      string   `yaml:"url"`       // NDW DATEX II situation publication (.xml or .xml.gz)
		VILDBase string   `yaml:"vild_base"` // where VILD<version>.zip location tables live
	} `yaml:"traffic"`
	Alarms struct {
		Enabled  bool     `yaml:"enabled"`
		City     string   `yaml:"city"`     // default city slug; visitors can pick their own
		Base     string   `yaml:"base"`     // feed base: <base>/<city>.xml
		Interval Duration `yaml:"interval"` // cache per city
		// top bar: alerts per service in the last hour for an area of one or more cities
		Counts struct {
			Enabled  bool     `yaml:"enabled"`
			Label    string   `yaml:"label"`
			Cities   []string `yaml:"cities"`
			Interval Duration `yaml:"interval"`
		} `yaml:"counts"`
	} `yaml:"alarms"`
	// Energy: Energieprijzen panel (EnergyZero day-ahead prices, no key).
	Energy struct {
		Enabled          bool     `yaml:"enabled"`
		URL              string   `yaml:"url"`
		Interval         Duration `yaml:"interval"`
		VAT              float64  `yaml:"vat"`               // 0.21
		ElectricityExtra float64  `yaml:"electricity_extra"` // €/kWh added (energy tax, markup; incl. VAT), default 0
		GasExtra         float64  `yaml:"gas_extra"`         // €/m³ added, default 0
	} `yaml:"energy"`
	// Air: Luchtkwaliteit panel (Luchtmeetnet, nearest station to the visitor's location).
	Air struct {
		Enabled     bool     `yaml:"enabled"`
		Base        string   `yaml:"base"`
		StationsURL string   `yaml:"stations_url"` // RIVM list of measuring locations (CSV)
		HeatSmog    bool     `yaml:"heat_smog"`    // Hitte en smog: KNMI heat warning, RIVM news, ozone forecast
		Interval    Duration `yaml:"interval"`
	} `yaml:"air"`
	// Trains: Treinstoringen panel (NS Disruptions API v3; needs keys.ns_api_key).
	Trains struct {
		Enabled  bool     `yaml:"enabled"`
		URL      string   `yaml:"url"`
		Interval Duration `yaml:"interval"`
	} `yaml:"trains"`
	// Politics: Politiek vandaag panel (Tweede Kamer open data, no key).
	Politics struct {
		Enabled  bool     `yaml:"enabled"`
		Base     string   `yaml:"base"`
		Interval Duration `yaml:"interval"`
	} `yaml:"politics"`
	// Today: Vandaag panel (holidays, moon and clock are calculated; school holidays fetched).
	Today struct {
		Enabled   bool   `yaml:"enabled"`
		SchoolURL string `yaml:"school_url"`
		OnThisDay bool   `yaml:"on_this_day"` // Op deze dag: events on today's date from Dutch Wikipedia
		WikiURL   string `yaml:"wiki_url"`
	} `yaml:"today"`
	// Ransomware: Ransomware NL panel (ransomware.live API v2; free for personal use).
	Ransomware struct {
		Enabled   bool     `yaml:"enabled"`
		Base      string   `yaml:"base"`
		Countries []string `yaml:"countries"`
		Interval  Duration `yaml:"interval"`
	} `yaml:"ransomware"`
	// Breaches: the Datalekken panel (Have I Been Pwned breach list, no key needed).
	Breaches struct {
		Enabled          bool     `yaml:"enabled"`
		URL              string   `yaml:"url"`
		Interval         Duration `yaml:"interval"`
		IncludeSensitive bool     `yaml:"include_sensitive"` // e.g. adult sites; off by default
		Phishing         bool     `yaml:"phishing"`          // Oplichting en phishing: Fraudehelpdesk warnings
		PhishingURL      string   `yaml:"phishing_url"`
	} `yaml:"breaches"`
	Outages struct {
		Enabled   bool           `yaml:"enabled"`
		Interval  Duration       `yaml:"interval"`
		Providers []OutageSource `yaml:"providers"`
		// Internet: outage events for the country and large networks, detected by IODA (Georgia Tech).
		Internet struct {
			Enabled  bool   `yaml:"enabled"`
			Base     string `yaml:"base"`
			Country  string `yaml:"country"`
			Networks []struct {
				ASN  int    `yaml:"asn"`
				Name string `yaml:"name"`
			} `yaml:"networks"`
			Interval Duration `yaml:"interval"`
		} `yaml:"internet"`
	} `yaml:"outages"`
	// Economy: Economie in cijfers panel (Eurostat and ECB open data, no key).
	Economy struct {
		Enabled      bool     `yaml:"enabled"`
		EurostatBase string   `yaml:"eurostat_base"`
		ECBBase      string   `yaml:"ecb_base"`
		Interval     Duration `yaml:"interval"`
	} `yaml:"economy"`
	// Markets: Beurs panel (Yahoo Finance's unofficial spark endpoint; personal use).
	Markets struct {
		Enabled  bool           `yaml:"enabled"`
		URL      string         `yaml:"url"`
		Interval Duration       `yaml:"interval"`
		Indices  []MarketSymbol `yaml:"indices"`
		Stocks   []MarketSymbol `yaml:"stocks"` // for the top 3 risers and fallers (AEX)
	} `yaml:"markets"`
	// Pollen: Hooikoorts panel (Open-Meteo Air Quality API, CAMS Europe; no key).
	Pollen struct {
		Enabled bool   `yaml:"enabled"`
		URL     string `yaml:"url"`
	} `yaml:"pollen"`
	// Utilities: Kritieke infrastructuur panel (grid operators that publish outages).
	Utilities struct {
		Enabled    bool     `yaml:"enabled"`
		LianderURL string   `yaml:"liander_url"` // ArcGIS feature service layer (IStoringen)
		StedinURL  string   `yaml:"stedin_url"`  // Stedin outage API (per place)
		Interval   Duration `yaml:"interval"`
	} `yaml:"utilities"`
	// Quakes: Aardbevingen panel (KNMI FDSN event service; no key).
	Quakes struct {
		Enabled  bool     `yaml:"enabled"`
		URL      string   `yaml:"url"`
		Days     int      `yaml:"days"`
		Interval Duration `yaml:"interval"`
	} `yaml:"quakes"`
	// NLAlert: NL-Alert panel (the public API behind actueel.nl-alert.nl; no key).
	NLAlert struct {
		Enabled  bool     `yaml:"enabled"`
		URL      string   `yaml:"url"`
		Interval Duration `yaml:"interval"`
	} `yaml:"nlalert"`
	// Fuel: Brandstofprijzen panel (UnitedConsumers' daily national average, GLA; personal use).
	Fuel struct {
		Enabled  bool     `yaml:"enabled"`
		URL      string   `yaml:"url"`
		Interval Duration `yaml:"interval"`
	} `yaml:"fuel"`
	// Waste: Afvalkalender panel. Visitors set their own address; the server's default
	// address (optional) can use the opzet calendars, an iCal link or Home Assistant.
	Waste struct {
		Enabled       bool     `yaml:"enabled"`
		Providers     []string `yaml:"providers"`     // provider ids to use (empty = all), or https URLs of extra opzet calendars
		AppProviders  bool     `yaml:"app_providers"` // also providers that need the vendor app's key or a guest login
		Provider      string   `yaml:"provider"`      // default address: auto | <provider id> | ics | home_assistant
		Postcode      string   `yaml:"postcode"`
		Number        int      `yaml:"number"`
		Suffix        string   `yaml:"suffix"`
		ICSURL        string   `yaml:"ics_url"`
		HomeAssistant struct {
			URL      string   `yaml:"url"`
			Token    string   `yaml:"token"` // long-lived access token, or NDB_HA_TOKEN
			Entities []string `yaml:"entities"`
		} `yaml:"home_assistant"`
		Interval Duration `yaml:"interval"`
	} `yaml:"waste"`
	// Trending: words that suddenly appear in many sources' headlines (computed from the news cache).
	Trending struct {
		Enabled bool `yaml:"enabled"`
		// Wikipedia: a short summary per trending topic (hover or ⓘ), Wikipedia REST API.
		Wikipedia struct {
			Enabled bool   `yaml:"enabled"`
			URL     string `yaml:"url"`
		} `yaml:"wikipedia"`
	} `yaml:"trending"`
	// Push: Web Push notifications (needs HTTPS and a VAPID key, see -gen-vapid).
	Push struct {
		Enabled          bool    `yaml:"enabled"`
		Subject          string  `yaml:"subject"`           // mailto: or https: contact for the push services
		VAPIDPrivateKey  string  `yaml:"vapid_private_key"` // or NDB_VAPID_PRIVATE_KEY
		MaxSubscriptions int     `yaml:"max_subscriptions"`
		QuakeMinMag      float64 `yaml:"quake_min_mag"`
		BreakingSources  int     `yaml:"breaking_sources"` // 0 = no breaking-news messages
		WasteHour        int     `yaml:"waste_hour"`       // local hour of the evening reminder; -1 = off
	} `yaml:"push"`
	// Amber: AMBER Alert and Vermist Kind Alert (Burgernet open API, no key).
	Amber struct {
		Enabled  bool     `yaml:"enabled"`
		URL      string   `yaml:"url"`
		Interval Duration `yaml:"interval"`
	} `yaml:"amber"`
	// Satellite: Satellietbeeld panel (EUMETSAT view service, WMS; no key).
	Satellite struct {
		Enabled  bool     `yaml:"enabled"`
		URL      string   `yaml:"url"`   // GeoServer base URL
		Layer    string   `yaml:"layer"` // workspace:layer with a time dimension
		Interval Duration `yaml:"interval"`
	} `yaml:"satellite"`
	// Radiation: gamma dose rate of the RIVM stations (EURDEP via BfS, open WFS), in Luchtkwaliteit.
	Radiation struct {
		Enabled       bool     `yaml:"enabled"`
		URL           string   `yaml:"url"`
		Interval      Duration `yaml:"interval"`
		AlertUSv      float64  `yaml:"alert_usv"`      // "raised" at or above this dose rate (µSv/h) ...
		AlertStations int      `yaml:"alert_stations"` // ... at this many stations
	} `yaml:"radiation"`
	// Solar: expected solar yield in Energieprijzen (Open-Meteo irradiance on a tilted plane).
	Solar struct {
		Enabled bool    `yaml:"enabled"`
		URL     string  `yaml:"url"`
		KWp     float64 `yaml:"kwp"`     // default installation; 0 = visitors enter their own
		Tilt    int     `yaml:"tilt"`    // degrees from horizontal
		Azimuth int     `yaml:"azimuth"` // 0 = south, -90 = east, 90 = west
	} `yaml:"solar"`
	// Insects: Teken en muggen panel (an estimate from the Open-Meteo forecast).
	Insects struct {
		Enabled bool   `yaml:"enabled"`
		URL     string `yaml:"url"`
	} `yaml:"insects"`
	// Sky: Vanavond aan de hemel panel (computed; NOAA Kp forecast, Open-Meteo clouds).
	Sky struct {
		Enabled         bool   `yaml:"enabled"`
		KpURL           string `yaml:"kp_url"`
		CloudsURL       string `yaml:"clouds_url"`
		Launches        bool   `yaml:"launches"`          // the next rocket launches (Launch Library 2)
		LaunchesURL     string `yaml:"launches_url"`      // free tier: 15 requests per hour; fetched hourly
		LaunchesHours   int    `yaml:"launches_hours"`    // only launches in the coming hours (1-720); default 24
		SpaceWeather    bool   `yaml:"space_weather"`     // NOAA space-weather scales and solar flares
		SpaceWeatherURL string `yaml:"space_weather_url"` // NOAA SWPC services root; read every 30 minutes
	} `yaml:"sky"`
	// World: Wereldwijd panel (big earthquakes from USGS, natural events from NASA EONET).
	World struct {
		Enabled     bool     `yaml:"enabled"`
		USGSURL     string   `yaml:"usgs_url"`      // a GeoJSON summary feed (M4.5+ of the past week)
		EONETURL    string   `yaml:"eonet_url"`     // EONET v3 events
		QuakeMinMag float64  `yaml:"quake_min_mag"` // default 6
		FireMinHa   float64  `yaml:"fire_min_ha"`   // wildfires from this size (hectares); default 2000
		Hours       int      `yaml:"hours"`         // the panel's period: quakes and events of the last hours (1-168); default 24
		FireRisk    bool     `yaml:"fire_risk"`     // natuurbrandrisico per safety region in the Netherlands (brandweer.nl)
		FireRiskURL string   `yaml:"fire_risk_url"` // the public page; read hourly
		Water       bool     `yaml:"water"`         // high-water codes and storm-surge barriers (Rijkswaterstaat)
		WaterURL    string   `yaml:"water_url"`     // waterberichtgeving.rws.nl; read every 10 minutes
		Days        int      `yaml:"days"`          // 1.23.1, replaced by hours; still read (days × 24) when hours is not set
		Interval    Duration `yaml:"interval"`
	} `yaml:"world"`
	// Sports: Sportagenda panel (F1 via Jolpica; championships from events).
	Sports struct {
		Enabled  bool         `yaml:"enabled"`
		Sports   []string     `yaml:"sports"` // f1, road, mtb, athletics, football; visitors choose among these
		F1URL    string       `yaml:"f1_url"`
		Interval Duration     `yaml:"interval"`
		Events   []SportEvent `yaml:"events"`
	} `yaml:"sports"`
	// Vulns: the removed Kwetsbaarheden panel (1.14.0); still accepted so an old
	// config.yaml keeps loading, but ignored (configWarnings says so).
	Vulns yaml.Node `yaml:"vulns"`
	// UI: server-wide look.
	UI struct {
		Accent string `yaml:"accent"` // e.g. "#00a4dc"; empty = the default blue
	} `yaml:"ui"`
	Categories []Category `yaml:"categories"`
	Presets    []Preset   `yaml:"presets"`
	Sources    []Source   `yaml:"sources"`

	trusted []netip.Prefix
}

func defaultConfig() *Config {
	c := &Config{}
	c.Server.Listen = "127.0.0.1:8080"
	c.Server.BasePath = "/"
	c.Server.TrustedProxies = []string{"127.0.0.1", "::1"}
	c.Server.LogLevel = "info"
	c.Fetch.UserAgent = "Nieuwsdashboard/1.0"
	c.Fetch.DefaultInterval = Duration(15 * time.Minute)
	c.Fetch.Timeout = Duration(10 * time.Second)
	c.Fetch.MaxConcurrent = 6
	c.Cache.MaxItemsPerSource = 50
	c.Cache.MaxAge = Duration(72 * time.Hour)
	c.Features.Geolocation = true
	c.Features.SourceIcons, c.Features.IconServices = true, true
	c.Weather.Location = Location{Name: "Utrecht", Lat: 52.09, Lon: 5.12, Region: "Utrecht", Country: "NL"}
	c.Weather.Interval = Duration(15 * time.Minute)
	c.Weather.Units = "metric"
	c.Alerts.NCTV.Enabled = true
	c.Alerts.NCTV.URL = "https://www.nctv.nl/onderwerpen/d/dtn"
	c.Alerts.NCTV.Interval = Duration(6 * time.Hour)
	c.Alerts.KNMI = true
	c.Traffic.Enabled = true
	c.Traffic.Interval = Duration(5 * time.Minute)
	c.Traffic.URL = "https://opendata.ndw.nu/actueel_beeld.xml.gz"
	c.Traffic.VILDBase = "https://opendata.ndw.nu/"
	c.Alarms.Enabled = true
	c.Alarms.City = "utrecht"
	c.Alarms.Base = "https://zwaailicht.nl/feed/meldingen/"
	c.Alarms.Interval = Duration(2 * time.Minute)
	c.Alarms.Counts.Enabled = true
	c.Alarms.Counts.Label = "Den Haag"
	c.Alarms.Counts.Cities = []string{"den-haag"}
	c.Alarms.Counts.Interval = Duration(3 * time.Minute)
	c.Refresh = map[string]Duration{}
	for k, v := range defaultRefresh {
		c.Refresh[k] = Duration(v)
	}
	c.Energy.Enabled, c.Energy.URL, c.Energy.Interval, c.Energy.VAT = true, "https://api.energyzero.nl/v1/energyprices", Duration(time.Hour), 0.21
	c.Air.Enabled, c.Air.Base, c.Air.Interval = true, "https://api.luchtmeetnet.nl/open_api", Duration(30*time.Minute)
	c.Air.StationsURL = "https://data.rivm.nl/data/luchtmeetnet/Metadata/luchtmeetnet_meetlocaties.csv"
	c.Trains.Enabled, c.Trains.URL, c.Trains.Interval = true, "https://gateway.apiportal.ns.nl/disruptions/v3?isActive=true", Duration(5*time.Minute)
	c.Politics.Enabled, c.Politics.Base, c.Politics.Interval = true, "https://gegevensmagazijn.tweedekamer.nl/OData/v4/2.0", Duration(30*time.Minute)
	c.Today.Enabled, c.Today.SchoolURL = true, "https://opendata.rijksoverheid.nl/v1/infotypes/schoolholidays?output=json"
	c.Today.OnThisDay, c.Today.WikiURL = true, "https://nl.wikipedia.org"
	c.Breaches.Phishing, c.Breaches.PhishingURL = true, "https://www.fraudehelpdesk.nl/feed/?post_type=alert"
	c.Weather.Sea, c.Weather.TidesURL = true, "https://ddapi20-waterwebservices.rijkswaterstaat.nl/ONLINEWAARNEMINGENSERVICES/OphalenWaarnemingen"
	c.Weather.MarineURL = "https://marine-api.open-meteo.com/v1/marine"
	c.Ransomware.Enabled, c.Ransomware.Base, c.Ransomware.Countries, c.Ransomware.Interval = true, "https://api.ransomware.live/v2", []string{"NL"}, Duration(time.Hour)
	c.Breaches.Enabled = true
	c.Breaches.URL = "https://haveibeenpwned.com/api/v3/breaches"
	c.Breaches.Interval = Duration(3 * time.Hour)
	c.Outages.Enabled = true
	c.Outages.Interval = Duration(10 * time.Minute)
	c.Outages.Internet.Enabled, c.Outages.Internet.Base, c.Outages.Internet.Country = true, "https://api.ioda.inetintel.cc.gatech.edu/v2", "NL"
	c.Outages.Internet.Interval = Duration(30 * time.Minute)
	for _, n := range []struct {
		asn  int
		name string
	}{{1136, "KPN"}, {33915, "VodafoneZiggo"}, {50266, "Odido thuis"}, {31615, "Odido mobiel"}, {15435, "DELTA Fiber"}} {
		c.Outages.Internet.Networks = append(c.Outages.Internet.Networks, struct {
			ASN  int    `yaml:"asn"`
			Name string `yaml:"name"`
		}{n.asn, n.name})
	}
	c.Economy.Enabled, c.Economy.Interval = true, Duration(6*time.Hour)
	c.Economy.EurostatBase = "https://ec.europa.eu/eurostat/api/dissemination/statistics/1.0/data"
	c.Economy.ECBBase = "https://data-api.ecb.europa.eu/service/data"
	c.Markets.Enabled, c.Markets.URL, c.Markets.Interval = true, "https://query1.finance.yahoo.com/v8/finance/spark", Duration(15*time.Minute)
	c.Markets.Indices = []MarketSymbol{{"^AEX", "AEX"}, {"^AMX", "AMX"}, {"^BFX", "BEL 20"}, {"^GDAXI", "DAX"}, {"^STOXX50E", "Euro Stoxx 50"},
		{"^GSPC", "S&P 500"}, {"^IXIC", "Nasdaq"}, {"BZ=F", "Brent-olie ($)"}, {"GC=F", "Goud ($)"}, {"BTC-EUR", "Bitcoin (€)"}}
	c.Markets.Stocks = []MarketSymbol{{"ASML.AS", "ASML"}, {"SHELL.AS", "Shell"}, {"UNA.AS", "Unilever"}, {"PRX.AS", "Prosus"}, {"REN.AS", "RELX"},
		{"INGA.AS", "ING"}, {"ADYEN.AS", "Adyen"}, {"HEIA.AS", "Heineken"}, {"WKL.AS", "Wolters Kluwer"}, {"ASM.AS", "ASM International"},
		{"AD.AS", "Ahold Delhaize"}, {"PHIA.AS", "Philips"}, {"NN.AS", "NN Group"}, {"ABN.AS", "ABN AMRO"}, {"KPN.AS", "KPN"},
		{"UMG.AS", "Universal Music Group"}, {"EXO.AS", "Exor"}, {"AGN.AS", "Aegon"}, {"AKZA.AS", "Akzo Nobel"}, {"ASRNL.AS", "ASR Nederland"},
		{"BESI.AS", "BE Semiconductor"}, {"DSFIR.AS", "DSM-Firmenich"}, {"IMCD.AS", "IMCD"}, {"RAND.AS", "Randstad"}, {"MT.AS", "ArcelorMittal"}}
	c.Pollen.Enabled, c.Pollen.URL = true, "https://air-quality-api.open-meteo.com/v1/air-quality"
	c.Utilities.Enabled, c.Utilities.Interval = true, Duration(5*time.Minute)
	c.Utilities.LianderURL = "https://services1.arcgis.com/v6W5HAVrpgSg3vts/arcgis/rest/services/IStoringen_Productie_V7/FeatureServer/0"
	c.Utilities.StedinURL = "https://www.stedin.net/api/storingen/places"
	c.Quakes.Enabled, c.Quakes.URL, c.Quakes.Days, c.Quakes.Interval = true, "https://rdsa.knmi.nl/fdsnws/event/1/query", 14, Duration(15*time.Minute)
	c.NLAlert.Enabled, c.NLAlert.URL, c.NLAlert.Interval = true, "https://api.public-warning.app/api/v1/providers/nl-alert/alerts", Duration(2*time.Minute)
	c.Fuel.Enabled, c.Fuel.URL, c.Fuel.Interval = true, "https://www.unitedconsumers.com/tanken/brandstofprijzen", Duration(3*time.Hour)
	c.Waste.Enabled, c.Waste.Provider, c.Waste.Interval = true, "auto", Duration(24*time.Hour)
	c.Trending.Enabled = true
	c.Trending.Wikipedia.Enabled, c.Trending.Wikipedia.URL = true, "https://nl.wikipedia.org"
	c.Satellite.Enabled, c.Satellite.URL, c.Satellite.Layer, c.Satellite.Interval = true, "https://view.eumetsat.int/geoserver", "mtg_fd:rgb_geocolour", Duration(10*time.Minute)
	c.Amber.Enabled, c.Amber.URL, c.Amber.Interval = true, "https://services.burgernet.nl/landactiehost/api/v1/alerts", Duration(5*time.Minute)
	c.Insects.Enabled, c.Insects.URL = true, "https://api.open-meteo.com/v1/forecast"
	c.Radiation.Enabled, c.Radiation.URL, c.Radiation.Interval = true, "https://www.imis.bfs.de/ogc/opendata/ows", Duration(time.Hour)
	c.Radiation.AlertUSv, c.Radiation.AlertStations = 0.3, 3
	c.Solar.Enabled, c.Solar.URL, c.Solar.Tilt = true, "https://api.open-meteo.com/v1/forecast", 35
	c.Sky.Enabled, c.Sky.KpURL, c.Sky.CloudsURL = true, "https://services.swpc.noaa.gov/products/noaa-planetary-k-index-forecast.json", "https://api.open-meteo.com/v1/forecast"
	c.Sky.Launches, c.Sky.LaunchesURL, c.Sky.LaunchesHours = true, "https://ll.thespacedevs.com/2.3.0/launches/upcoming/?limit=10", 24
	c.World.Enabled, c.World.USGSURL, c.World.EONETURL = true, "https://earthquake.usgs.gov/earthquakes/feed/v1.0/summary/4.5_week.geojson", "https://eonet.gsfc.nasa.gov/api/v3/events?status=open&days=30"
	c.World.QuakeMinMag, c.World.FireMinHa, c.World.Interval = 6, 2000, Duration(30*time.Minute)
	c.World.FireRisk, c.World.FireRiskURL = true, "https://www.brandweer.nl/natuurbrandrisico/"
	c.World.Water, c.World.WaterURL = true, "https://waterberichtgeving.rws.nl"
	c.Sky.SpaceWeather, c.Sky.SpaceWeatherURL = true, "https://services.swpc.noaa.gov"
	c.Air.HeatSmog = true
	c.Sports.Enabled, c.Sports.Sports, c.Sports.F1URL, c.Sports.Interval = true, []string{"f1", "road", "mtb", "athletics", "football"}, "https://api.jolpi.ca/ergast/f1", Duration(time.Hour)
	c.Push.MaxSubscriptions, c.Push.QuakeMinMag, c.Push.BreakingSources, c.Push.WasteHour = 50, 2.5, 6, 19
	c.Threats.Enabled = true
	c.Threats.Interval = Duration(15 * time.Minute)
	c.Threats.DailyInterval = Duration(time.Hour)
	c.Threats.URLhausNL, c.Threats.URLhausNLURL = true, "https://urlhaus.abuse.ch/feeds/country/NL/"
	c.Threats.ThreatFox, c.Threats.ThreatFoxURL = true, "https://threatfox-api.abuse.ch/api/v1/"
	c.Exploits.Enabled, c.Exploits.ExploitDBURL, c.Exploits.EPSSURL = true, "https://www.exploit-db.com/rss.xml", "https://epss.empiricalsecurity.com"
	c.NLThreat.Enabled, c.NLThreat.RadarURL, c.NLThreat.Country, c.NLThreat.Interval = true, "https://api.cloudflare.com/client/v4/radar", "NL", Duration(30*time.Minute)
	c.Weather.MeteoAlarm = map[string]string{
		"nl": "https://feeds.meteoalarm.org/feeds/meteoalarm-legacy-atom-netherlands",
		"be": "https://feeds.meteoalarm.org/feeds/meteoalarm-legacy-atom-belgium",
	}
	return c
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseConfig(b)
}

// fileInDir: a path that ends with "/" or names an existing folder gets name appended.
func fileInDir(p, name string) string {
	if p == "" {
		return p
	}
	if strings.HasSuffix(p, "/") {
		return filepath.Join(p, name)
	}
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return filepath.Join(p, name)
	}
	return p
}

func parseConfig(b []byte) (*Config, error) {
	c := defaultConfig()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // typos in config.yaml are errors, not silently ignored
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.applyEnv()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

func (c *Config) applyEnv() {
	set := func(dst *string, key string) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			*dst = v
		}
	}
	set(&c.Server.Listen, "NDB_LISTEN")
	set(&c.Server.BasePath, "NDB_BASE_PATH")
	set(&c.Server.LogLevel, "NDB_LOG_LEVEL")
	set(&c.Fetch.UserAgent, "NDB_USER_AGENT")
	set(&c.Cache.SnapshotPath, "NDB_SNAPSHOT_PATH")
	set(&c.Cache.IconCachePath, "NDB_ICON_CACHE_PATH")
	// a folder is fine too: the file then gets its usual name inside it
	c.Cache.SnapshotPath = fileInDir(c.Cache.SnapshotPath, "cache.json.gz")
	c.Cache.IconCachePath = fileInDir(c.Cache.IconCachePath, "icons.json")
	if c.World.Hours == 0 { // world.days from 1.23.1 still works
		c.World.Hours = 24
		if c.World.Days > 0 {
			c.World.Hours = c.World.Days * 24
		}
	}
	set(&c.Keys.AbusechAuthKey, "ABUSECH_AUTH_KEY")
	set(&c.Keys.NSAPIKey, "NS_API_KEY")
	set(&c.Keys.CloudflareRadarToken, "CLOUDFLARE_RADAR_TOKEN")
	set(&c.Waste.HomeAssistant.Token, "NDB_HA_TOKEN")
	set(&c.Push.VAPIDPrivateKey, "NDB_VAPID_PRIVATE_KEY")
	if v := os.Getenv("NDB_TRUSTED_PROXIES"); v != "" { // e.g. the Docker gateway range
		c.Server.TrustedProxies = strings.Split(strings.ReplaceAll(v, " ", ""), ",")
	}
	if v := os.Getenv("NDB_METRICS"); v != "" {
		c.Server.Metrics = v == "1" || strings.EqualFold(v, "true")
	}
}

var (
	accentRe   = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	idRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	haEntityRe = regexp.MustCompile(`^[a-z_]+\.[a-z0-9_]{1,100}$`)
)

// defaultRefresh: browser refresh intervals per panel (config.yaml refresh:).
// removedRefresh: refresh keys of panels that no longer exist; accepted and ignored.
var removedRefresh = map[string]bool{"vulns": true}

var defaultRefresh = map[string]time.Duration{
	"news": 5 * time.Minute, "weather": 15 * time.Minute, "alerts": 3 * time.Minute,
	"traffic": 5 * time.Minute, "alarms": 2 * time.Minute, "threats": 15 * time.Minute,
	"advisories": 30 * time.Minute, "outages": 10 * time.Minute, "ap": 30 * time.Minute, "breaches": 30 * time.Minute,
	"energy": 30 * time.Minute, "air": 15 * time.Minute, "trains": 3 * time.Minute, "politics": 15 * time.Minute,
	"today": 60 * time.Minute, "ransomware": 30 * time.Minute,
	"pollen": 60 * time.Minute, "utilities": 5 * time.Minute, "quakes": 15 * time.Minute,
	"economy": 60 * time.Minute, "markets": 5 * time.Minute,
	"nlalert": 2 * time.Minute, "fuel": 60 * time.Minute, "waste": 60 * time.Minute, "trending": 10 * time.Minute,
	"insects": 60 * time.Minute, "sky": 30 * time.Minute, "sports": 30 * time.Minute, "amber": 5 * time.Minute, "satellite": 10 * time.Minute, "radiation": 30 * time.Minute, "solar": 60 * time.Minute, "world": 30 * time.Minute,
	"health": 30 * time.Minute,
}

func (c *Config) validate() error {
	for k, v := range c.Refresh {
		if removedRefresh[k] {
			continue // a removed panel: ignored, configWarnings mentions it
		}
		if _, ok := defaultRefresh[k]; !ok {
			return fmt.Errorf("refresh.%s: unknown panel (known: news, weather, alerts, traffic, alarms, energy, air, trains, politics, today, ransomware, pollen, utilities, quakes, economy, markets, nlalert, fuel, waste, trending, insects, sky, sports, amber, satellite, radiation, solar, world, threats, advisories, breaches, outages, ap, health)", k)
		}
		if v.D() < time.Minute || v.D() > 24*time.Hour {
			return fmt.Errorf("refresh.%s: %s is outside 1m..24h", k, v.D())
		}
	}
	var errs []string
	fail := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if c.Server.Listen == "" {
		fail("server.listen is empty")
	}
	bp := "/" + strings.Trim(c.Server.BasePath, "/") + "/"
	if bp == "//" {
		bp = "/"
	}
	c.Server.BasePath = bp
	if parseLevel(c.Server.LogLevel) == nil {
		fail("server.log_level %q must be debug, info, warn or error", c.Server.LogLevel)
	}
	c.trusted = nil
	for _, p := range c.Server.TrustedProxies {
		if pr, err := netip.ParsePrefix(p); err == nil {
			c.trusted = append(c.trusted, pr.Masked())
		} else if ip, err := netip.ParseAddr(p); err == nil {
			c.trusted = append(c.trusted, netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()))
		} else {
			fail("server.trusted_proxies: %q is not an IP or CIDR", p)
		}
	}
	if strings.TrimSpace(c.Fetch.UserAgent) == "" {
		fail("fetch.user_agent is empty")
	}
	if c.Fetch.DefaultInterval.D() < time.Minute {
		fail("fetch.default_interval must be at least 1m")
	}
	if t := c.Fetch.Timeout.D(); t < time.Second || t > time.Minute {
		fail("fetch.timeout must be between 1s and 1m")
	}
	if c.Fetch.MaxConcurrent < 1 || c.Fetch.MaxConcurrent > 32 {
		fail("fetch.max_concurrent must be between 1 and 32")
	}
	if c.Cache.MaxItemsPerSource < 1 || c.Cache.MaxItemsPerSource > 500 {
		fail("cache.max_items_per_source must be between 1 and 500")
	}
	if c.Cache.MaxAge.D() < time.Hour {
		fail("cache.max_age must be at least 1h")
	}
	l := c.Weather.Location
	if l.Lat < -90 || l.Lat > 90 || l.Lon < -180 || l.Lon > 180 {
		fail("weather.location lat/lon out of range")
	}
	if c.Weather.Interval.D() < 5*time.Minute {
		fail("weather.interval must be at least 5m")
	}
	if c.Weather.Units != "metric" {
		fail("weather.units: only \"metric\" is supported")
	}
	for cc, u := range c.Weather.MeteoAlarm {
		if cc != "nl" && cc != "be" {
			fail("weather.meteoalarm: unsupported country %q (use nl or be)", cc)
		}
		if u != "" && !isHTTPURL(u) {
			fail("weather.meteoalarm.%s: must be an http(s) URL", cc)
		}
	}

	if c.Threats.Interval.D() < 15*time.Minute {
		fail("threats.interval must be at least 15m (SANS ISC asks clients not to poll more often)")
	}
	if c.Threats.DailyInterval.D() < time.Hour {
		fail("threats.daily_interval must be at least 1h")
	}
	if c.Alerts.NCTV.Enabled && (!isHTTPURL(c.Alerts.NCTV.URL) || c.Alerts.NCTV.Interval.D() < time.Hour) {
		fail("alerts.nctv: url must be http(s) and interval at least 1h")
	}
	if c.Traffic.Enabled && (!isHTTPURL(c.Traffic.URL) || !isHTTPURL(c.Traffic.VILDBase) || c.Traffic.Interval.D() < 2*time.Minute) {
		fail("traffic: url and vild_base must be http(s), interval at least 2m")
	}
	if c.Alarms.Enabled && (!citySlugRe.MatchString(c.Alarms.City) || !isHTTPURL(c.Alarms.Base) || c.Alarms.Interval.D() < time.Minute) {
		fail("alarms: city must be a slug like den-haag, base an http(s) URL, interval at least 1m")
	}
	if cc := c.Alarms.Counts; cc.Enabled {
		// a busy feed covers little time (the national ambulance feed ~17 minutes), so polls stay frequent
		if cc.Interval.D() < time.Minute || cc.Interval.D() > 10*time.Minute {
			fail("alarms.counts.interval must be between 1m and 10m")
		}
		if strings.TrimSpace(cc.Label) == "" || len(cc.Cities) == 0 || len(cc.Cities) > 20 {
			fail("alarms.counts: needs a label and 1 to 20 cities")
		}
		for _, city := range cc.Cities {
			if !citySlugRe.MatchString(city) {
				fail("alarms.counts.cities: %q is not a city slug (e.g. den-haag)", city)
			}
		}
	}
	httpsURL := func(u string) bool { return isHTTPURL(u) && strings.HasPrefix(u, "https://") }
	if c.Threats.Enabled && ((c.Threats.URLhausNL && !httpsURL(c.Threats.URLhausNLURL)) || (c.Threats.ThreatFox && !httpsURL(c.Threats.ThreatFoxURL))) {
		fail("threats.urlhaus_nl_url and threats.threatfox_url must be https URLs")
	}
	if c.Exploits.Enabled && (!httpsURL(c.Exploits.ExploitDBURL) || !httpsURL(c.Exploits.EPSSURL)) {
		fail("exploits.exploitdb_url and exploits.epss_url must be https URLs")
	}
	if c.NLThreat.Enabled && (!httpsURL(c.NLThreat.RadarURL) || !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(c.NLThreat.Country) || c.NLThreat.Interval.D() < 15*time.Minute) {
		fail("nlthreat: radar_url must be an https URL, country a two-letter code (e.g. NL), interval at least 15m")
	}
	if c.Energy.Enabled && (c.Energy.Interval.D() < 15*time.Minute || !httpsURL(c.Energy.URL) || c.Energy.VAT < 0 || c.Energy.VAT > 1 ||
		math.Abs(c.Energy.ElectricityExtra) > 2 || math.Abs(c.Energy.GasExtra) > 5) {
		fail("energy: interval must be at least 15m, url https, vat 0–1, extras within ±2 €/kWh and ±5 €/m³")
	}
	if c.Air.Enabled && (c.Air.Interval.D() < 15*time.Minute || !httpsURL(c.Air.Base) || !httpsURL(c.Air.StationsURL)) {
		fail("air: interval must be at least 15m, base and stations_url https URLs")
	}
	if c.Trains.Enabled && (c.Trains.Interval.D() < 2*time.Minute || !httpsURL(c.Trains.URL)) {
		fail("trains: interval must be at least 2m and url an https URL")
	}
	if c.Ransomware.Enabled {
		if c.Ransomware.Interval.D() < 10*time.Minute || !httpsURL(c.Ransomware.Base) || len(c.Ransomware.Countries) == 0 || len(c.Ransomware.Countries) > 5 {
			fail("ransomware: interval must be at least 10m, base an https URL, and 1–5 countries")
		}
		for i, cc := range c.Ransomware.Countries {
			c.Ransomware.Countries[i] = strings.ToUpper(strings.TrimSpace(cc))
			if !rwCountryRe.MatchString(c.Ransomware.Countries[i]) {
				fail("ransomware.countries: %q is not an ISO country code such as NL", cc)
			}
		}
	}
	if c.Economy.Enabled && (c.Economy.Interval.D() < time.Hour || !httpsURL(c.Economy.EurostatBase) || !httpsURL(c.Economy.ECBBase)) {
		fail("economy: interval must be at least 1h and the URLs https")
	}
	if m := &c.Markets; m.Enabled {
		if m.Interval.D() < 5*time.Minute || !httpsURL(m.URL) || len(m.Indices)+len(m.Stocks) == 0 || len(m.Indices) > 20 || len(m.Stocks) > 60 {
			fail("markets: interval must be at least 5m, url https, 1–20 indices and at most 60 stocks")
		}
		for _, l := range [][]MarketSymbol{m.Indices, m.Stocks} {
			for _, s := range l {
				if !marketSymbolRe.MatchString(s.Symbol) || strings.TrimSpace(s.Name) == "" {
					fail("markets: %q needs a valid symbol (e.g. ^AEX, ASML.AS) and a name", s.Symbol)
				}
			}
		}
	}
	if c.Pollen.Enabled && !httpsURL(c.Pollen.URL) {
		fail("pollen.url must be an https URL")
	}
	if c.Utilities.Enabled && (c.Utilities.Interval.D() < 2*time.Minute || !httpsURL(c.Utilities.LianderURL) || !httpsURL(c.Utilities.StedinURL)) {
		fail("utilities: interval must be at least 2m and the URLs https")
	}
	if c.Quakes.Enabled && (c.Quakes.Interval.D() < 5*time.Minute || !httpsURL(c.Quakes.URL) || c.Quakes.Days < 1 || c.Quakes.Days > 365) {
		fail("quakes: interval must be at least 5m, url https, days 1–365")
	}
	if c.NLAlert.Enabled && (c.NLAlert.Interval.D() < time.Minute || !httpsURL(c.NLAlert.URL)) {
		fail("nlalert: interval must be at least 1m and url https")
	}
	if c.Fuel.Enabled && (c.Fuel.Interval.D() < time.Hour || !httpsURL(c.Fuel.URL)) {
		fail("fuel: interval must be at least 1h and url https")
	}
	if w := &c.Waste; w.Enabled {
		if w.Interval.D() < time.Hour {
			fail("waste.interval must be at least 1h")
		}
		if len(w.Providers) > 80 {
			fail("waste.providers: at most 80 entries")
		}
		for _, p := range w.Providers {
			if _, ok := wasteProviderByID(p); !ok && !httpsURL(p) {
				fail("waste.providers: %q is not a known provider id or an https URL", p)
			}
		}
		if len(enabledWasteProviders(c)) == 0 {
			fail("waste.providers: no provider left (check the ids and waste.app_providers)")
		}
		switch w.Provider {
		case "auto", "opzet":
			w.Postcode = normPostcode(w.Postcode)
			if w.Postcode != "" && (!postcodeRe.MatchString(w.Postcode) || w.Number < 1 || w.Number > 99999 || !wasteSuffixRe.MatchString(w.Suffix)) {
				fail("waste: default address needs a postcode like 2511AB, number 1–99999 and a suffix of at most 6 letters/digits (or leave postcode empty)")
			}
		case "ics":
			if w.ICSURL != "" && !isHTTPURL(w.ICSURL) {
				fail("waste.ics_url must be an http(s) URL")
			}
		case "home_assistant":
			ha := w.HomeAssistant
			if ha.URL != "" && (!isHTTPURL(ha.URL) || ha.Token == "" || len(ha.Entities) == 0 || len(ha.Entities) > 10) {
				fail("waste.home_assistant: url (http/https), token (or NDB_HA_TOKEN) and 1–10 entities are required")
			}
			for _, e := range ha.Entities {
				if !haEntityRe.MatchString(e) {
					fail("waste.home_assistant.entities: %q is not an entity id like sensor.afval_rest", e)
				}
			}
		default:
			p, ok := wasteProviderByID(w.Provider)
			w.Postcode = normPostcode(w.Postcode)
			if !ok || (p.App && !w.AppProviders) {
				fail("waste.provider must be auto, ics, home_assistant or a provider id (app providers need waste.app_providers: true)")
			} else if w.Postcode != "" && (!postcodeRe.MatchString(w.Postcode) || w.Number < 1 || w.Number > 99999 || !wasteSuffixRe.MatchString(w.Suffix)) {
				fail("waste: default address needs a postcode like 2511AB, number 1–99999 and a suffix of at most 6 letters/digits")
			}
		}
	}
	if c.Trending.Wikipedia.Enabled && !httpsURL(c.Trending.Wikipedia.URL) {
		fail("trending.wikipedia.url must be an https URL")
	}
	if c.Satellite.Enabled && (!httpsURL(c.Satellite.URL) || !satLayerRe.MatchString(c.Satellite.Layer) || c.Satellite.Interval.D() < 5*time.Minute) {
		fail("satellite: url must be an https URL, layer workspace:name, interval at least 5m")
	}
	if c.Amber.Enabled && (c.Amber.Interval.D() < time.Minute || !httpsURL(c.Amber.URL)) {
		fail("amber: interval must be at least 1m and url https")
	}
	if c.Radiation.Enabled && (!httpsURL(c.Radiation.URL) || c.Radiation.Interval.D() < 15*time.Minute ||
		c.Radiation.AlertUSv <= 0 || c.Radiation.AlertUSv > 100 || c.Radiation.AlertStations < 1) {
		fail("radiation: url must be https, interval at least 15m, alert_usv above 0 and alert_stations at least 1")
	}
	if c.Solar.Enabled && (!httpsURL(c.Solar.URL) || c.Solar.KWp < 0 || c.Solar.KWp > 1000 || c.Solar.Tilt < 0 || c.Solar.Tilt > 90 ||
		c.Solar.Azimuth < -180 || c.Solar.Azimuth > 180) {
		fail("solar: url must be https, kwp 0-1000, tilt 0-90, azimuth -180..180")
	}
	if c.Insects.Enabled && !httpsURL(c.Insects.URL) {
		fail("insects.url must be an https URL")
	}
	if c.Sky.Enabled && (!httpsURL(c.Sky.KpURL) || !httpsURL(c.Sky.CloudsURL) || (c.Sky.Launches && !httpsURL(c.Sky.LaunchesURL))) {
		fail("sky: kp_url, clouds_url and launches_url must be https URLs")
	}
	if w := c.World; w.Enabled && (!httpsURL(w.USGSURL) || !httpsURL(w.EONETURL) || w.QuakeMinMag < 4.5 || w.QuakeMinMag > 9 || w.FireMinHa < 0 || w.Hours < 1 || w.Hours > 168 || w.Interval.D() < 10*time.Minute) {
		fail("world: usgs_url and eonet_url must be https URLs, quake_min_mag 4.5-9, fire_min_ha at least 0, hours 1-168, interval at least 10m")
	}
	if c.World.Enabled && c.World.FireRisk && !httpsURL(c.World.FireRiskURL) {
		fail("world.fire_risk_url must be an https URL")
	}
	if c.World.Enabled && c.World.Water && !httpsURL(c.World.WaterURL) {
		fail("world.water_url must be an https URL")
	}
	if c.Sky.Enabled && c.Sky.SpaceWeather && !httpsURL(c.Sky.SpaceWeatherURL) {
		fail("sky.space_weather_url must be an https URL")
	}
	if c.Sky.Launches && (c.Sky.LaunchesHours < 1 || c.Sky.LaunchesHours > 720) {
		fail("sky.launches_hours must be 1-720")
	}
	if sp := &c.Sports; sp.Enabled {
		if sp.Interval.D() < 15*time.Minute || !httpsURL(sp.F1URL) || len(sp.Sports) == 0 || len(sp.Events) > 100 {
			fail("sports: interval at least 15m, f1_url https, at least one sport, at most 100 events")
		}
		for _, s := range sp.Sports {
			if _, ok := sportNames[s]; !ok {
				fail("sports.sports: %q is not one of f1, road, mtb, athletics, football", s)
			}
		}
		for _, e := range sp.Events {
			if err := validSportEvent(e); err != nil {
				fail("%v", err)
			}
		}
	}
	if a := c.UI.Accent; a != "" && !accentRe.MatchString(a) {
		fail("ui.accent must be a colour like \"#00a4dc\"")
	}
	if p := &c.Push; p.Enabled {
		if _, _, err := parseVAPIDKey(p.VAPIDPrivateKey); err != nil {
			fail("%v (push.vapid_private_key or NDB_VAPID_PRIVATE_KEY)", err)
		}
		if !strings.HasPrefix(p.Subject, "mailto:") && !httpsURL(p.Subject) {
			fail("push.subject must be a mailto: address or https URL (the push services contact you there)")
		}
		if p.MaxSubscriptions < 1 || p.MaxSubscriptions > 1000 || p.QuakeMinMag < 0 || p.QuakeMinMag > 9 ||
			p.BreakingSources < 0 || p.BreakingSources > 9 || p.WasteHour < -1 || p.WasteHour > 23 {
			fail("push: max_subscriptions 1–1000, quake_min_mag 0–9, breaking_sources 0–9 (0 = off), waste_hour -1–23")
		}
	}
	if in := &c.Outages.Internet; c.Outages.Enabled && in.Enabled {
		in.Country = strings.ToUpper(strings.TrimSpace(in.Country))
		if in.Interval.D() < 10*time.Minute || !httpsURL(in.Base) || !rwCountryRe.MatchString(in.Country) || len(in.Networks) > 10 {
			fail("outages.internet: interval must be at least 10m, base https, country an ISO code, at most 10 networks")
		}
		for _, n := range in.Networks {
			if n.ASN <= 0 || n.ASN > 4294967295 || strings.TrimSpace(n.Name) == "" {
				fail("outages.internet.networks: each needs an asn and a name")
			}
		}
	}
	if c.Today.Enabled && c.Today.OnThisDay && !httpsURL(c.Today.WikiURL) {
		fail("today.wiki_url must be an https URL")
	}
	if c.Breaches.Enabled && c.Breaches.Phishing && !httpsURL(c.Breaches.PhishingURL) {
		fail("breaches.phishing_url must be an https URL")
	}
	if c.Weather.Sea && (!httpsURL(c.Weather.TidesURL) || !httpsURL(c.Weather.MarineURL)) {
		fail("weather.tides_url and weather.marine_url must be https URLs")
	}
	if c.Today.Enabled && !httpsURL(c.Today.SchoolURL) {
		fail("today.school_url must be an https URL")
	}
	if c.Politics.Enabled && (c.Politics.Interval.D() < 10*time.Minute || !httpsURL(c.Politics.Base)) {
		fail("politics: interval must be at least 10m and base an https URL")
	}
	if c.Breaches.Enabled {
		if c.Breaches.Interval.D() < time.Hour {
			fail("breaches.interval must be at least 1h (the list is about 1 MB and changes a few times a week)")
		}
		if !isHTTPURL(c.Breaches.URL) || !strings.HasPrefix(c.Breaches.URL, "https://") {
			fail("breaches.url must be an https URL")
		}
	}
	if c.Outages.Interval.D() < 5*time.Minute {
		fail("outages.interval must be at least 5m")
	}
	outIDs := map[string]bool{}
	for i, s := range c.Outages.Providers {
		where := fmt.Sprintf("outages.providers[%d] (%s)", i, s.ID)
		if !idRe.MatchString(s.ID) || outIDs[s.ID] {
			fail("%s: invalid or duplicate id", where)
		}
		outIDs[s.ID] = true
		if strings.TrimSpace(s.Name) == "" || !isHTTPURL(s.URL) {
			fail("%s: needs a name and an http(s) url", where)
		}
		if s.Format != "statuspage" && s.Format != "rss" && s.Format != "m365" && s.Format != "gcp" {
			fail("%s: format must be statuspage, rss, m365 or gcp", where)
		}
	}
	advIDs := map[string]bool{}
	for i, s := range c.Advisories {
		where := fmt.Sprintf("advisories[%d] (%s)", i, s.ID)
		if !idRe.MatchString(s.ID) || advIDs[s.ID] {
			fail("%s: invalid or duplicate id", where)
		}
		advIDs[s.ID] = true
		if strings.TrimSpace(s.Name) == "" {
			fail("%s: name is empty", where)
		}
		if s.Group != "" && s.Group != "edge" {
			fail("%s: group must be empty or \"edge\"", where)
		}
		if s.IsEnabled() && !isHTTPURL(s.URL) {
			fail("%s: url must be an absolute http(s) URL", where)
		}
		if s.Format != "ncsc" && s.Format != "rss" && s.Format != "ghsa" {
			fail("%s: format must be ncsc, rss or ghsa", where)
		}
		if s.Interval != 0 && s.Interval.D() < 5*time.Minute {
			fail("%s: interval must be at least 5m", where)
		}
	}

	cats := map[string]bool{}
	for i, cat := range c.Categories {
		switch {
		case !idRe.MatchString(cat.ID):
			fail("categories[%d]: invalid id %q", i, cat.ID)
		case cats[cat.ID]:
			fail("categories: duplicate id %q", cat.ID)
		case strings.TrimSpace(cat.Name) == "":
			fail("categories[%d]: name is empty", i)
		}
		cats[cat.ID] = true
	}
	ids := map[string]bool{}
	for i, s := range c.Sources {
		where := fmt.Sprintf("sources[%d] (%s)", i, s.ID)
		if !idRe.MatchString(s.ID) {
			fail("%s: invalid id, use lowercase letters, digits and dashes", where)
		}
		if ids[s.ID] {
			fail("%s: duplicate id", where)
		}
		ids[s.ID] = true
		if strings.TrimSpace(s.Name) == "" {
			fail("%s: name is empty", where)
		}
		if !cats[s.Category] {
			fail("%s: unknown category %q", where, s.Category)
		}
		if s.IsEnabled() && !isHTTPURL(s.URL) {
			fail("%s: url must be an absolute http(s) URL", where)
		}
		if s.Homepage != "" && !isHTTPURL(s.Homepage) {
			fail("%s: homepage must be an absolute http(s) URL", where)
		}
		if s.Interval != 0 && s.Interval.D() < time.Minute {
			fail("%s: interval must be at least 1m", where)
		}
		if s.MaxAge != 0 && (s.MaxAge.D() < time.Hour || s.MaxAge.D() > 365*24*time.Hour) {
			fail("%s: max_age must be between 1h and 8760h", where)
		}
		switch s.Type {
		case "", "rss", "atom", "rdf", "json":
		default:
			fail("%s: type must be rss, atom, rdf or json", where)
		}
	}
	presetIDs := map[string]bool{}
	for i, p := range c.Presets {
		where := fmt.Sprintf("presets[%d] (%s)", i, p.ID)
		if !idRe.MatchString(p.ID) || presetIDs[p.ID] {
			fail("%s: invalid or duplicate id", where)
		}
		presetIDs[p.ID] = true
		if strings.TrimSpace(p.Name) == "" {
			fail("%s: name is empty", where)
		}
		if len(p.Sources) == 0 && !p.Region {
			fail("%s: needs sources or region: true", where)
		}
		for _, id := range p.Sources {
			if src, ok := c.sourceByID(id); !ok || !src.IsEnabled() {
				fail("%s: unknown or disabled source %q", where, id)
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c *Config) sourceByID(id string) (Source, bool) {
	for _, s := range c.Sources {
		if s.ID == id && s.IsEnabled() {
			return s, true
		}
	}
	return Source{}, false
}

func (c *Config) maxAge(s Source) time.Duration {
	if s.MaxAge != 0 {
		return s.MaxAge.D()
	}
	return c.Cache.MaxAge.D()
}

func (c *Config) interval(s Source) time.Duration {
	if s.Interval != 0 {
		return s.Interval.D()
	}
	return c.Fetch.DefaultInterval.D()
}

func parseLevel(s string) *slog.Level {
	var l slog.Level
	switch strings.ToLower(s) {
	case "debug":
		l = slog.LevelDebug
	case "info", "":
		l = slog.LevelInfo
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		return nil
	}
	return &l
}

// ---------------------------------------------------------------------------
// App

type App struct {
	cfgPath string
	level   *slog.LevelVar
	started time.Time

	mu     sync.RWMutex
	cfg    *Config
	cfgMod time.Time

	fetcher   *Fetcher
	sched     *Scheduler
	news      *NewsCache
	wx        *weatherCaches
	threats   *stateStore // threat panels and advisories
	geo       *geoCache
	images    *imageProxy
	metrics   *httpMetrics
	vild      atomic.Pointer[vildTable] // NDW location table for road names
	alarms    *ttlCache[[]Alarm]        // P2000 alerts per city slug
	air       *ttlCache[[]AirComponent] // pollutant values per Luchtmeetnet station
	pollen    *ttlCache[PollenData]     // pollen forecast per ~10 km cell
	p2k       map[string]*p2kCounter    // national alerts per service, last hour
	trend     trendCache                // trending words, recomputed at most every 5 min
	waste     *ttlCache[WasteResult]    // pickups per visitor address
	insects   *ttlCache[InsectData]     // tick/mosquito estimate per ~10 km cell
	wikiCache *ttlCache[WikiSummary]    // Wikipedia summary per trending term, 24 h
	solar     *ttlCache[[]SolarDay]     // solar yield per ~10 km cell, tilt and direction
	icons     *iconCache                // news site icons per host
	skyClouds *ttlCache[[]cloudPoint]   // cloud cover per ~10 km cell
	tides     *ttlCache[[]Tide]         // tide extremes per coastal station
	asnMu     sync.Mutex
	asnNames  map[int]string    // AS number -> organisation (public Shadowserver lookup)
	sea       *ttlCache[SeaNow] // sea temperature and waves per coastal station
	wasteIdx  wasteIndex        // address -> municipal calendar
	push      *pushHub          // Web Push subscriptions and watcher state
}

func (a *App) config() *Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

// applyConfig installs a validated config and reconciles the scheduler.
func (a *App) applyConfig(cfg *Config) {
	a.mu.Lock()
	old := a.cfg
	a.cfg = cfg
	a.mu.Unlock()
	a.level.Set(*parseLevel(cfg.Server.LogLevel))
	if cfg.Push.Enabled && a.push != nil {
		if err := a.push.setKey(cfg.Push.VAPIDPrivateKey); err != nil {
			slog.Error("push disabled", "err", err)
		}
	}
	if old != nil {
		if old.Server.Listen != cfg.Server.Listen || old.Server.BasePath != cfg.Server.BasePath {
			slog.Warn("server.listen/base_path changed; restart required to apply")
		}
		if old.Fetch.MaxConcurrent != cfg.Fetch.MaxConcurrent {
			slog.Warn("fetch.max_concurrent changed; restart required to apply")
		}
	}

	var jobs []Job
	keep := map[string]bool{}
	for _, s := range cfg.Sources {
		if !s.IsEnabled() {
			continue
		}
		s := s
		keep[s.ID] = true
		jobs = append(jobs, Job{
			Key:      "news:" + s.ID,
			Sig:      s.URL + "|" + s.Type,
			Interval: cfg.interval(s),
			Run:      func(ctx context.Context) error { return a.fetchSource(ctx, s) },
		})
	}
	a.news.prune(keep)
	jobs = append(jobs, a.threatJobs(cfg)...)
	a.sched.Set(append(jobs, a.p2kJobs(cfg)...))
}

// configWarnings lists settings that are probably missing: logged at startup and after a reload,
// so an administrator sees them in `docker logs` / journalctl instead of only in a panel.
func configWarnings(c *Config) (warn, info []string) {
	if c.Trains.Enabled && c.Keys.NSAPIKey == "" {
		warn = append(warn, "Treinstoringen: no NS API key set (keys.ns_api_key or NS_API_KEY); the panel shows a setup note. "+
			"With Docker, check that the variable reaches the container (env_file or an environment: line)")
	}
	if ua := strings.ToLower(c.Fetch.UserAgent); strings.Contains(ua, "example.nl") || strings.Contains(ua, "example.com") {
		warn = append(warn, "fetch.user_agent still contains the example contact; SANS ISC asks for your own site and e-mail (fetch.user_agent or NDB_USER_AGENT)")
	}
	if c.Vulns.Kind != 0 || c.Keys.NVDAPIKey != "" || c.Refresh["vulns"] != 0 {
		warn = append(warn, "the Kwetsbaarheden panel was removed in 1.14.2: vulns, keys.nvd_api_key and refresh.vulns are ignored and can be deleted from config.yaml")
	}
	if c.Threats.Enabled && c.Keys.AbusechAuthKey == "" {
		info = append(info, "abuse.ch: no Auth-Key set (optional; keys.abusech_auth_key or ABUSECH_AUTH_KEY)")
	}
	return warn, info
}

func logConfigWarnings(c *Config) {
	warn, info := configWarnings(c)
	for _, m := range warn {
		slog.Warn("config: " + m)
	}
	for _, m := range info {
		slog.Info("config: " + m)
	}
}

func (a *App) reload(reason string) {
	cfg, err := loadConfig(a.cfgPath)
	if err != nil {
		slog.Error("config reload failed, keeping previous config", "reason", reason, "err", err)
		return
	}
	if st, err := os.Stat(a.cfgPath); err == nil {
		a.mu.Lock()
		a.cfgMod = st.ModTime()
		a.mu.Unlock()
	}
	a.applyConfig(cfg)
	slog.Info("config reloaded", "reason", reason, "sources", len(cfg.Sources))
	logConfigWarnings(cfg)
}

// watchConfig reloads on SIGHUP and when the file's mtime changes (checked every 60 s).
func (a *App) watchConfig(ctx context.Context) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			a.reload("SIGHUP")
		case <-t.C:
			st, err := os.Stat(a.cfgPath)
			if err != nil {
				continue
			}
			a.mu.RLock()
			changed := !st.ModTime().Equal(a.cfgMod)
			a.mu.RUnlock()
			if changed {
				a.reload("file changed")
			}
		}
	}
}

func (a *App) snapshotLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.saveSnapshot()
		}
	}
}

func (a *App) saveSnapshot() {
	path := a.config().Cache.SnapshotPath
	if path == "" {
		return
	}
	if err := a.news.saveSnapshot(path); err != nil {
		slog.Error("snapshot write failed", "path", path, "err", err)
		return
	}
	slog.Debug("snapshot written", "path", path)
}

func run(cfgPath string) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	level := new(slog.LevelVar)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	a := &App{cfgPath: cfgPath, level: level, started: time.Now(), news: newNewsCache(), sched: newScheduler(), wx: newWeatherCaches(),
		threats: newStateStore(), geo: newGeoCache(10000), metrics: newHTTPMetrics(),
		alarms: newTTLCache[[]Alarm](500), air: newTTLCache[[]AirComponent](200), pollen: newTTLCache[PollenData](300), p2k: newP2KCounters(),
		push: newPushHub(), waste: newTTLCache[WasteResult](1000), insects: newTTLCache[InsectData](300), wikiCache: newTTLCache[WikiSummary](200), solar: newTTLCache[[]SolarDay](500), icons: newIconCache(), skyClouds: newTTLCache[[]cloudPoint](300), tides: newTTLCache[[]Tide](50), sea: newTTLCache[SeaNow](50), asnNames: map[int]string{}}
	if st, err := os.Stat(cfgPath); err == nil {
		a.cfgMod = st.ModTime()
	}
	a.images = newImageProxy(func() string { return a.config().Fetch.UserAgent })
	a.fetcher = newFetcher(cfg.Fetch.MaxConcurrent,
		func() string { return a.config().Fetch.UserAgent },
		func() time.Duration { return a.config().Fetch.Timeout.D() })
	if p := cfg.Cache.SnapshotPath; p != "" {
		if n, err := a.news.loadSnapshot(p, cfg); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("snapshot load failed", "path", p, "err", err)
		} else if err == nil {
			slog.Info("snapshot loaded", "path", p, "sources", n)
		}
	}
	if p := iconsPathFor(cfg); p != "" {
		if n, err := a.loadIcons(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("icon cache load failed", "path", p, "err", err)
		} else if err == nil && n > 0 {
			slog.Info("icon cache loaded", "path", p, "icons", n)
		}
	}
	checkWritable(cfg)
	a.applyConfig(cfg)
	a.loadPushState()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go a.sched.Loop(ctx)
	go a.watchConfig(ctx)
	if cfg.Cache.SnapshotPath != "" {
		go a.snapshotLoop(ctx)
	}

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           a.routes(cfg.Server.BasePath),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("nieuwsdashboard started", "version", version, "listen", cfg.Server.Listen,
		"base_path", cfg.Server.BasePath, "sources", len(cfg.Sources))
	logConfigWarnings(cfg)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	a.saveSnapshot()
	return nil
}

func main() {
	cfgPath := flag.String("config", envOr("NDB_CONFIG", "config.yaml"), "path to config.yaml (env NDB_CONFIG)")
	checkFeeds := flag.Bool("check-feeds", false, "fetch and parse every source, print a report and exit")
	only := flag.String("only", "", "with -check-feeds: comma-separated source ids to check")
	healthcheck := flag.Bool("healthcheck", false, "query /healthz on the local server and exit 0/1 (for Docker HEALTHCHECK)")
	showVersion := flag.Bool("version", false, "print version and exit")
	genVAPID := flag.Bool("gen-vapid", false, "print a new VAPID key for push notifications (NDB_VAPID_PRIVATE_KEY) and exit")
	flag.Parse()

	switch {
	case *showVersion:
		fmt.Println("nieuwsdashboard", version)
	case *genVAPID:
		k, err := genVAPIDKey()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("NDB_VAPID_PRIVATE_KEY=" + k)
	case *healthcheck:
		os.Exit(runHealthcheck(*cfgPath))
	case *checkFeeds:
		cfg, err := loadConfig(*cfgPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(runCheckFeeds(cfg, *only))
	default:
		if err := run(*cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runHealthcheck(cfgPath string) int {
	listen, base := envOr("NDB_LISTEN", "127.0.0.1:8080"), envOr("NDB_BASE_PATH", "/")
	if cfg, err := loadConfig(cfgPath); err == nil {
		listen, base = cfg.Server.Listen, cfg.Server.BasePath
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	base = "/" + strings.Trim(base, "/") + "/"
	if base == "//" {
		base = "/"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + base + "healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// HTTP

// csp allows only this origin. Inline scripts are allowed by their SHA-256 hash
// (computed from the embedded page at startup), not by 'unsafe-inline'. Styles
// keep 'unsafe-inline' because the page uses computed style attributes.
var csp = buildCSP(indexHTML)

var inlineScriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

func buildCSP(page []byte) string {
	var hashes []string
	for _, m := range inlineScriptRe.FindAllSubmatch(page, -1) {
		sum := sha256.Sum256(m[1])
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return "default-src 'self'; img-src 'self' https: data:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self' " + strings.Join(hashes, " ") + "; connect-src 'self'; worker-src 'self'; manifest-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(self), camera=(), microphone=(), payment=(), usb=(), "+
			"interest-cohort=(), browsing-topics=(), accelerometer=(), gyroscope=(), magnetometer=()")
		if r.Method == http.MethodPost && pushPostPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// pushPostPath: the only URLs that accept POST (push subscriptions; checked again in pushRequest).
func pushPostPath(p string) bool {
	for _, s := range []string{"/api/push/subscribe", "/api/push/unsubscribe", "/api/push/test"} {
		if strings.HasSuffix(p, s) {
			return true
		}
	}
	return false
}

func (a *App) routes(basePath string) http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, a.metrics.count(pattern, h)) }
	idx := newStaticAsset(bytes.Replace(indexHTML, []byte("__PAGE_ID__"), []byte(pageID), 1), "text/html; charset=utf-8")
	handle("GET /{$}", idx.serve)
	for name, asset := range pwaAssets(idx.etag) {
		handle("GET /"+name, asset.serve)
	}
	handle("GET /api/img", a.handleImage)
	handle("GET /api/catalog", a.handleCatalog)
	handle("GET /api/news", a.handleNews)
	handle("GET /api/weather", a.handleWeather)
	handle("GET /api/geocode", a.handleGeocode)
	handle("GET /api/threats", a.handleThreats)
	handle("GET /api/advisories", a.handleAdvisories)
	handle("GET /api/alerts", a.handleAlerts)
	handle("GET /api/traffic", a.handleTraffic)
	handle("GET /api/outages", a.handleOutages)
	handle("GET /api/breaches", a.handleBreaches)
	handle("GET /api/energy", a.handleEnergy)
	handle("GET /api/air", a.handleAir)
	handle("GET /api/trains", a.handleTrains)
	handle("GET /api/politics", a.handlePolitics)
	handle("GET /api/today", a.handleToday)
	handle("GET /api/ransomware", a.handleRansomware)
	handle("GET /api/pollen", a.handlePollen)
	handle("GET /api/utilities", a.handleUtilities)
	handle("GET /api/quakes", a.handleQuakes)
	handle("GET /api/economy", a.handleEconomy)
	handle("GET /api/markets", a.handleMarkets)
	handle("GET /api/alarms", a.handleAlarms)
	handle("GET /api/nlalert", a.handleNLAlert)
	handle("GET /api/fuel", a.handleFuel)
	handle("GET /api/waste", a.handleWaste)
	handle("GET /api/trending", a.handleTrending)
	handle("GET /api/insects", a.handleInsects)
	handle("GET /api/amber", a.handleAmber)
	handle("GET /api/wiki", a.handleWiki)
	handle("GET /api/icon", a.handleIcon)
	handle("GET /api/world", a.handleWorld)
	handle("GET /api/radiation", a.handleRadiation)
	handle("GET /api/solar", a.handleSolar)
	handle("GET /api/satellite", a.handleSatellite)
	handle("GET /api/satellite/image", a.handleSatelliteFile(false))
	handle("GET /api/satellite/overlay", a.handleSatelliteFile(true))
	handle("GET /api/sky", a.handleSky)
	handle("GET /api/sea", a.handleSea)
	handle("GET /api/exploits", a.handleExploits)
	handle("GET /api/nlthreat", a.handleNLThreat)
	handle("GET /api/sports", a.handleSports)
	handle("GET /api/push", a.handlePushInfo)
	handle("POST /api/push/subscribe", a.handlePushSubscribe)
	handle("POST /api/push/unsubscribe", a.handlePushUnsubscribe)
	handle("POST /api/push/test", a.handlePushTest)
	handle("GET /healthz", a.handleHealth)
	handle("GET /metrics", a.handleMetrics)

	var h http.Handler = mux
	if basePath != "/" {
		prefix := strings.TrimSuffix(basePath, "/")
		inner := http.StripPrefix(prefix, mux)
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == prefix {
				http.Redirect(w, r, basePath, http.StatusMovedPermanently)
				return
			}
			inner.ServeHTTP(w, r)
		})
	}
	return securityHeaders(h)
}

// staticAsset holds a precomputed gzip variant and ETag of an embedded file.
type staticAsset struct {
	body, gz    []byte
	etag, ctype string
}

func newStaticAsset(b []byte, ctype string) *staticAsset {
	sum := sha256.Sum256(b)
	return &staticAsset{body: b, gz: gzipBytes(b), etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ctype: ctype}
}

func (s *staticAsset) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", s.ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("Vary", "Accept-Encoding")
	h.Set("ETag", s.etag)
	if etagMatch(r.Header.Get("If-None-Match"), s.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := s.body
	if acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = s.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

var gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression); return w }}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzPool.Get().(*gzip.Writer)
	zw.Reset(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	gzPool.Put(zw)
	return buf.Bytes()
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q=")
			if !ok {
				return true
			}
			v, err := strconv.ParseFloat(q, 64)
			return err == nil && v > 0
		}
	}
	return false
}

func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || strings.TrimPrefix(t, "W/") == want {
			return true
		}
	}
	return false
}

// writeJSON writes v with a weak ETag, conditional 304, Cache-Control and optional gzip.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, maxAge int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(b)
	etag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	if maxAge > 0 {
		h.Set("Cache-Control", "max-age="+strconv.Itoa(maxAge))
	} else {
		h.Set("Cache-Control", "no-store")
	}
	h.Set("ETag", etag)
	h.Set("Vary", "Accept-Encoding, X-NDB-Page") // the page sends its build id: caches keep builds apart
	h.Set("X-NDB-Page", pageID)
	if status == http.StatusOK && etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if len(b) > 512 && acceptsGzip(r) {
		b = gzipBytes(b)
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}

func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeJSON(w, r, status, 0, map[string]string{"error": msg})
}

// clientIP returns the caller's address, honouring X-Forwarded-For only from trusted proxies.
func (a *App) clientIP(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip := ap.Addr().Unmap()
	trusted := func(ip netip.Addr) bool {
		for _, p := range a.config().trusted {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(ip) {
		return ip
	}
	// Walk X-Forwarded-For right to left, skipping trusted hops.
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		h = h.Unmap()
		if !trusted(h) {
			return h
		}
		ip = h
	}
	return ip
}

// ---------------------------------------------------------------------------
// API handlers

func (a *App) handleCatalog(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	sources := make([]Source, 0, len(cfg.Sources))
	for _, s := range cfg.Sources {
		if s.IsEnabled() {
			if cfg.Features.SourceIcons {
				s.Icon = a.iconURL(s)
			}
			sources = append(sources, s)
		}
	}
	advisories := []AdvisorySource{}
	for _, s := range cfg.Advisories {
		if s.IsEnabled() {
			advisories = append(advisories, s)
		}
	}
	writeJSON(w, r, http.StatusOK, 60, map[string]any{
		"version":    version,
		"categories": cfg.Categories,
		"sources":    sources,
		"features": map[string]bool{
			"allow_custom_feeds": cfg.Features.AllowCustomFeeds,
			"show_images":        cfg.Features.ShowImages,
		},
		"weather_location": cfg.Weather.Location,
		"threats":          cfg.Threats.Enabled,
		"traffic":          cfg.Traffic.Enabled,
		"outages":          cfg.Outages.Enabled,
		"breaches":         cfg.Breaches.Enabled,
		"energy":           cfg.Energy.Enabled,
		"air":              cfg.Air.Enabled,
		"trains":           cfg.Trains.Enabled,
		"politics":         cfg.Politics.Enabled,
		"today":            cfg.Today.Enabled,
		"ransomware":       cfg.Ransomware.Enabled,
		"pollen":           cfg.Pollen.Enabled,
		"utilities":        cfg.Utilities.Enabled,
		"quakes":           cfg.Quakes.Enabled,
		"economy":          cfg.Economy.Enabled,
		"markets":          cfg.Markets.Enabled,
		"nlalert":          cfg.NLAlert.Enabled,
		"fuel":             cfg.Fuel.Enabled,
		"waste":            cfg.Waste.Enabled,
		"waste_default":    cfg.Waste.Enabled && hasWasteDefault(cfg),
		"waste_providers":  wasteProviderList(cfg),
		"trending":         cfg.Trending.Enabled,
		"push":             cfg.Push.Enabled,
		"insects":          cfg.Insects.Enabled,
		"amber":            cfg.Amber.Enabled,
		"satellite":        cfg.Satellite.Enabled,
		"radiation":        cfg.Radiation.Enabled,
		"solar":            map[string]any{"enabled": cfg.Solar.Enabled, "kwp": cfg.Solar.KWp, "tilt": cfg.Solar.Tilt, "az": cfg.Solar.Azimuth},
		"trending_wiki":    cfg.Trending.Enabled && cfg.Trending.Wikipedia.Enabled,
		"sky":              cfg.Sky.Enabled,
		"world":            cfg.World.Enabled,
		"water":            cfg.World.Enabled && cfg.World.Water,
		"space_weather":    cfg.Sky.Enabled && cfg.Sky.SpaceWeather,
		"heat_smog":        cfg.Air.Enabled && cfg.Air.HeatSmog,
		"sports":           map[string]any{"enabled": cfg.Sports.Enabled, "sports": cfg.Sports.Sports},
		"accent":           cfg.UI.Accent,
		"alarms":           map[string]any{"enabled": cfg.Alarms.Enabled, "city": cfg.Alarms.City},
		"alerts":           map[string]bool{"nctv": cfg.Alerts.NCTV.Enabled, "knmi": cfg.Alerts.KNMI},
		"presets":          cfg.Presets,
		"advisories":       advisories,
		"refresh":          refreshSeconds(cfg.Refresh),
	})
}

func refreshSeconds(m map[string]Duration) map[string]int {
	out := make(map[string]int, len(defaultRefresh))
	for k, d := range defaultRefresh {
		out[k] = int(d.Seconds())
	}
	for k, d := range m {
		if removedRefresh[k] {
			continue
		}
		out[k] = int(d.D().Seconds())
	}
	return out
}

// requestedSources: the known source ids from a comma-separated list (max 300);
// an empty list means the sources that are on by default.
func requestedSources(cfg *Config, list string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, id := range strings.Split(list, ",") {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || len(ids) >= 300 {
			continue
		}
		if _, ok := cfg.sourceByID(id); ok {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if list == "" {
		for _, s := range cfg.Sources {
			if s.IsEnabled() && s.DefaultEnabled {
				ids = append(ids, s.ID)
			}
		}
	}
	return ids
}

func (a *App) handleNews(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	q := r.URL.Query()
	ids := requestedSources(cfg, q.Get("sources"))
	limit := 60
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		limit = min(max(v, 1), 500)
	}
	since, err := parseSince(q.Get("since"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "since: use RFC 3339 or unix seconds")
		return
	}
	var lang map[string]string // nil = no story grouping
	if q.Get("group") != "0" {
		lang = map[string]string{}
		for _, s := range cfg.Sources {
			lang[s.ID] = s.Lang
		}
	}
	perSource := 0 // at least this many articles per source, on top of the limit (max. 20)
	if v, err := strconv.Atoi(q.Get("per_source")); err == nil {
		perSource = min(max(v, 0), 20)
	}
	lists, status := a.news.collect(ids)
	items := topUpPerSource(mergeItems(lists, since, limit, lang), lists, since, perSource)
	if cfg.Features.ProxyImages {
		items = a.images.rewrite(items)
	}
	writeJSON(w, r, http.StatusOK, 60, map[string]any{
		"items":  items,
		"status": status,
	})
}

func parseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	return time.Parse(time.RFC3339, s)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	var ids []string
	for _, s := range cfg.Sources {
		if s.IsEnabled() {
			ids = append(ids, s.ID)
		}
	}
	_, status := a.news.collect(ids)
	ok := 0
	for _, s := range status {
		if s.OK {
			ok++
		}
	}
	writeJSON(w, r, http.StatusOK, 0, map[string]any{
		"status":     "ok",
		"version":    version,
		"started":    a.started.UTC().Truncate(time.Second),
		"sources_ok": ok,
		"sources":    status,
		"feeds":      a.otherFeeds(cfg), // threat intelligence and advisory sources
	})
}

// FeedStatus is the health of a non-news source (threat intelligence, advisories).
type FeedStatus struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"` // threat | advisory
	OK        bool       `json:"ok"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
	Error     string     `json:"error,omitempty"`
	ErrSince  *time.Time `json:"error_since,omitempty"`
}

func (a *App) otherFeeds(cfg *Config) []FeedStatus {
	var out []FeedStatus
	add := func(key, name, kind string) {
		st := a.threats.get(key)
		fs := FeedStatus{ID: key, Name: name, Kind: kind, OK: st.Err == "" && !st.FetchedAt.IsZero(), Error: st.Err}
		if !st.FetchedAt.IsZero() {
			t := st.FetchedAt.UTC().Truncate(time.Second)
			fs.FetchedAt = &t
		}
		if st.Err != "" && !st.ErrSince.IsZero() {
			t := st.ErrSince.UTC().Truncate(time.Second)
			fs.ErrSince = &t
		}
		out = append(out, fs)
	}
	if cfg.Threats.Enabled {
		add("isc:infocon", "SANS ISC · Infocon", "threat")
		add("isc:topports", "SANS ISC · aangevallen poorten", "threat")
		add("isc:daily", "SANS ISC · 30-daagse trend", "threat")
		add("isc:topips", "SANS ISC · top bron-IP's", "threat")
		add("abusech:feodo", "abuse.ch Feodo Tracker", "threat")
		if cfg.Threats.URLhausNL {
			add("urlhaus:nl", "abuse.ch URLhaus · malware in NL", "threat")
		}
		if cfg.Threats.ThreatFox && cfg.Keys.AbusechAuthKey != "" {
			add("threatfox:iocs", "abuse.ch ThreatFox · IOC's", "threat")
		}
		if cfg.Threats.CISAKEV {
			add("cisa:kev", "CISA KEV", "threat")
		}
	}
	if cfg.Exploits.Enabled {
		add("exploitdb", "Exploit-DB · exploits", "threat")
		add("epss:risers", "FIRST EPSS · stijgers", "threat")
	}
	if cfg.NLThreat.Enabled && cfg.Keys.CloudflareRadarToken != "" {
		add("radar:nl", "Cloudflare Radar · aanvallen en BGP", "threat")
	}
	for _, s := range cfg.Advisories {
		if s.IsEnabled() {
			add("adv:"+s.ID, s.Name, "advisory")
		}
	}
	if cfg.Alerts.NCTV.Enabled {
		add("nctv", "NCTV · dreigingsniveau", "alert")
	}
	if cfg.Alarms.Enabled && cfg.Alarms.Counts.Enabled {
		for _, city := range cfg.Alarms.Counts.Cities {
			add("p2k:"+city, "Zwaailicht · "+city+" (tellingen)", "alert")
		}
	}
	if cfg.Traffic.Enabled {
		add("ndw:traffic", "NDW · verkeer", "traffic")
	}
	if cfg.Breaches.Enabled {
		add("hibp:breaches", "Have I Been Pwned · datalekken", "breach")
		if cfg.Breaches.Phishing {
			add("fhd:alerts", "Fraudehelpdesk · oplichting en phishing", "breach")
		}
	}
	if cfg.Energy.Enabled {
		add("energyzero", "EnergyZero · energieprijzen", "daily")
	}
	if cfg.Air.Enabled {
		add("lml:stations", "RIVM · meetstations luchtkwaliteit", "daily")
		add("lml:lki", "Luchtmeetnet · luchtkwaliteitsindex", "daily")
	}
	if cfg.Trains.Enabled && cfg.Keys.NSAPIKey != "" {
		add("ns:disruptions", "NS · treinstoringen", "daily")
	}
	if cfg.Politics.Enabled {
		add("tk:politics", "Tweede Kamer · agenda en stemmingen", "daily")
	}
	if cfg.Today.Enabled {
		add("rijk:schoolholidays", "Rijksoverheid · schoolvakanties", "daily")
		if cfg.Today.OnThisDay {
			add("wiki:onthisday", "Wikipedia · op deze dag", "daily")
		}
	}
	if cfg.Ransomware.Enabled {
		for _, cc := range cfg.Ransomware.Countries {
			add("rw:"+cc, "ransomware.live · "+cc, "breach")
		}
	}
	if cfg.Utilities.Enabled {
		add("grid:outages", "Netbeheerders · stroom- en gasstoringen", "daily")
	}
	if cfg.Quakes.Enabled {
		add("knmi:quakes", "KNMI · aardbevingen", "daily")
	}
	if cfg.Economy.Enabled {
		add("econ:figures", "Eurostat en ECB · economie", "daily")
	}
	if cfg.Markets.Enabled {
		add("yahoo:markets", "Yahoo Finance · beurs", "daily")
	}
	if cfg.NLAlert.Enabled {
		add("nlalert", "NL-Alert", "alert")
	}
	if cfg.Fuel.Enabled {
		add("fuel:gla", "UnitedConsumers · brandstofprijzen", "daily")
	}
	if cfg.Waste.Enabled && hasWasteDefault(cfg) {
		add("waste:calendar", "Afvalkalender (standaardadres)", "daily")
	}
	if cfg.Amber.Enabled {
		add("burgernet:amber", "Burgernet · AMBER Alert en Vermist Kind Alert", "alert")
	}
	if cfg.Satellite.Enabled {
		add(satKey, "EUMETSAT · satellietbeeld", "daily")
	}
	if cfg.World.Enabled {
		add("usgs:world", "USGS · aardbevingen wereldwijd", "daily")
		add("eonet:events", "NASA EONET · natuurrampen", "daily")
		if cfg.World.FireRisk {
			add("brandweer:firerisk", "Brandweer · natuurbrandrisico", "daily")
		}
		if cfg.World.Water {
			add("rws:water", "Rijkswaterstaat · hoogwater en stormvloedkeringen", "alert")
		}
	}
	if cfg.Radiation.Enabled {
		add(radKey, "RIVM via EURDEP · straling", "daily")
	}
	if cfg.Sky.Enabled {
		add("noaa:kp", "NOAA SWPC · noorderlichtverwachting", "daily")
		if cfg.Sky.SpaceWeather {
			add("noaa:space", "NOAA SWPC · ruimteweer", "daily")
		}
		if cfg.Sky.Launches {
			add("ll2:launches", "Launch Library 2 · raketlanceringen", "daily")
		}
	}
	if cfg.Sports.Enabled && slices.Contains(cfg.Sports.Sports, "f1") {
		add("f1:jolpica", "Jolpica · Formule 1", "daily")
	}
	if cfg.Outages.Enabled && cfg.Outages.Internet.Enabled {
		add("ioda:internet", "IODA · internetverstoringen", "outage")
	}
	if cfg.Outages.Enabled {
		for _, p := range cfg.Outages.Providers {
			if p.IsEnabled() {
				add("outage:"+p.ID, p.Name, "outage")
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Metrics (opt-in: server.metrics / NDB_METRICS=1), Prometheus text format.

type httpMetrics struct {
	mu   sync.Mutex
	hits map[[2]string]uint64 // {route, status code} -> requests
}

func newHTTPMetrics() *httpMetrics { return &httpMetrics{hits: map[[2]string]uint64{}} }

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

func (m *httpMetrics) count(route string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		h(sw, r)
		m.mu.Lock()
		m.hits[[2]string{route, strconv.Itoa(sw.code)}]++
		m.mu.Unlock()
	})
}

func promLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Server.Metrics {
		http.NotFound(w, r)
		return
	}
	var b strings.Builder
	metric := func(name, help, typ string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ) }
	metric("ndb_build_info", "Build version.", "gauge")
	fmt.Fprintf(&b, "ndb_build_info{version=%q} 1\n", promLabel(version))
	metric("ndb_start_time_seconds", "Process start time.", "gauge")
	fmt.Fprintf(&b, "ndb_start_time_seconds %d\n", a.started.Unix())

	var ids []string
	for _, s := range cfg.Sources {
		if s.IsEnabled() {
			ids = append(ids, s.ID)
		}
	}
	_, status := a.news.collect(ids)
	type row struct {
		id, kind      string
		ok            bool
		items, errors int
		last          *time.Time
	}
	var rows []row
	for _, id := range ids {
		s := status[id]
		rows = append(rows, row{id, "news", s.OK, s.Items, s.ErrorCount, s.FetchedAt})
	}
	for _, f := range a.otherFeeds(cfg) {
		rows = append(rows, row{f.ID, f.Kind, f.OK, -1, 0, f.FetchedAt})
	}
	metric("ndb_source_up", "1 if the last fetch of the source succeeded.", "gauge")
	for _, r := range rows {
		up := 0
		if r.ok {
			up = 1
		}
		fmt.Fprintf(&b, "ndb_source_up{source=\"%s\",kind=\"%s\"} %d\n", promLabel(r.id), r.kind, up)
	}
	metric("ndb_source_last_success_timestamp_seconds", "Time of the last successful fetch.", "gauge")
	for _, r := range rows {
		if r.last != nil {
			fmt.Fprintf(&b, "ndb_source_last_success_timestamp_seconds{source=\"%s\",kind=\"%s\"} %d\n", promLabel(r.id), r.kind, r.last.Unix())
		}
	}
	metric("ndb_source_items", "Items cached per news source.", "gauge")
	metric("ndb_source_consecutive_errors", "Consecutive failed fetches per news source.", "gauge")
	for _, r := range rows {
		if r.kind == "news" {
			fmt.Fprintf(&b, "ndb_source_items{source=\"%s\"} %d\n", promLabel(r.id), r.items)
			fmt.Fprintf(&b, "ndb_source_consecutive_errors{source=\"%s\"} %d\n", promLabel(r.id), r.errors)
		}
	}
	metric("ndb_http_requests_total", "HTTP requests by route and status code.", "counter")
	a.metrics.mu.Lock()
	keys := make([][2]string, 0, len(a.metrics.hits))
	for k := range a.metrics.hits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i][0]+keys[i][1] < keys[j][0]+keys[j][1] })
	for _, k := range keys {
		fmt.Fprintf(&b, "ndb_http_requests_total{route=\"%s\",code=\"%s\"} %d\n", promLabel(k[0]), k[1], a.metrics.hits[k])
	}
	a.metrics.mu.Unlock()
	a.images.mu.Lock()
	imgBytes, imgN := a.images.bytes, a.images.ll.Len()
	a.images.mu.Unlock()
	a.geo.mu.Lock()
	geoN := a.geo.ll.Len()
	a.geo.mu.Unlock()
	metric("ndb_image_cache_bytes", "Bytes held by the image proxy cache (max 50 MB).", "gauge")
	fmt.Fprintf(&b, "ndb_image_cache_bytes %d\nndb_image_cache_entries %d\n", imgBytes, imgN)
	metric("ndb_geo_cache_entries", "Cached ip-api lookups (max 10 000).", "gauge")
	fmt.Fprintf(&b, "ndb_geo_cache_entries %d\n", geoN)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	metric("go_goroutines", "Number of goroutines.", "gauge")
	fmt.Fprintf(&b, "go_goroutines %d\n", runtime.NumGoroutine())
	metric("go_memstats_heap_alloc_bytes", "Heap bytes in use.", "gauge")
	fmt.Fprintf(&b, "go_memstats_heap_alloc_bytes %d\n", ms.HeapAlloc)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, b.String())
}

// ---------------------------------------------------------------------------
// Image proxy (features.proxy_images): only URLs the server signed itself are
// fetched, every connection is checked against private ranges after DNS
// resolution (also after redirects), images become ≤ 320 px JPEG thumbnails,
// and results live in an in-memory LRU of at most 50 MB.

const (
	thumbWidth    = 320
	imgCacheBytes = 50 << 20
	imgMaxPixels  = 40_000_000
)

type imageProxy struct {
	allowIP func(string) bool // publicIP in production; tests may narrow it
	key     []byte
	client  *http.Client
	sem     chan struct{}
	limiter *rateLimiter
	ua      func() string

	mu    sync.Mutex
	ll    *list.List
	m     map[string]*list.Element
	bytes int
}

type imgEntry struct {
	url   string
	body  []byte
	ctype string
	etag  string
	err   bool // negative cache: failed recently
	at    time.Time
}

func newImageProxy(ua func() string) *imageProxy {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	p := &imageProxy{allowIP: publicIP}
	// Control runs for every outbound connection after DNS resolution (so also after
	// each redirect and on DNS rebinding): refuse anything that is not public.
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if !p.allowIP(host) {
			return fmt.Errorf("blocked non-public address %s", host)
		}
		return nil
	}}
	tr := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext, ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns: 16, IdleConnTimeout: 30 * time.Second,
	}
	p.key, p.ua, p.sem, p.limiter = key, ua, make(chan struct{}, 4), newRateLimiter(240, 120)
	p.ll, p.m = list.New(), map[string]*list.Element{}
	p.client = &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("more than 3 redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("redirect to non-https URL")
		}
		return nil
	}}
	return p
}

func (p *imageProxy) sign(u string) string {
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte(u))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// rewrite replaces image URLs by signed proxy URLs (relative, so base_path works).
func (p *imageProxy) rewrite(items []Item) []Item {
	for i := range items {
		if u := items[i].Image; strings.HasPrefix(u, "https://") {
			items[i].Image = "api/img?u=" + base64.RawURLEncoding.EncodeToString([]byte(u)) + "&s=" + p.sign(u)
		}
	}
	return items
}

func (p *imageProxy) verify(enc, sig string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(b) > 2048 {
		return "", false
	}
	u := string(b)
	if !hmac.Equal([]byte(sig), []byte(p.sign(u))) || !strings.HasPrefix(u, "https://") {
		return "", false
	}
	return u, true
}

func (p *imageProxy) cached(u string) (*imgEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	el, ok := p.m[u]
	if !ok {
		return nil, false
	}
	e := el.Value.(*imgEntry)
	if e.err && time.Since(e.at) > 10*time.Minute {
		p.ll.Remove(el)
		delete(p.m, u)
		return nil, false
	}
	p.ll.MoveToFront(el)
	return e, true
}

func (p *imageProxy) store(e *imgEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.m[e.url]; ok {
		p.bytes -= len(old.Value.(*imgEntry).body)
		p.ll.Remove(old)
	}
	p.m[e.url] = p.ll.PushFront(e)
	p.bytes += len(e.body)
	for p.bytes > imgCacheBytes && p.ll.Len() > 1 {
		last := p.ll.Back()
		le := last.Value.(*imgEntry)
		p.bytes -= len(le.body)
		p.ll.Remove(last)
		delete(p.m, le.url)
	}
}

func (p *imageProxy) get(ctx context.Context, u string, ip netip.Addr) (*imgEntry, error) {
	if e, ok := p.cached(u); ok {
		if e.err {
			return nil, errors.New("image unavailable")
		}
		return e, nil
	}
	if !p.limiter.allow(ip) {
		return nil, errRateLimited
	}
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	body, ctype, err := p.fetch(ctx, u)
	if err != nil {
		slog.Debug("image proxy", "url", u, "err", err)
		p.store(&imgEntry{url: u, err: true, at: time.Now()})
		return nil, err
	}
	sum := sha256.Sum256(body)
	e := &imgEntry{url: u, body: body, ctype: ctype, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, at: time.Now()}
	p.store(e)
	return e, nil
}

func (p *imageProxy) fetch(ctx context.Context, u string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", p.ua())
	req.Header.Set("Accept", "image/avif,image/webp,image/jpeg,image/png,image/gif;q=0.8")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", shortErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxBody {
		return nil, "", errors.New("image larger than 5 MB")
	}
	return thumbnail(body)
}

// thumbnail turns JPEG/PNG/GIF into a ≤ 320 px wide JPEG (on white, so
// transparency does not turn black). WebP cannot be decoded by the standard
// library (x/image would be an extra dependency), so it is passed through
// unchanged up to 250 KB; larger WebP images get no thumbnail.
func thumbnail(body []byte) ([]byte, string, error) {
	switch ct := http.DetectContentType(body); ct {
	case "image/webp":
		if len(body) > 250<<10 {
			return nil, "", errors.New("webp too large to pass through")
		}
		return body, ct, nil
	case "image/jpeg", "image/png", "image/gif":
	default:
		return nil, "", fmt.Errorf("not an image (%s)", ct)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > imgMaxPixels {
		return nil, "", errors.New("image dimensions out of range")
	}
	if format == "jpeg" && cfg.Width <= thumbWidth && len(body) <= 80<<10 {
		return body, "image/jpeg", nil // already small
	}
	src, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaleDown(src, thumbWidth), &jpeg.Options{Quality: 78}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

// scaleDown averages up to 4×4 source samples per destination pixel onto white.
func scaleDown(src image.Image, maxW int) *image.RGBA {
	b := src.Bounds()
	w, hh := b.Dx(), b.Dy()
	dw := min(w, maxW)
	dh := max(1, hh*dw/w)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	fx, fy := float64(w)/float64(dw), float64(hh)/float64(dh)
	sx, sy := min(4, max(1, int(fx))), min(4, max(1, int(fy)))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var r, g, bl, n uint32
			for j := 0; j < sy; j++ {
				for i := 0; i < sx; i++ {
					px := b.Min.X + int((float64(x)+(float64(i)+0.5)/float64(sx))*fx)
					py := b.Min.Y + int((float64(y)+(float64(j)+0.5)/float64(sy))*fy)
					cr, cg, cb, ca := src.At(min(px, b.Max.X-1), min(py, b.Max.Y-1)).RGBA()
					white := 0xffff - ca // premultiplied colour over white
					r, g, bl, n = r+cr+white, g+cg+white, bl+cb+white, n+1
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 0xff})
		}
	}
	return dst
}

func (a *App) handleImage(w http.ResponseWriter, r *http.Request) {
	if !a.config().Features.ProxyImages {
		http.NotFound(w, r)
		return
	}
	u, ok := a.images.verify(r.URL.Query().Get("u"), r.URL.Query().Get("s"))
	if !ok {
		http.Error(w, "invalid image signature", http.StatusForbidden)
		return
	}
	e, err := a.images.get(r.Context(), u, a.clientIP(r))
	if errors.Is(err, errRateLimited) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		http.Error(w, "image unavailable", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", e.ctype)
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("ETag", e.etag)
	if etagMatch(r.Header.Get("If-None-Match"), e.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(e.body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.body)
	}
}

// ---------------------------------------------------------------------------
// Installable web app: manifest, icons (drawn at startup) and service worker.

// pwaAssets returns the manifest, icons and service worker. The icons repeat the
// favicon (three #00a4dc bars on a dark rounded square); the maskable one keeps the bars
// inside the 80 % safe zone.
func pwaAssets(indexETag string) map[string]*staticAsset {
	manifest, _ := json.Marshal(map[string]any{
		"name": "Nieuws Hub", "short_name": "Nieuws Hub", "lang": "nl",
		"description": "Nieuws, weer en actuele cyberdreigingen op één pagina.",
		"start_url":   "./", "scope": "./", "display": "standalone",
		"background_color": "#000000", "theme_color": "#0f1115",
		"icons": []map[string]string{
			{"src": "icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "icon-maskable.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	})
	ver := version + "-" + strings.Trim(indexETag, `"`)
	return map[string]*staticAsset{
		"manifest.webmanifest": newStaticAsset(manifest, "application/manifest+json"),
		"icon-192.png":         newStaticAsset(drawIcon(192, false), "image/png"),
		"icon-512.png":         newStaticAsset(drawIcon(512, false), "image/png"),
		"icon-maskable.png":    newStaticAsset(drawIcon(512, true), "image/png"),
		"sw.js":                newStaticAsset([]byte(strings.Replace(serviceWorkerJS, "__VERSION__", ver, 1)), "text/javascript; charset=utf-8"),
	}
}

func drawIcon(size int, maskable bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	unit := float64(size) / 32 // icon is designed on a 32×32 grid
	cover := func(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d*unit)) }
	bars := [][4]float64{{8, 11, 24, 11}, {8, 16, 24, 16}, {8, 21, 18, 21}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := (float64(x)+0.5)/unit, (float64(y)+0.5)/unit
			bg := 1.0
			if !maskable { // rounded square, radius 6
				qx, qy := math.Abs(px-16)-10, math.Abs(py-16)-10
				d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - 6
				bg = cover(d)
			}
			if maskable { // shrink the bars into the safe zone
				px, py = 16+(px-16)/0.62, 16+(py-16)/0.62
			}
			fg := 0.0
			for _, b := range bars {
				bx, by := b[2]-b[0], b[3]-b[1]
				t := math.Max(0, math.Min(1, ((px-b[0])*bx+(py-b[1])*by)/(bx*bx+by*by)))
				d := math.Hypot(px-b[0]-bx*t, py-b[1]-by*t) - 1.25
				if maskable {
					d *= 0.62
				}
				fg = math.Max(fg, cover(d))
			}
			// bars (#00a4dc) over background (#0f1115)
			c := func(bgc, fgc float64) uint8 { return uint8(math.Round(bgc*(1-fg) + fgc*fg)) }
			img.SetNRGBA(x, y, color.NRGBA{c(0x0f, 0x00), c(0x11, 0xa4), c(0x15, 0xdc), uint8(math.Round(255 * bg))})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// serviceWorkerJS: network first with the last good response as offline
// fallback (marked with X-NDB-Offline), cache first for proxied thumbnails.
const serviceWorkerJS = `// Nieuwsdashboard service worker (served by the Go binary)
const CACHE = 'ndb-__VERSION__';
const MAX_IMAGES = 200;

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE)
    .then((c) => c.addAll(['./', 'manifest.webmanifest', 'icon-192.png']))
    .then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(caches.keys()
    .then((keys) => Promise.all(keys.filter((k) => k.startsWith('ndb-') && k !== CACHE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

function markOffline(resp) {
  const h = new Headers(resp.headers);
  h.set('X-NDB-Offline', '1');
  return new Response(resp.body, { status: resp.status, statusText: resp.statusText, headers: h });
}

async function trimImages(cache) {
  const keys = (await cache.keys()).filter((r) => new URL(r.url).pathname.endsWith('/api/img'));
  for (let i = 0; i < keys.length - MAX_IMAGES; i++) await cache.delete(keys[i]);
}

self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  const scope = new URL(self.registration.scope);
  if (url.origin !== scope.origin || !url.pathname.startsWith(scope.pathname)) return;
  const rel = url.pathname.slice(scope.pathname.length);
  if (rel === 'sw.js' || rel === 'healthz' || rel.startsWith('api/geocode')) return;

  if (rel === 'api/img') {
    e.respondWith(caches.open(CACHE).then(async (c) => {
      const hit = await c.match(req);
      if (hit) return hit;
      const r = await fetch(req);
      if (r.ok) { await c.put(req, r.clone()); trimImages(c); }
      return r;
    }));
    return;
  }

  e.respondWith(fetch(req).then((r) => {
    if (r.ok) { const copy = r.clone(); caches.open(CACHE).then((c) => c.put(req, copy)); }
    return r;
  }).catch(async () => {
    const c = await caches.open(CACHE);
    const hit = (await c.match(req)) || (req.mode === 'navigate' ? await c.match('./') : undefined);
    return hit ? markOffline(hit) : Response.error();
  }));
});

// Push notifications: the payload is JSON {title, body, url, tag}.
self.addEventListener('push', (e) => {
  let d = {};
  try { d = e.data ? e.data.json() : {}; } catch { d = { body: e.data ? e.data.text() : '' }; }
  e.waitUntil(self.registration.showNotification(d.title || 'Nieuws Hub', {
    body: d.body || '', tag: d.tag || undefined, icon: 'icon-192.png', badge: 'icon-192.png', data: { url: d.url || '' },
  }));
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const u = String(e.notification.data?.url || '');
  const target = /^https:\/\//.test(u) ? u : new URL(u.startsWith('#') ? './' + u : './', self.registration.scope).href;
  e.waitUntil((async () => {
    if (!/^https:\/\//.test(u) || u.startsWith(self.registration.scope)) {
      const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
      for (const w of wins) if (w.url.startsWith(self.registration.scope)) { await w.focus(); return w.navigate ? w.navigate(target) : null; }
    }
    return self.clients.openWindow(target);
  })());
});
`
