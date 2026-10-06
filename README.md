# Nieuws Hub

*Nieuws Hub* is the name shown in the app. The program, container and repository keep their technical names (`nieuwsdashboard`, `news-dashboard-docker`).

<img width="1148" height="779" alt="Image" src="https://github.com/user-attachments/assets/f2d6a544-6912-41b4-b63b-eea1ad19ae20" />

A fast, privacy-friendly **single-page news dashboard in Dutch, with an English interface**. It combines:

- **News** from about 80 selectable RSS/Atom feeds: NL, regional, tech & security, data breaches, finance, sport, international and Belgium.
- **Weather** for a configurable location: Open-Meteo forecast, Buienradar rain for the next 2 hours, and KNMI/KMI warnings via MeteoAlarm. **Zee en getij** at the coastal station nearest to the weather location: the next high and low tides (Rijkswaterstaat), sea temperature and waves (Open-Meteo Marine).
- **Live cyber threats**:
  - SANS ISC/DShield top ports, 30-day attacker trend and top source IPs
  - abuse.ch Feodo botnet C2 servers, **Malware in NL** (active URLhaus malware URLs on Dutch servers, per network) and **IOC's** (ThreatFox indicators and malware families; needs the free abuse.ch key). Malware URLs and indicators are shown defanged, never as links.
  - geolocation via ip-api.com
  - the ISC Infocon level
  - Autoriteit Persoonsgegevens enforcement news (panel "Autoriteit Persoonsgegevens") (last 14 days)
- **Security advisories**: NCSC-NL, with the `[kans/schade]` rating parsed into badges, plus optional CERT-EU, CISA, BSI and MSRC. Tabs **Edge-apparaten** (Fortinet, Palo Alto, Cisco and OPNsense, severity from the source, critical first, at most 8 per vendor; Fortinet titles get the product name) and **Exploits** (new public exploits from Exploit-DB and the CVEs whose EPSS score rose most in a week, marked when in CISA KEV).
- **Dreigingsbeeld NL:** incidents at Dutch organisations from your news (last 7 days; the headline must point to a Dutch organisation), DDoS attacks on the Netherlands (trend, attack types, origin countries) and BGP hijacks and route leaks involving Dutch networks (Cloudflare Radar; needs a free API token).
- **Top bar:** the current KNMI weather code (only while there is a warning), the number of P2000 alerts in the last hour per service for a configured area (default Den Haag), an active NL-Alert with its place (only while one is active), and the NCTV terrorism threat level.
- **Vandaag:** date and week number, sunrise and sunset, moon phase, the next public holiday, the next clock change, and school holidays for regio Noord, Midden and Zuid (the visitor's region highlighted). **Op deze dag**: four events on today's date from the Dutch Wikipedia day page (Dutch ones first).
- **Luchtkwaliteit:** the air quality index (1–11) and NO₂, PM2.5, PM10 and O₃ from the nearest Luchtmeetnet station. The place is chosen per visitor (default: their weather location). Below it the **radiation** (gamma dose rate) at the nearest RIVM station, with the national range; when several stations measure raised levels, a notice appears in the top bar and a push message can go out. Then **Hitte en smog**: whether the Nationaal Hitteplan is active (KNMI heat warning), RIVM's smog warnings, and the ozone forecast for 3 days.
- **Gezondheid:** two sections for the visitor's air-quality place. **Hooikoorts**: the pollen forecast (grass, birch, alder, mugwort, ragweed) for 3 days at the visitor's air-quality place, with indicative levels. **Teken en muggen**: an estimate of tick and mosquito activity for 3 days, from the weather.
- **Aardbevingen en natuurrampen:** two tabs. **Nederland**: earthquakes from KNMI (14 days), the KNMI wind warning when there is one, the natuurbrandrisico phase per safety region (Brandweer Nederland), and **high water and storm surge**: Rijkswaterstaat's water-safety code per river, lake and coast sector, the highest code expected in the next 24 hours, and whether the storm-surge barriers are open. At code orange or red, or with a closed barrier, a notice appears in the top bar and a push message can go out. **Wereld**: big earthquakes from USGS (M6+, last 24 hours, with tsunami warning and expected impact) and active natural disasters from NASA EONET (hurricanes and typhoons with wind speed, volcanic eruptions, floods, large wildfires). A summary line above the tabs; the panel remembers the chosen tab.
- **Kritieke infrastructuur:** current electricity and gas outages at Liander and Stedin (place, status, expected repair time, customers affected), planned work and outages resolved in the last 24 h.
- **AMBER Alert and Vermist Kind Alert:** while a child is being searched for, a prominent banner at the top with the name, description, photo and "call 112" (a Vermist Kind Alert only for visitors whose weather location lies in its area), plus a push notification. Source: the police's Burgernet open API.
- **NL-Alert:** active and recent NL-Alerts (last 14 days), marked when the visitor's weather location lies inside the alert area.
- **Afvalkalender:** the next waste collection days. Each visitor sets an own address (postcode and house number) under Instellingen, like the places for alarms and air quality; the server finds the provider that knows it among 51 built-in providers (municipal calendars, Ximmio, Amsterdam, HVC, RD4, ROVA and more; 60 with the optional app providers such as Mijn Afvalwijzer). An optional default address can also come from an iCal link or Home Assistant.
- **UV index** in the Weer panel: the highest UV of the next 24 hours and when.
- **Zonnestroom** in Energieprijzen: the expected yield of your solar panels today and tomorrow, and the sunniest 3 hours. Each visitor enters kWp, direction and tilt under Instellingen.
- **Satellietbeeld:** the latest Meteosat image of the Benelux from EUMETSAT, every 10 minutes: true colour by day, clouds and city lights at night, with coastlines and borders. The server fetches it and serves it itself.
- **Vanavond aan de hemel:** when it gets dark, the moon, the planets you can see tonight (when and in which direction), the chance of northern lights, the clouds and active meteor showers, **space weather** (the NOAA scales for geomagnetic storms, radiation storms and radio blackouts, now and for 3 days, and the strongest solar flare of the day) and the next rocket launches (Launch Library 2).
- **Sportagenda:** Formula 1 (next race with qualifying, last podium, standings) and the important races and tournaments of road cycling (classics, grand tours, EK, WK), mountain biking (EK, WK), athletics (NK, EK, WK, big marathons) and football (EK and WK only): per sport what is on now and the next three, with matching headlines while an event is on. Visitors choose their sports.
- **Brandstofprijzen:** the national average recommended pump price (GLA) for Euro95, diesel and LPG, with the change since yesterday.
- **Treinstoringen:** current rail disruptions and engineering works from the NS Disruptions API (needs a free key).
- **Economie in cijfers:** Dutch inflation (with the euro-area figure and a 12-month trend), unemployment, the ECB deposit rate and the euro in dollars, from Eurostat and the ECB.
- **Beurs:** the AEX, AMX, BEL 20, DAX, Euro Stoxx 50, S&P 500 and Nasdaq, Brent oil, gold and bitcoin, plus the top 3 risers and fallers of the AEX (delayed prices, Yahoo Finance).
- **Energieprijzen:** today's and tomorrow's hourly electricity prices and the gas price (EnergyZero), with a chart and the cheapest 3 hours.
- **Politiek vandaag:** today's debates and committee meetings of the Tweede Kamer (or the next sitting day) and the latest votes.
- **Verkeer:** jams, accidents and road closures from NDW open data (Rijkswaterstaat), with readable road names.
- **Alarmeringen:** the latest P2000 alerts for your city from Zwaailicht.nl, grouped as Brandweer, Ambulance, Politie and Lifeliner (at most 2 each). The city is chosen per visitor under Instellingen.
- **Datalekken:** the latest 3 Dutch and 3 other data breaches at organisations, from Have I Been Pwned: number of accounts, leak date, and what data leaked. Below them **Oplichting en phishing**: the latest scam warnings of the Fraudehelpdesk.
- **Ransomware:** organisations claimed by ransomware groups on their leak sites (ransomware.live), with counts, the most active groups and the latest claims. No links to leak sites and no descriptions.
- **Internetstoringen:** **internet in the Netherlands** on top, then the status of Akamai, AWS, Cloudflare, Microsoft Azure, Microsoft 365, Google Cloud and STACKIT (in the order of `config.yaml`); internet: outages detected by IODA for the country and KPN, VodafoneZiggo, Odido and DELTA Fiber. Any service with an Atlassian Statuspage or RSS status feed can be added in `config.yaml`.
- **RIVM:** RIVM news of the last 14 days, filtered to health alerts (infectious diseases, vaccination, heat, smog).
- **Themes**: Licht / Donker (true black) / Auto.
- **Language**: Nederlands / English / Auto (browser language), switchable at the top and under Instellingen → Weergave. Only the interface is translated; news, advisories and alerts stay in their original language.

**Reading features:**
- **First visit:** pick topics (presets) instead of 80 switches.
- **Story grouping:** the same story from several outlets becomes one item, with "Ook bij: …" links and a **coverage view** (which outlets, when, and who reported first).
- **Trending:** words and names that suddenly appear in many headlines in the last 3 hours; click one to search. Hover over one (or tap ⓘ) for a short explanation from Wikipedia.
- **Search operators:** `bron:nos` or `source:nos` (only that source; `bron:"de volkskrant"` for names with a space), `"exact words"`, and `-word` or `-bron:x` to leave out.
- **Paywall label:** a € next to articles from sources marked `paywall: true` (some or all articles need a subscription).
- **Source icons:** each article shows the news site's own small icon instead of a coloured dot. The server fetches the sharpest icon the site offers (apple-touch-icon, a PNG icon or `/favicon.ico`) once a week and serves it itself. Sites that show the server only a cookie wall or block it get their icon from DuckDuckGo's or Google's favicon service instead; a site without any icon keeps the dot.
- **Data saver:** under Instellingen → Weergave: Automatisch / Aan / Uit. When on, the page refreshes 3× less often and loads no images (thumbnails, source icons, the satellite image only on request). Automatic turns it on when the browser asks to save data, on a 2G connection, or with a battery below 20 % that is not charging.
- **Read state:** read/unread plus a "Bewaard" list with **notes, labels** and export to Markdown or JSON.
- **OPML:** export the chosen sources, or import a list from another reader (only feeds that exist on this server are turned on).
- **Push notifications** (opt-in, per device): NL-Alert in your area, KNMI code orange/red, raised radiation, high water (code orange/red) or a closed storm-surge barrier, NCTV threat level, earthquakes, big news and the evening before waste collection. See [Push notifications](#push-notifications).
- **Watchlist and mute words:** security advisories that mention your products are pinned to the top.
- **Freshness:** every panel shows how old its data is.
- **Thumbnails:** optional, via the built-in image proxy.
- **Installable and offline-capable** (PWA). On a phone, swipe between the overview, the news and two panel pages (Dagelijks and Achtergrond, your choice per panel), and pull down to refresh. A share button on every article.
- **Finding a panel:** a bar above the panels stays in view while you scroll. **Ga naar paneel** lists your panels from A to Z and jumps to the one you pick (or press `g`, type a few letters and Enter); **Alles inklappen** turns every panel into one line with a short summary (temperature, electricity price, disruptions…), so all panels fit on about one screen. On a tablet, **Panelen ↓** next to the news jumps down to them.
- **Keyboard shortcuts:** press `?` in the app.
- **Overview and kiosk mode:** "Vandaag in het kort" puts the essentials of today on one screen; kiosk mode is for a wall display (see below).

It is built as **one Go binary with the frontend embedded, plus one `config.yaml`**:
- **No database.** All caching is in memory.
- **No disk writes** at runtime by default.
- **No third-party requests from the browser.**
- **Per-user preferences** (sources, theme, weather location, panel order) live in the browser's `localStorage`.

---

## Contents

1. [Quick start with Docker](#quick-start-with-docker)
2. [Configuration](#configuration)
3. [Adding or fixing a news source](#adding-or-fixing-a-news-source)
4. [Install on an ISPConfig VPS (systemd)](#install-on-an-ispconfig-vps-systemd)
5. [Docker on an ISPConfig VPS](#docker-on-an-ispconfig-vps)
6. [Updating](#updating) · [Push notifications](#push-notifications)
7. [Data sources, terms and licences](#data-sources-terms-and-licences)
8. [Privacy and disk writes](#privacy-and-disk-writes)
9. [Security](#security)
10. [Monitoring](#monitoring)
11. [HTTP API](#http-api)
12. [Development](#development)
13. [Possible extensions](#possible-extensions)
14. [Changelog](#changelog)
15. [License](#license) · [Disclaimer](#disclaimer)

---

## Quick start with Docker

```sh
git clone https://github.com/digibaro/news-dashboard-docker.git nieuwsdashboard && cd nieuwsdashboard
cp config.yaml.default config.yaml                  # your own settings; not tracked by git
cp docker-compose.yml.default docker-compose.yml    # your own ports/limits; not tracked by git
# Keys and your contact details go in .env (not tracked by git); every line reaches the container:
cat > .env <<'ENV'
NDB_USER_AGENT=Nieuwsdashboard/1.0 (+https://nieuws.example.nl; beheer@example.nl)
ABUSECH_AUTH_KEY=
NS_API_KEY=
CLOUDFLARE_RADAR_TOKEN=
ENV
chmod 600 .env
docker compose pull && docker compose up -d      # ready-made image from ghcr.io
# or build it yourself from this checkout:  docker compose build && docker compose up -d
```

- **`NDB_USER_AGENT`:** SANS ISC requires a User-Agent with your own site and e-mail.
- **`ABUSECH_AUTH_KEY`:** optional; a free key from <https://auth.abuse.ch/>.
- **`NS_API_KEY`:** for Treinstoringen; a free key from <https://apiportal.ns.nl>.
- **`CLOUDFLARE_RADAR_TOKEN`** (optional): for DDoS attacks and BGP events in Dreigingsbeeld NL; a free Cloudflare API token with the permission *Account → Radar → Read* (<https://dash.cloudflare.com/profile/api-tokens>).
- **`NDB_TAG`** (optional): which published image to download. `1` (default) is the newest 1.x, `1.7` the newest 1.7.x, `1.7.2` exactly that version.

**Download or build: your choice, every time.** The compose file has both an `image:` and a `build:` entry:

| | Command | What runs |
|---|---|---|
| **Ready-made image** | `docker compose pull && docker compose up -d` | the image GitHub Actions built and tested for the release (`ghcr.io/digibaro/news-dashboard-docker`, linux/amd64 and linux/arm64) |
| **Build from source** | `git pull && docker compose build && docker compose up -d` | an image built on your server from your checkout; the tests run during the build |

Whichever you ran last is what runs. A plain `docker compose up -d` never downloads or builds by itself while an image is present. The footer shows the running version.

The compose file loads `.env` with `env_file`, so a new variable only needs a line in `.env`. This needs Docker Compose 2.24 or newer; check with `docker compose version`. After editing `.env`, run `docker compose up -d`: `restart` keeps the old environment.

**Local changes without editing the compose file:** put them in `docker-compose.override.yml`, which `docker compose` merges automatically and git ignores. Your `docker-compose.yml` can then stay an unchanged copy of the template. Example: another port and an external proxy network:

```yaml
services:
  nieuwsdashboard:
    ports: !override          # replace the template's ports; without !override both would be published
      - "8082:8080"
    networks:
      - default
      - proxy_frontend

networks:
  proxy_frontend:
    external: true
```

**Missing settings are logged at startup:** a missing NS key and the example User-Agent as warnings, a missing abuse.ch key as info. Check with `docker logs nieuwsdashboard 2>&1 | grep config:`.

Open <http://localhost:8080>. The first fetch round takes about 20–30 seconds.

What you get:

- **Image:** about 11 MB. `distroless/static`, no shell, runs as a non-root user.
- **Container hardening:** runs with `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges` and a 128 MB memory limit, and needs no volumes. It typically uses 20–40 MB of RAM.
- **Health check:** `HEALTHCHECK` uses the binary's own `-healthcheck` flag, so no curl is needed in the image.
- **Logs:** go to stdout (`docker logs nieuwsdashboard`).

**Your own files:** `config.yaml` and `docker-compose.yml` are copies of the `.default` templates. They are in `.gitignore`, so `git pull` never overwrites them. After an update, compare them with the templates (`diff config.yaml.default config.yaml`, `diff docker-compose.yml.default docker-compose.yml`) to pick up new options. Keep only one compose file in the folder: when several exist (`compose.yaml`, `docker-compose.yml`, `docker-compose.yaml`), Compose warns and uses the first in that order.

**Config changes:** `config.yaml` is bind-mounted read-only. Many editors save by replacing the file, and a single-file bind mount keeps pointing at the old version. After editing, run `docker compose restart`.

---

## Configuration

Everything lives in `config.yaml`. The repository ships [`config.yaml.default`](config.yaml.default) as a template: copy it to `config.yaml` and edit the copy. `config.yaml` is in `.gitignore`, so your User-Agent, keys and local changes are never committed. The comments in the file explain each option. The file is re-read on `SIGHUP` and automatically when its modification time changes (checked every 60 s). An invalid file is rejected with a log line, and the previous configuration stays active. `server.listen`, `server.base_path` and `fetch.max_concurrent` need a restart.

| Section | What it controls |
|---|---|
| `server` | `listen` address, `base_path` (e.g. `/nieuws/` for a subfolder), `trusted_proxies` (whose `X-Forwarded-For` is believed), `log_level`, `metrics` (Prometheus endpoint, default off) |
| `fetch` | `user_agent` (**put your site and e-mail here**), default refresh `interval`, `timeout`, `max_concurrent` (max 2 per host is fixed) |
| `cache` | `max_items_per_source`, `max_age`, `snapshot_path` (empty = no disk writes, see below), `icon_cache_path` (only the site icons; ~85 KB, written about weekly) |
| `features` | `show_images` (keep feed images), `proxy_images` (serve them through `/api/img`, see below), `source_icons` (the news sites' own icons, default on), `icon_services` (fallback to DuckDuckGo's and Google's favicon services for sites that block the server, default on), `geolocation` (ip-api lookups), `allow_custom_feeds` (reserved, see below) |
| `refresh` | how often an open browser tab asks the server for new data, per panel: `news`, `alerts`, `weather`, `today`, `air`, `pollen`, `traffic`, `trains`, `alarms`, `quakes`, `nlalert`, `energy`, `fuel`, `economy`, `markets`, `waste`, `trending`, `amber`, `insects`, `sky`, `sports`, `satellite`, `radiation`, `solar`, `world`, `politics`, `threats`, `advisories`, `breaches`, `ransomware`, `utilities`, `outages`, `ap`, `health` (1m–24h, see below) |
| `keys` | `abusech_auth_key` (optional; also ThreatFox), `ns_api_key` (Treinstoringen), `cloudflare_radar_token` (Dreigingsbeeld NL; or `CLOUDFLARE_RADAR_TOKEN`) |
| `energy` | Energieprijzen: `enabled`, `url`, `interval` (min. 15m), `vat` (0.21), `electricity_extra` / `gas_extra` (€ added per kWh / m³, e.g. energy tax and markup; default 0) |
| `air` | Luchtkwaliteit: `enabled`, `base` (Luchtmeetnet API), `stations_url` (RIVM station list, CSV), `interval` (min. 15m), `heat_smog` (Hitte en smog, default true; the ozone forecast comes with the pollen request) |
| `trains` | Treinstoringen: `enabled`, `url` (NS Disruptions API v3), `interval` (min. 2m). Needs `keys.ns_api_key` |
| `pollen` | Hooikoorts: `enabled`, `url` (Open-Meteo Air Quality API) |
| `utilities` | Kritieke infrastructuur: `enabled`, `liander_url` (ArcGIS layer), `stedin_url`, `interval` (min. 2m) |
| `quakes` | Aardbevingen: `enabled`, `url` (KNMI FDSN), `days` (1–365, default 14; the panel shows the last 14 days, the overview card the last 7), `interval` (min. 5m) |
| `economy` | Economie in cijfers: `enabled`, `eurostat_base`, `ecb_base`, `interval` (default 6h, min. 1h) |
| `nlalert` | NL-Alert: `enabled`, `url`, `interval` (default 2m, min. 1m) |
| `fuel` | Brandstofprijzen: `enabled`, `url` (UnitedConsumers page), `interval` (default 3h, min. 1h) |
| `waste` | Afvalkalender: `enabled`, `providers` (provider ids to use, e.g. `[denhaag, hvc]`; a list replaces the default of all built-in providers; https URLs add extra opzet calendars), `app_providers` (default `false`; see *Data sources*), `interval` (default 24h, min. 1h; also how long a visitor address is cached). Visitors set their own address in the browser and can pick a provider or let the server find it. Optional **default address** (for visitors without one, and their push reminders) via `provider`: **auto** (or a provider id) `postcode`, `number`, `suffix` · **ics** `ics_url` · **home_assistant** `home_assistant.url`, `home_assistant.token` (or `NDB_HA_TOKEN`), `home_assistant.entities` (1–10 sensor ids). |
| `trending` | Trending words above the news: `enabled` (computed from the news cache, no extra requests); `wikipedia`: `enabled` (default true), `url` (default `https://nl.wikipedia.org`), a short summary per topic, only for current trending terms, cached 24 h per term |
| `radiation` | Straling in Luchtkwaliteit: `enabled`, `url` (BfS open WFS with EURDEP data), `interval` (default 60m, min. 15m), `alert_usv` (default 0.3) and `alert_stations` (default 3): "raised" when at least that many stations measure at least that dose rate |
| `solar` | Zonnestroom in Energieprijzen: `enabled`, `url` (Open-Meteo forecast), `kwp` (default 0: visitors enter their own panels; above 0 a default for everyone), `tilt` (default 35), `azimuth` (0 south, -90 east, 90 west) |
| `world` | The Wereld tab and natuurbrandrisico of Aardbevingen en natuurrampen: `enabled`, `usgs_url` (a USGS GeoJSON summary feed, default M4.5+ of the past week), `eonet_url` (NASA EONET v3), `quake_min_mag` (default 6), `fire_min_ha` (wildfires from this size, default 2000), `hours` (the Wereld tab's period, 1–168, default 24; the older `days` still works), `fire_risk` (default true) and `fire_risk_url` (natuurbrandrisico, read hourly from brandweer.nl), `water` (default true) and `water_url` (high water and storm-surge barriers, read every 10 minutes from waterberichtgeving.rws.nl), `interval` (default 30m, min. 10m) |
| `satellite` | Satellietbeeld: `enabled`, `url` (EUMETSAT GeoServer), `layer` (default `mtg_fd:rgb_geocolour`; e.g. `msg_fes:ir108` for infrared), `interval` (default 10m, min. 5m) |
| `amber` | AMBER Alert and Vermist Kind Alert: `enabled`, `url` (Burgernet Landactiehost; the test feed `.../api/test/alerts` cycles through test messages), `interval` (default 5m, min. 1m) |
| `insects` | Teken en muggen: `enabled`, `url` (Open-Meteo forecast) |
| `sky` | Vanavond aan de hemel: `enabled`, `kp_url` (NOAA SWPC), `clouds_url` (Open-Meteo), `launches` (default true), `launches_url` (Launch Library 2; free tier 15 requests per hour, fetched hourly) and `launches_hours` (only launches in the coming hours, default 24), `space_weather` (default true) and `space_weather_url` (NOAA SWPC, read every 30 minutes) |
| `sports` | Sportagenda: `enabled`, `sports` (`f1`, `road`, `mtb`, `athletics`, `football`; visitors choose among these), `f1_url` (Jolpica), `interval` (default 1h, min. 15m), `events` (races and tournaments: `sport` road/mtb/athletics/football, `name`, `start`, `end`, `place`, `url`, `note` (an extra line), `tentative` (dates not yet confirmed), `keywords` for matching headlines) |
| `ui` | `accent`: accent colour for all visitors, e.g. `"#00a4dc"` (empty = the default blue); adjusted automatically to a readable shade in light and dark mode |
| `push` | Push notifications (off by default): `enabled`, `subject` (`mailto:` or https contact), `vapid_private_key` (or `NDB_VAPID_PRIVATE_KEY`), `max_subscriptions` (default 50), `quake_min_mag` (2.5), `breaking_sources` (6; 0 = off), `waste_hour` (19; -1 = off). See [Push notifications](#push-notifications). |
| `markets` | Beurs: `enabled`, `url` (Yahoo spark), `interval` (default 15m, min. 5m), `indices` (1–20, shown in order) and `stocks` (max. 60, the source of the top 3 risers and fallers), each `{ symbol, name }`. The default stocks are the AEX constituents; Euronext reviews them every quarter. |
| `today` | Vandaag: `enabled`, `school_url` (Rijksoverheid school holidays), `on_this_day` (default true) and `wiki_url` (Dutch Wikipedia, read hourly) |
| `ransomware` | Ransomware NL: `enabled`, `base` (ransomware.live API v2), `countries` (ISO codes, default `[NL]`, max. 5), `interval` (min. 10m) |
| `politics` | Politiek vandaag: `enabled`, `base` (Tweede Kamer OData), `interval` (min. 10m) |
| `weather` | default `location` (`name`, `lat`, `lon`, `region` = province for warnings, `country`), `interval`, MeteoAlarm feed URLs, `sea` (Zee en getij, default true), `tides_url` (Rijkswaterstaat water data) and `marine_url` (Open-Meteo Marine) |
| `threats` | `enabled`, `interval` (min. 15m, ISC's request), `daily_interval`, `cisa_kev`, `urlhaus_nl` + `urlhaus_nl_url` (tab Malware in NL, every 3 hours), `threatfox` + `threatfox_url` (tab IOC's, hourly) |
| `exploits` | tab Exploits in Security-adviezen: `enabled`, `exploitdb_url` (RSS, hourly), `epss_url` (folder with the daily EPSS files, every 6 hours) |
| `nlthreat` | Dreigingsbeeld NL: `enabled`, `radar_url` (Cloudflare Radar API), `country` (default NL), `interval` (default 30m, min. 15m) |
| `alerts` | top bar: `nctv` (`enabled`, `url`, `interval`, min. 1h) and `knmi` (`true`/`false`) |
| `traffic` | `enabled`, `interval` (min. 2m), `url` (NDW DATEX II publication), `vild_base` (where the VILD location tables live) |
| `alarms` | `enabled`, `city` (default city slug, e.g. `den-haag`), `base` (feed URL prefix), `interval` (cache per city, min. 1m); `counts` for the top bar: `label`, `cities` (one or more slugs, e.g. a whole safety region), `interval` (1m–10m) |
| `breaches` | Datalekken panel: `enabled`, `url` (HIBP breach list), `interval` (min. 1h, default 3h), `include_sensitive` (default `false`), `phishing` (default true) and `phishing_url` (Fraudehelpdesk alerts RSS, read every 2 hours) |
| `outages` | `enabled`, `interval` (min. 5m), `internet` (`enabled`, `base`, `country`, `networks`: `asn` + `name`, max. 10, `interval` min. 10m), `providers`: `id`, `name`, `url`, `homepage`, `format` (`statuspage` for any Atlassian Statuspage `summary.json` / `rss` / `m365` / `gcp` for Google Cloud's `incidents.json`) |
| `advisories` | advisory feeds: `format: ncsc` (parses the NCSC title) `rss` (any feed; severity from the feed when it states one, such as a CVSS score, otherwise from keywords) or `ghsa` (the GitHub security advisories of a repository, e.g. `https://api.github.com/repos/opnsense/core/security-advisories`); `group: edge` puts a feed in the tab Edge-apparaten |
| `categories`, `sources` | news categories (`short` = chip label; `name_en`/`short_en` for the English interface) and feeds (`region` = province, for the "Mijn regio" preset) |
| `presets` | topics offered on the first visit and under Instellingen → Bronnen: a list of `sources`, or `region: true` for the broadcaster matching the visitor's weather province; `name_en`/`description_en` for the English interface |

Environment variables override the file, so Docker users rarely need to edit it:

| Variable | Overrides |
|---|---|
| `NDB_CONFIG` | path to `config.yaml` (default `./config.yaml`) |
| `NDB_LISTEN` | `server.listen` |
| `NDB_BASE_PATH` | `server.base_path` |
| `NDB_LOG_LEVEL` | `server.log_level` |
| `NDB_USER_AGENT` | `fetch.user_agent` |
| `NDB_SNAPSHOT_PATH` | `cache.snapshot_path` |
| `NDB_ICON_CACHE_PATH` | `cache.icon_cache_path` |
| `ABUSECH_AUTH_KEY` | `keys.abusech_auth_key` |
| `NS_API_KEY` | `keys.ns_api_key` |
| `CLOUDFLARE_RADAR_TOKEN` | `keys.cloudflare_radar_token` |
| `NDB_TRUSTED_PROXIES` | `server.trusted_proxies` (comma-separated IPs/CIDRs) |
| `NDB_METRICS` | `server.metrics` (`true`/`false`) |

Unknown keys in `config.yaml` are an error, so typos don't pass silently.

### Refresh rates

There are two separate rates:

1. **Server → sources:** how often the server fetches upstream. This is set per source or panel. The server fetches once for all visitors, with jitter, conditional GETs and backoff on errors.
2. **Browser → server:** how often an open tab asks the server for its cached data. This is set under `refresh:`. These requests are cheap: they are served from memory, answer `304` when nothing changed, and are paused while the tab is hidden.

| Panel | Browser (`refresh:`) | Server fetches upstream |
|---|---|---|
| News | 5m | per source, `fetch.default_interval` 15m (some 30m–1h) |
| Top bar (NCTV, KNMI, P2000 counts) | 3m | NCTV 6h, KNMI 10m, counts 3m |
| Weather | 15m | forecast 15m, rain 5m, warnings 10m |
| Vandaag | 60m | school holidays daily (the rest is calculated) |
| Ransomware NL | 30m | 1h per country |
| Hooikoorts | 60m | on demand, cached 1h per ~10 km |
| Kritieke infrastructuur | 5m | 5m |
| Aardbevingen | 15m | 15m |
| Luchtkwaliteit | 15m | index 30m, station list daily |
| Traffic | 5m | 5m |
| Treinstoringen | 3m | 5m |
| Energieprijzen | 30m | 1h |
| Economie in cijfers | 60m | 6h (monthly and daily figures) |
| Beurs | 5m | 15m (prices are delayed ~15 min) |
| NL-Alert | 2m | 2m |
| Brandstofprijzen | 60m | 3h (the GLA changes once a day) |
| Afvalkalender | 60m | 6h |
| Trending | 10m | computed at most every 5 min |
| AMBER Alert | 5m | 5m (almost always an empty list) |
| Straling | 30m | 60m (hourly values, a few hours delayed) |
| Zonnestroom | 60m | on demand, cached 1h per ~10 km, tilt and direction |
| Wereldwijd | 30m | 30m (USGS and EONET); launches hourly |
| Satellietbeeld | 10m | 10m (EUMETSAT publishes every 10 min, ~25–30 min after the scan; the image is only downloaded when it is new) |
| Teken en muggen | 60m | on demand, cached 1h per ~10 km |
| Vanavond aan de hemel | 30m | computed on request; Kp forecast 3h, clouds cached 1h per ~10 km |
| Sportagenda | 30m | F1 1h; races and tournaments from the config |
| Politiek vandaag | 15m | 30m |
| Alarmeringen | 2m | 2m per city |
| Cyberdreigingen | 15m | 15m (ISC minimum), 30-day summary 1h |
| Security advisories | 30m | 15m |
| Datalekken | 30m | 3h (≈ 1 MB list) |
| Internetstoringen | 10m | status pages 10m, internet (IODA) 30m |
| AP actions, Gezondheid | 30m | 30m |

A browser refresh faster than the server's interval gives nothing new, so keep `refresh:` at or above the matching server interval.

---

## Adding or fixing a news source

Add one line under `sources:`, then reload. No code changes are needed:

```yaml
  - { id: omroep-flevoland, name: "Omroep Flevoland", category: regio,
      url: "https://www.omroepflevoland.nl/rss", homepage: "https://www.omroepflevoland.nl", lang: nl }
```

**Fields:**
- `id`: lowercase letters, digits and `-`. It must be unique.
- `name`, `url`, `homepage`.
- `category`: must be one of the ids under `categories:`.
- **Optional:**
  - `default_enabled: true` (on for first-time visitors)
  - `interval: 30m`
  - `max_age: 720h`, for low-volume sources such as the AP
  - `type: rss|atom|rdf|json`, normally auto-detected
  - `lang: nl|en|…`: stories are only grouped within one language
  - `region: "Utrecht"`: province, for the "Mijn regio" preset
  - `enabled: false`: keeps the source in the file but neither fetches it nor shows it
  - `paywall: true`: a € label on its articles (some or all need a subscription)

**Before relying on a feed, verify it:**

```sh
./nieuwsdashboard -check-feeds                    # all news + advisory feeds
./nieuwsdashboard -check-feeds -only nos-politiek,tweakers
docker compose run --rm nieuwsdashboard -check-feeds   # the same, inside Docker
```

Then reload: `systemctl reload nieuwsdashboard` or `docker compose restart`, or wait up to 60 s for the automatic re-read. A new source appears in everyone's list under Instellingen → Bronnen. A new category also needs an entry under `categories:`.

The report shows status, item count and the newest date per feed. When a feed fails or is stale, the checker looks for a working alternative: `<link rel="alternate">` autodiscovery on the homepage, then common paths (`/rss`, `/feed`, `/rss.xml`, …). It prints what works. Sources that can't be fixed stay in `config.yaml` with `enabled: false` and a `# TODO` comment explaining why. HTML pages are never scraped as a substitute.

**Recommended sources** can be added the same way. A source with `default_enabled: true` is also switched on for existing visitors who customised their list, unless they have already seen that source before.

**Presets (first-visit topics).** Add or change them under `presets:`; every listed source must exist and be enabled. Give a regional broadcaster a `region:` (e.g. `"Utrecht"`) so it is offered to visitors whose weather location is in that province.

**Story grouping.** Articles from different outlets are grouped under the newest one when:
- they are in the same `lang` and published within 36 h of each other
- they share at least 25 % of their meaningful words (title + start of summary), or 60 % of the shorter title's words

Each article is compared with the group's lead only, so unrelated stories don't chain together. Visitors can switch grouping off under *Weergave*.

---

## Install on an ISPConfig VPS (systemd)

Tested on Debian 12 with systemd 252, using exactly the unit below. It works the same on Ubuntu and on any systemd distribution. No runtime needs to be installed.

The only system packages needed are `ca-certificates` (for HTTPS feeds) and `procps` (provides `/bin/kill` for `systemctl reload`). Both are present on every normal Debian/Ubuntu server, and ISPConfig itself needs them.

### 1. Build or copy the binary

On any machine with Go ≥ 1.27, or with Docker:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o nieuwsdashboard .
# without Go installed:
docker run --rm -v "$PWD":/src -w /src golang:1.27-alpine \
  sh -c 'CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o nieuwsdashboard .'
```

Copy it together with the config to the VPS:

```sh
sudo install -d -m 0755 /opt/nieuwsdashboard
sudo install -m 0755 nieuwsdashboard /opt/nieuwsdashboard/nieuwsdashboard
sudo install -m 0644 config.yaml.default /opt/nieuwsdashboard/config.yaml
sudoedit /opt/nieuwsdashboard/config.yaml    # set fetch.user_agent, weather.location, …
```

### 2. System user

```sh
sudo useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin nieuwsdashboard
```

The service user only needs to *read* `/opt/nieuwsdashboard`, and the unit below makes that path read-only anyway.

### 3. systemd unit

Save as `/etc/systemd/system/nieuwsdashboard.service`:

```ini
[Unit]
Description=Nieuwsdashboard (news, weather and threat dashboard)
Documentation=file:///opt/nieuwsdashboard/README.md
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=nieuwsdashboard
Group=nieuwsdashboard
ExecStart=/opt/nieuwsdashboard/nieuwsdashboard -config /opt/nieuwsdashboard/config.yaml
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5s
Environment=NDB_LISTEN=127.0.0.1:8080
Environment=GOMEMLIMIT=96MiB
# Secrets such as ABUSECH_AUTH_KEY=... go in this optional file (chmod 600):
EnvironmentFile=-/etc/default/nieuwsdashboard

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ReadOnlyPaths=/opt/nieuwsdashboard
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
ProtectProc=invisible
ProcSubset=pid
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RemoveIPC=true
CapabilityBoundingSet=
AmbientCapabilities=
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged
SystemCallErrorNumber=EPERM
UMask=0077
MemoryMax=256M
# Only needed when cache.snapshot_path is set, e.g. /var/lib/nieuwsdashboard/cache.json.gz:
#StateDirectory=nieuwsdashboard

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now nieuwsdashboard
systemctl status nieuwsdashboard
journalctl -u nieuwsdashboard -f          # logs (stdout)
curl -s http://127.0.0.1:8080/healthz     # {"status":"ok",...}
```

`systemd-analyze security nieuwsdashboard` rates this unit **1.4 OK**. The process runs without capabilities, with `NoNewPrivileges` and a seccomp filter.

### 4. Website in ISPConfig

1. **Sites → Website → Add new website**:
   - Pick the domain, e.g. `nieuws.example.nl`.
   - Enable **SSL** and **Let's Encrypt SSL**.
   - No PHP is needed: set PHP to *Disabled*.
   - Save.
2. Open the website again → tab **Options**.
3. Paste the directives for your web server (below) and save. ISPConfig rewrites the vhost and reloads the web server.

**nginx**: paste into **nginx Directives**:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_read_timeout 30s;
}
```

**Apache**: paste into **Apache Directives**:

```apache
ProxyPreserveHost On
ProxyPass / http://127.0.0.1:8080/
ProxyPassReverse / http://127.0.0.1:8080/
RequestHeader set X-Forwarded-Proto "https" env=HTTPS
```

For Apache, the proxy modules must be enabled once, as root:

```sh
a2enmod proxy proxy_http headers && systemctl restart apache2
```

Notes:

- Keep `listen` on `127.0.0.1` so the dashboard is only reachable through the vhost.
- `server.trusted_proxies` already contains `127.0.0.1` and `::1`, so the rate limiters see the real client IP from `X-Forwarded-For`.
- **Subfolder instead of a (sub)domain:** set `server.base_path: "/nieuws/"` and use `location /nieuws/ { proxy_pass http://127.0.0.1:8080; … }` (nginx) or `ProxyPass /nieuws/ http://127.0.0.1:8080/nieuws/` (Apache).
- The app sends its own security headers (CSP, `nosniff`, `Referrer-Policy`, `Permissions-Policy`, `X-Frame-Options`), so you don't need to add them in the vhost. HSTS is best set by ISPConfig ("HSTS" option on the SSL tab).

---

## Docker on an ISPConfig VPS

Use the same proxy directives as above, but publish the container on localhost only. In your `docker-compose.yml` (copied from `docker-compose.yml.default`):

```yaml
    ports:
      - "127.0.0.1:8080:8080"
```

Then run `docker compose up -d`. Docker's own restart policy (`unless-stopped`) replaces the systemd unit, and the update timer below keeps the image current.

**Client IPs.** Requests from nginx/Apache on the host reach the container from the Docker gateway (`172.16.0.0/12`). The compose file therefore sets `NDB_TRUSTED_PROXIES=127.0.0.1,::1,172.16.0.0/12`, so the rate limits apply per visitor and not to everyone at once. When port 8080 is **published directly to the internet** without a proxy in front, remove `172.16.0.0/12`. On setups where Docker's userland proxy forwards external traffic, visitors would otherwise appear to come from the gateway and could set their own `X-Forwarded-For`.

---

## Updating

| What | systemd | Docker |
|---|---|---|
| New version | download the archive for your platform from the release, replace `/opt/nieuwsdashboard/nieuwsdashboard`, then `sudo systemctl restart nieuwsdashboard` | `git pull`, then **either** `docker compose pull && docker compose up -d` (ready-made image) **or** `docker compose build && docker compose up -d` (build it yourself). Your `config.yaml`, `.env` and compose files are left alone. |
| Edit sources/config | edit `config.yaml`, then `sudo systemctl reload nieuwsdashboard` (or wait ≤ 60 s) | edit `config.yaml`, then `docker compose restart` |

Updating is done by hand, so you decide when; the release notes on GitHub say what changed. There is no automatic updater. If you want one, a cron job can run the download commands from the table above.

**One-time step when coming from 1.7.1 or older:** copy the new template over your `docker-compose.yml`. Your local changes live in `docker-compose.override.yml` and `.env`:

```sh
git pull
cp docker-compose.yml docker-compose.yml.bak
cp docker-compose.yml.default docker-compose.yml
docker compose pull && docker compose up -d      # or: docker compose build && docker compose up -d
```

**One-time step when updating from 1.5.0 or older with Docker.** Up to 1.5.0 `docker-compose.yml` was part of the repository. Now it ships `docker-compose.yml.default`, and your own `docker-compose.yml` is not tracked. Before this first pull, git either refuses to update ("Your local changes … would be overwritten") or deletes the file. Keep your version like this:

```sh
cp docker-compose.yml /tmp/docker-compose.yml.mine   # safety copy of your settings
git checkout -- docker-compose.yml                   # drop local edits to the old tracked file
git pull                                             # removes the old tracked docker-compose.yml
cp /tmp/docker-compose.yml.mine docker-compose.yml   # put yours back: now untracked and ignored
```

Then keep your port and network changes in `docker-compose.override.yml`, copy the template over your `docker-compose.yml` (see the step above), and run `docker compose pull && docker compose up -d`. `git status` should show nothing.

**Coming from 1.5.1** (which used `docker-compose.yaml`): your `docker-compose.yaml` keeps working and stays ignored. To follow the new name, run `mv docker-compose.yaml docker-compose.yml`.

A restart starts with an empty cache, which fills within about 30 seconds (the site icons within 2 minutes). If you want the news and icons to be there immediately after a restart, see *snapshot* below; with Docker, uncomment the warm-start lines in `docker-compose.yml`.

---

## Push notifications

Visitors can get notifications on their phone or computer, also when the dashboard is closed. They choose the topics under *Instellingen → Meldingen*:
- AMBER Alert, and a Vermist Kind Alert in their area
- Raised radiation at several RIVM stations
- NL-Alert in their area (their weather location)
- KNMI code orange or red
- a change of the NCTV threat level
- high water: a Rijkswaterstaat sector at code orange or red, or a storm-surge barrier closing (once per episode)
- earthquakes from `push.quake_min_mag`
- big news: a story that `push.breaking_sources` sources reported within an hour
- waste: the evening before collection, at `push.waste_hour`, for the address set on that device (or the default address)

**Setup** (once):
1. The dashboard must be served over **HTTPS** (browsers only allow push on secure sites; `localhost` also works for testing).
2. Create a VAPID key:
   - Docker: `docker compose run --rm nieuwsdashboard -gen-vapid`
   - systemd: `/usr/local/bin/nieuwsdashboard -gen-vapid`
3. Put the printed `NDB_VAPID_PRIVATE_KEY=...` line in `.env` (Docker) or the unit's environment file. Keep it secret and keep it: a new key means every device has to turn notifications on again.
4. In `config.yaml`: `push.enabled: true` and `push.subject: "mailto:you@example.nl"` (the push services contact you there if something is wrong).
5. Restart. The *Meldingen* section appears in the settings.

Notes:
- **iPhone and iPad:** push works only when the dashboard is added to the home screen (Share → Add to Home Screen) and opened from there (iOS 16.4 or later).
- **After a restart** without `cache.snapshot_path`, the server has forgotten the subscriptions; they return automatically when each device opens the dashboard again. With a snapshot path they are kept.
- The first minute after a start only records the current state, so a restart never repeats old alerts.

---

## Data sources, terms and licences

The server fetches everything; browsers only talk to the dashboard itself.

| Source | Used for | Terms |
|---|---|---|
| Publishers' RSS/Atom feeds | news | Headlines and summaries remain the publishers' property; every item links to the original. Summaries are cut to ~300 characters, and images are off by default. |
| [Open-Meteo](https://open-meteo.com/) | forecast, geocoding | Free for **non-commercial** use (attribution). Commercial use needs an API subscription. |
| [Buienradar](https://www.buienradar.nl/) | rain next 2 h (NL/BE) | Free with attribution; not for commercial use without permission. |
| [MeteoAlarm](https://meteoalarm.org/) | KNMI/KMI warnings | CC BY 4.0 (see MeteoAlarm terms). |
| [SANS ISC / DShield](https://isc.sans.edu/) | ports, trend, top IPs, Infocon | CC BY-NC-SA, **non-commercial**. Requires a User-Agent with contact info; polled at most every 15 min (daily summary hourly). |
| [abuse.ch Feodo Tracker](https://feodotracker.abuse.ch/) | botnet C2 list | CC0. An `Auth-Key` is sent when configured. |
| [abuse.ch URLhaus](https://urlhaus.abuse.ch/) | Cyberdreigingen: Malware in NL | The country feed for NL (CSV, ~10 MB), every 3 hours; only URLs still online are counted. CC0. Network names via Shadowserver's public ASN lookup. |
| [abuse.ch ThreatFox](https://threatfox.abuse.ch/) | Cyberdreigingen: IOC's | `get_iocs` for the last 24 hours, hourly; needs the free abuse.ch Auth-Key. CC0. |
| Vendor PSIRT feeds | Security-adviezen: Edge-apparaten | Fortinet (filestore.fortinet.com), Palo Alto Networks and Cisco RSS; OPNsense via the GitHub security advisories API (no key). Citrix has no working feed. |
| [Exploit-DB](https://www.exploit-db.com/) | Security-adviezen: Exploits | The RSS feed of new exploits, hourly. |
| [FIRST EPSS](https://www.first.org/epss/) | Security-adviezen: Exploits | Two daily score files (~3 MB each: today and a week ago), compared every 6 hours. |
| [Cloudflare Radar](https://radar.cloudflare.com/) | Dreigingsbeeld NL | Layer-3 attack trend and types, layer-7 attack origins, BGP hijacks (confidence ≥ 5) and route leaks for NL; 5 requests per 30 minutes with your API token. CC BY-NC 4.0. |
| [ip-api.com](https://ip-api.com/) | IP → country/AS | Free tier is **non-commercial only**, HTTP-only, 15 batch requests/min (honoured via `X-Rl`/`X-Ttl`). Results cached 24 h. Can be disabled: `features.geolocation: false`. |
| [NCSC-NL](https://advisories.ncsc.nl/) | advisories | Public RSS. |
| [Autoriteit Persoonsgegevens](https://www.autoriteitpersoonsgegevens.nl/) | AP actions panel | Public RSS. |
| CISA KEV (optional) | exploited vulnerabilities | US government work, public domain. |
| [NCTV](https://www.nctv.nl/onderwerpen/d/dtn) | terrorism threat level | Public page. There is no feed or structured field, so only the sentence "… niveau N op een schaal van 5" is read, every 6 h. If the wording changes the badge says *onbekend*; it never guesses. This is the one deliberate exception to "no HTML scraping". |
| KNMI via MeteoAlarm | top-bar weather code | KNMI's own RSS (`rss_KNMIwaarschuwingen.xml`) has not been updated since October 2023, so the code comes from the MeteoAlarm feed that carries KNMI's warnings. |
| [NDW](https://www.ndw.nu/) | traffic | Open data (Rijkswaterstaat, provinces, municipalities), polled every 5 min (≈ 260 KB). ANWB has no public API, and its site is not scraped. Road names come from NDW's VILD location table: only its ~400 KB table is read from the 42 MB zip with HTTP range requests, kept in memory and refreshed weekly or when NDW switches versions. |
| Akamai, AWS, Cloudflare, Azure, Microsoft 365, Google Cloud, STACKIT | outages | The providers' public status feeds. Microsoft 365 uses the JSON behind status.cloud.microsoft (consumer services, undocumented). Google Cloud publishes `incidents.json` (all incidents with start, end, severity and affected locations; no key); STACKIT (Schwarz Digits) uses Atlassian Statuspage. The health of your own tenant would need Microsoft Graph with an app registration. |
| [RIVM](https://www.rivm.nl/) | health alerts | Public RSS. |
| [EnergyZero](https://www.energyzero.nl/) | Energieprijzen | The public price API behind EnergyZero's website (day-ahead EPEX prices). Not officially documented and no published terms (checked September 2026); fetched hourly, two small requests. |
| [Luchtmeetnet](https://www.luchtmeetnet.nl/) / [RIVM](https://data.rivm.nl/data/luchtmeetnet/) | Luchtkwaliteit | The index (every 30 min, ≈3 requests for all stations) and pollutants (on demand, cached 30 min) from the Luchtmeetnet API. Station locations come from RIVM's `luchtmeetnet_meetlocaties.csv`, one file checked daily, so no per-station API calls: the API answers bursts with HTTP 429. RIVM: "a free service from which no rights can be derived"; attribution shown. |
| [NS API portal](https://apiportal.ns.nl/) | Treinstoringen | Disruptions API v3. Free, but needs registration and a subscription key; the NS API terms apply. |
| [Tweede Kamer open data](https://opendata.tweedekamer.nl/) | Politiek vandaag | Official OData API, no key. No explicit licence found on the portal (checked September 2026), attribution shown. |
| [Rijksoverheid open data](https://opendata.rijksoverheid.nl/) | Vandaag | School holidays per region, fetched daily. Public holidays, moon phases (Meeus' algorithm, accurate to minutes) and clock changes are calculated by the app. |
| [Open-Meteo](https://open-meteo.com/) (CAMS) | Hooikoorts | Air Quality API, pollen from the Copernicus Atmosphere Monitoring Service (CC BY 4.0). Levels are indicative thresholds per pollen type (grains/m³, daily maximum), not a medical scale. |
| [Eurostat](https://ec.europa.eu/eurostat) | Economie in cijfers | HICP inflation (`prc_hicp_minr`) and unemployment (`une_rt_m`, seasonally adjusted), JSON-stat API, no key. Reuse allowed with attribution ([Eurostat copyright notice](https://ec.europa.eu/eurostat/about-us/policies/copyright)). Inflation is the European HICP measure, which can differ slightly from CBS's national CPI. |
| [ECB Data Portal](https://data.ecb.europa.eu/) | Economie in cijfers | Deposit facility rate and the EUR/USD reference rate, SDMX API, no key; reuse allowed with attribution. |
| [Open-Meteo](https://open-meteo.com/) | Onweer, Teken en muggen, hemel (bewolking) | Lightning potential and CAPE (ICON-D2) for the thunderstorm risk; temperature, humidity and wind for the tick and mosquito **estimate** (no open source with measurements exists: Tekenradar's activity map needs an account); cloud cover tonight. Same terms as the weather. |
| [NOAA SWPC](https://www.swpc.noaa.gov/) | Hemel: noorderlicht | Planetary Kp index forecast (US government, public domain). Planets, moon and twilight are computed locally (JPL Keplerian elements and Meeus; checked against JPL Horizons). |
| [Jolpica F1](https://github.com/jolpica/jolpica-f1) | Sportagenda: Formule 1 | The open, community-run successor of the Ergast API; no key, fair use. Road cycling, mountain biking, athletics and football have no open calendar API (the UCI, World Athletics, UEFA and FIFA only use internal keys), so they come from `sports.events` in `config.yaml`; the defaults were checked with the organisers (UCI, UEC, KNWU, Atletiekunie, race organisers, FIFA, UEFA) on 29 September 2026. Dates not yet published are listed as `# TODO` in the file. |
| [EUMETSAT](https://view.eumetsat.int/) | Satellietbeeld | The EUMETView WMS (GeoServer), no key: the newest time from the layer's GetCapabilities, then one GetMap image of the Benelux; coastlines and borders (Natural Earth) as a separate transparent PNG, refreshed weekly. Credited as "© EUMETSAT"; check the [EUMETSAT data policy](https://www.eumetsat.int/eumetsat-data-licensing) for your kind of use. |
| [RIVM Nationaal Meetnet Radioactiviteit](https://www.rivm.nl/nationaal-meetnet-radioactiviteit) via [EURDEP](https://remon.jrc.ec.europa.eu/) | Straling | The gamma dose rate (µSv/h, hourly averages) of ~150 Dutch stations, from the open WFS of the German Bundesamt für Strahlenschutz (`imis.bfs.de/ogc/opendata`, layer `eurdep_latestValue`; no key, no fees). Indicative only; official warnings come through NL-Alert and the government. |
| [Open-Meteo](https://open-meteo.com/) | UV, Zonnestroom | `uv_index` (hourly) in the weather request; `global_tilted_irradiance` for the solar estimate (kWh = irradiation × kWp × 0.8 performance ratio; no shade, no snow). |
| [USGS](https://earthquake.usgs.gov/earthquakes/feed/) | Wereldwijd: aardbevingen | GeoJSON summary feeds, public domain (US government); every 30 minutes. |
| [NASA EONET](https://eonet.gsfc.nasa.gov/) | Wereldwijd: natuurrampen | Earth Observatory Natural Event Tracker v3, open, no key; storms with a position in the last 3 days, wildfires from `fire_min_ha`. Each event links to its source (NHC, JTWC, IRWIN, …). |
| [Brandweer Nederland](https://www.brandweer.nl/natuurbrandrisico/) | Natuurbrandrisico | The current phase per safety region. There is **no open API**: the list on the public page is read once an hour (robots.txt allows it). If the page layout changes, the block shows that the site is unreachable until this is adapted. |
| [Rijkswaterstaat](https://waterberichtgeving.rws.nl/owb/) | Hoogwater en stormvloed | The water-safety codes of the Watermanagementcentrum Nederland per sector and the status of the storm-surge barriers. There is **no documented API**: these are the public JSON and HTML files that waterberichtgeving.rws.nl itself loads, read every 10 minutes. If their format changes, the block says Rijkswaterstaat is unreachable rather than showing a guess. |
| [NOAA SWPC](https://www.swpc.noaa.gov/) | Ruimteweer | The NOAA space-weather scales and the GOES X-ray flares (public domain), read every 30 minutes. |
| [Launch Library 2](https://thespacedevs.com/llapi) (The Space Devs) | Hemel: raketlanceringen | Open API, free tier 15 requests per hour per IP; the server asks once an hour. |
| [Wikipedia](https://nl.wikipedia.org/) | Trending: uitleg | REST API (`/api/rest_v1/page/summary`, `/w/rest.php/v1/search/page`), no key. Only for current trending terms; disambiguation pages are skipped, and a search hit is used only if its title holds every word of the term plus at most one more. Text CC BY-SA 4.0, credited in the card. |
| [Burgernet](https://www.burgernet.nl/amberalert) (police) | AMBER Alert, Vermist Kind Alert | The open API "Landactiehost" (`services.burgernet.nl/landactiehost/api/v1/alerts`, JSON, no key), documented in *Technische koppelingen Burgernet/AMBER Alert berichten* v1.1. AlertLevel 10 = AMBER Alert (national), 5 = Vermist Kind Alert (a circle); a Cancel closes the alert. The photo is shown via this server's image proxy. |
| [NL-Alert](https://actueel.nl-alert.nl/) | NL-Alert | The public JSON API behind actueel.nl-alert.nl (`api.public-warning.app`), no key. Alerts include their broadcast areas; "in jouw omgeving" is a point-in-polygon check on the server with the visitor's weather location. |
| [UnitedConsumers](https://www.unitedconsumers.com/tanken/brandstofprijzen) | Brandstofprijzen | The daily *gemiddelde landelijke adviesprijs* (GLA). There is **no open API**: the price table is read from the public page once every 3 hours. UnitedConsumers claims copyright on the data on its site, so this is for **personal use only**; turn it off with `fuel.enabled: false` for public or commercial use. CBS publishes official daily pump prices (table 80416ned), but its OData hosts are not reachable from every network. |
| Waste collection providers | Afvalkalender | The provider list and request formats follow the Home Assistant integration [afvalwijzer](https://github.com/xirixiz/homeassistant-afvalwijzer) by xirixiz (MIT licence), rewritten in Go and checked with real addresses (September 2026). A visitor's municipality comes from [PDOK](https://www.pdok.nl/) (Locatieserver, open, no key); then that municipality's calendar and all regional providers are asked in parallel once, and the provider that knows the address is remembered. **Public APIs without a key (on):** 16 municipal calendars with the "opzet" API (Den Haag, Alphen aan den Rijn, Purmerend, Haarlem/Spaarnelanden, …), the regional opzet calendars of HVC, GAD, DAR, Cyclus, Afvalstoffendienst, Offalkalinder, PreZero, Saver and ZRD, 14 Ximmio companies (Almere, Twente Milieu, Avalex, ACV, Avri, Blink, Meerlanden, RAD, Waardlanden, Area, Venlo, Woerden, Hellendoorn, Oostzaan), Amsterdam (open data; dates computed from weekdays and frequency), RD4, ROVA, Irado, Reinis, RWM, Kliko (Maassluis, Oude IJsselstreek), Straatbeeld (Drimmelen) and the iCal calendars of Borsele, Goes and Edam-Volendam. **App providers (`waste.app_providers`, off by default):** Mijn Afvalwijzer (a large share of municipalities, e.g. Utrecht, Eindhoven, Breda) with the key of its web app, Burgerportaal (Groningen, Tilburg, Assen, BAR, Nijkerk, RMN) with an anonymous Firebase session, Omrin with the app's guest login, and Circulus with a web session. These are not public APIs; switch them on at your own discretion. Not included: providers that did not answer for any tested address (Westland, Afval3xbeter, Mijn Afvalzaken, De Afval App), Montferland (plain HTTP only), Mijn Afvalhulp and RecycleApp (Belgium). For the default address also: any iCal link, or your own Home Assistant (REST API with a long-lived token). |
| Push services | Meldingen | Messages go through the browser vendor's push service (Google FCM, Mozilla, Apple, Microsoft). The content is end-to-end encrypted (RFC 8291); the service sees only the timing and size. |
| [Yahoo Finance](https://finance.yahoo.com/) | Beurs | **Unofficial** "spark" endpoint without a key: delayed prices, **personal use only** under Yahoo's terms, and it may change or stop without notice. Free official APIs with European stocks either forbid display (Twelve Data free plan) or cover only the US (EODHD demo); Euronext's own data is encrypted and not used. Turn the panel off with `markets.enabled: false` for public or commercial use. |
| [KNMI](https://www.knmi.nl/nederland-nu/seismologie/aardbevingen) | Aardbevingen | FDSN event service (`rdsa.knmi.nl`), open data; each quake links to its KNMI page. Only earthquakes and induced events; explosions, quarry blasts and sonic booms are left out. |
| [Liander](https://www.liander.nl/storingen-en-onderhoud) | Kritieke infrastructuur | The public ArcGIS feature service `IStoringen_Productie_V7` (Alliander) behind Liander's outage map: status, cause, expected repair time and a customer count per outage. No explicit licence; attribution shown. |
| [Stedin](https://web.stedin.net/storingen) | Kritieke infrastructuur | The JSON behind Stedin's outage page (`/api/storingen/places`, undocumented): one overview request plus one per affected place. Enexis, Rendo, Coteq, Westland Infra and the drinking-water companies publish no open outage data (checked September 2026), so they are not in the panel. |
| [IODA](https://ioda.inetintel.cc.gatech.edu/) (Georgia Tech) | Internetstoringen | Outage events for the country and chosen networks (routing, reachability, traffic). No key. The data is "Copyright Georgia Tech Research Corporation"; no published data licence found (checked September 2026), attribution shown. IODA's server does not answer Go's TLS 1.3 handshake, so the app talks to that one host over TLS 1.2. |
| [ransomware.live](https://www.ransomware.live/) | Ransomware NL | Free API v2: no key, **personal use only**, 1 request per minute per endpoint (polled hourly per country). Business use needs their free PRO key under separate terms. These are claims made by criminal groups, not verified; the panel says so. Descriptions (which can quote stolen data) and links to leak sites are never passed on. |
| [Have I Been Pwned](https://haveibeenpwned.com/) | Datalekken | The public breach list (`/api/v3/breaches`): no API key, and no visitor data is sent. Licensed **CC BY 4.0** (attribution shown in the panel). Fetched every 3 h. Left out: unverified, fabricated, retired, spam lists, malware and stealer logs, entries without a domain, and (unless `include_sensitive: true`) sensitive breaches. HIBP has no country field, so "Dutch" means a `.nl` domain or a description mentioning Dutch/the Netherlands. |
| [Fraudehelpdesk](https://www.fraudehelpdesk.nl/actueel/) | Datalekken: oplichting en phishing | The public RSS feed of warnings (`/feed/?post_type=alert`), read every 2 hours. |
| [Wikipedia (nl)](https://nl.wikipedia.org/) | Vandaag: op deze dag | The wikitext of the day page (e.g. "3 oktober", section Gebeurtenissen) via the standard API, read hourly; CC BY-SA. Dutch Wikipedia has no "on this day" feed. |
| [Rijkswaterstaat water data](https://rijkswaterstaatdata.nl/waterdata/) | Weer: getij | The astronomical tide (high and low water, cm relative to NAP) of 19 coastal stations, via the open DDAPI 2.0 (`OphalenWaarnemingen`); cached per station for 6 hours. |
| [Open-Meteo Marine](https://open-meteo.com/en/docs/marine-weather-api) | Weer: zee | Sea surface temperature and wave height at the station; cached per station for an hour. |
| [Zwaailicht.nl](https://zwaailicht.nl/blog/rss-feeds-p2000-meldingen) | P2000 alerts | Public Atom feeds per city (`/feed/meldingen/<city>.xml`), refreshed every minute. House numbers are left out by Zwaailicht. Fetched only for cities visitors actually choose, and cached 2 min per city. **Not for emergencies: call 112.** |

**Grid operators (Stedin, Enexis, Liander)** publish outages only as web pages or through internal app APIs, not as open data (checked September 2026). They are therefore not included; see the `# TODO` in `config.yaml`.

**Commercial use:** Open-Meteo, Buienradar, SANS ISC and ip-api all restrict it. Check their terms. For geolocation, the alternative is an offline GeoLite2/DB-IP Lite database, which adds a data file and a monthly update.

**Overview and kiosk mode.** Each visitor chooses a mode under Instellingen → Weergave → Modus; it is stored in their browser:

- **Normaal:** the news stream with the panels.
- **Overzicht** (key `v`): "Vandaag in het kort" puts weather, the most widely covered news of the last 24 h, traffic, trains, energy, air quality, security (high NCSC advisories, new breaches), politics and outages on one screen. A card title opens the full panel. Hidden panels are left out.
- **Kiosk:** for a wall display. Larger text, no settings or search. News and panels scroll by themselves every 15 s, pausing for a minute after any touch or key. The screen is kept awake where the browser allows it (Wake Lock). Leave with Esc or the small button in the corner.

A URL sets the mode for that visit only, without changing the saved choice: `https://nieuws.example.nl/?mode=kiosk` for a wall display, `?mode=digest` for the overview.

---

## Privacy and disk writes

- **Nothing is written to disk** at runtime by default. Caches are in memory, and logs go to stdout/stderr (journald or `docker logs`).
  - Checked with `strace` during a 5-minute run: no file was opened for writing.
  - Checked with `docker diff`: the running container's filesystem stays unchanged.
- **Optional warm-start snapshot:** set `cache.snapshot_path` (e.g. `/var/lib/nieuwsdashboard/cache.json.gz` and uncomment `StateDirectory=` in the unit). The server then writes one gzip JSON file at most every 30 minutes and on shutdown, and loads it at startup. Next to it, `<snapshot>.push.json` keeps the push subscriptions and `<snapshot>.icons.json` the site icons. These are the only code paths that write to disk.
- **Icons only:** to have the site icons right after a restart without writing the news to disk, set only `cache.icon_cache_path` (e.g. `/var/lib/nieuwsdashboard/icons.json`): one ~85 KB file, written only when the icons change (about weekly), against ~0.5 MB every 30 minutes for the news snapshot.
- **With Docker:** uncomment `NDB_ICON_CACHE_PATH` and/or `NDB_SNAPSHOT_PATH` and the `./data` volume in `docker-compose.yml`, after `mkdir -p data && sudo chown 65532:65532 data` (the container runs as uid 65532). If a folder is not writable, the log says so at startup.
- **No cookies, no trackers, no external fonts or scripts.**
  - The Content-Security-Policy only allows the page's own origin, plus `https:` images when `show_images` is on.
  - Feed titles and summaries are stripped of all HTML on the server and rendered as text in the browser.
  - Links are limited to `http(s)` and open with `rel="noopener noreferrer"`.
- **Source icons** are fetched by the server from the news sites' own pages (never by the browser), kept in memory and refreshed weekly; SVG icons are skipped because they can contain scripts, and the fetches refuse private network addresses like the image proxy. For sites that block the server, it asks DuckDuckGo's and then Google's favicon service, sending only the site's name (e.g. `www.nu.nl`); turn that off with `features.icon_services: false`.
- **"Gebruik mijn locatie"** rounds coordinates to 2 decimals (~1 km) in the browser, and sends them only to this server.
- **Afvalkalender address:** kept in the browser. The server uses it only to ask the municipal calendars, keeps the result up to 6 hours in memory, and never logs it.
- **Push notifications** are opt-in per device. The server keeps each subscription (the push-service URL and two keys, the chosen topics, the language, the weather location rounded to ~1 km and, for the waste reminder, the address) in memory; with `cache.snapshot_path` set also in `<snapshot>.push.json` (mode 0600). Turning notifications off removes it.
- **Read state, "Bewaard" (including notes and labels), watchlist and mute words** live in `localStorage`. The service worker keeps the last good responses in the browser's cache for offline use; the server stores nothing per user.
- **Thumbnails** are off per visitor by default (*Instellingen → Weergave*). When on, they come from `/api/img`, so publishers never see the visitor. The proxy:
  - only fetches URLs this server signed itself (HMAC with a random key per process), so it is not an open proxy
  - checks every connection *after DNS resolution* and on each redirect, and refuses private, loopback, link-local (incl. `169.254.169.254`), CGNAT and other special-purpose ranges (IPv4 and IPv6)
  - accepts `https` only, at most 3 redirects, 5 MB, 40 megapixels
  - turns JPEG/PNG/GIF into 320 px JPEG thumbnails. WebP is passed through up to 250 KB, because the standard library cannot decode it.
  - keeps results in an in-memory LRU of at most 50 MB, rate-limited per client IP

---

## Security

- **Browser:**
  - The Content-Security-Policy allows only the dashboard's own origin. Its two inline scripts are allowed by **SHA-256 hash**, computed from the embedded page at startup, not by `'unsafe-inline'`.
  - It also sends `object-src 'none'`, `frame-ancestors 'none'` and `base-uri 'none'`, plus `nosniff`, `Referrer-Policy: no-referrer`, a restrictive `Permissions-Policy`, `X-Frame-Options: DENY` and `Cross-Origin-Opener/Resource-Policy`.
  - The page never uses `innerHTML`; all feed data is inserted as text.
- **Feed content:**
  - All HTML is stripped on the server, including double-escaped markup and Unicode bidi-override characters.
  - Links must be `http(s)` without credentials; titles and summaries are capped at 300 characters.
  - Responses are capped at 5 MB, 10 s and 3 redirects.
  - XML entities are never expanded, and absurdly nested XML is skipped.
- **Only GET, except push:** every route answers only GET/HEAD, except `POST /api/push/{subscribe,unsubscribe,test}`. Those accept only same-origin requests (`Sec-Fetch-Site: same-origin`, or a matching `Origin`), JSON bodies of at most 4 KB with no unknown fields, and are rate-limited per IP. Subscriptions are accepted only for the push services of the major browsers (FCM, Mozilla, Apple, Microsoft) over https on the default port, so the server never posts to an arbitrary URL.
- **Outbound requests from user input:** only the image proxy fetches addresses that originate outside the configuration. It accepts only URLs this server signed, and checks every connection's IP after DNS resolution (so also redirects and DNS rebinding) against private and special-purpose ranges. See *Privacy*.
- **Abuse limits:**
  - Weather, geocoding and image requests that cause upstream traffic are rate-limited per client IP.
  - Every cache has a fixed maximum (news per source, 500 weather locations, 1000 geocoding queries, 10 000 IP lookups, 50 MB of images).
  - The rate limiter itself is bounded and prunes at most once a minute.
- **Tested:** `main_test.go` feeds a hostile RSS fixture through the real fetcher and `/api/news`. It also checks:
  - security headers on every route
  - that the CSP hashes match the page
  - gzip/ETag behaviour
  - redirects from an allowed host to an internal address, which must be refused before connecting
  - the rate-limiter bounds

  `govulncheck` reports no known vulnerabilities. The Docker build only produces an image when `go vet` and all tests pass.

## Monitoring

- **`/healthz`** returns `200` with the status of every source (news, threat intelligence, advisories). Use it for Docker's `HEALTHCHECK`, Uptime Kuma or any HTTP check.
- **Bronstatus** (footer link in the dashboard) shows the same information for people: per source *ok · bijgewerkt 3 min geleden* or *niet bereikbaar sinds 10:42: HTTP 403*, with a filter for problems only.
- **`/metrics`** (off by default; `server.metrics: true` or `NDB_METRICS=true`) serves Prometheus text format:
  - `ndb_source_up`, `ndb_source_last_success_timestamp_seconds`, `ndb_source_items`, `ndb_source_consecutive_errors`
  - `ndb_http_requests_total{route,code}`
  - the image and geolocation cache sizes
  - `go_goroutines` and heap size

  Behind the ISPConfig vhost, restrict it (e.g. `location /metrics { allow 10.0.0.0/8; deny all; proxy_pass …; }`) if it should not be public.

## HTTP API

All JSON responses:
- are gzipped when the client accepts it
- carry a weak `ETag` and answer `304` to a matching `If-None-Match`
- use `Cache-Control: max-age=60`, except `/healthz`, which is `no-store`

| Endpoint | Returns |
|---|---|
| `GET /` | the dashboard |
| `GET /api/catalog` | categories, sources (without feed URLs), features, advisory sources |
| `GET /api/news?sources=a,b&limit=60&since=&group=&per_source=` | merged, de-duplicated, date-sorted items with `related` (same story elsewhere) + per-source status. `since`: RFC 3339 or unix seconds; `group=0` disables story grouping; `per_source` (max. 20) adds older articles so every source has at least that many |
| `GET /api/img?u=&s=` | thumbnail through the image proxy (only URLs signed by this server) |
| `GET /manifest.webmanifest`, `/icon-*.png`, `/sw.js` | installable web app: manifest, icons (drawn at startup) and offline service worker |
| `GET /api/weather?lat=&lon=&region=&cc=` | current, 24 h, 7 days, rain 2 h, warnings (defaults to the configured location) |
| `GET /api/sea?lat=&lon=` | Zee en getij: the nearest coastal `station` (`name`, `km`), the next `tides` (`time`, `high`, `cm`) and `sea` (`temp`, `wave`, `wave_dir`, `wave_max`) |
| `GET /api/geocode?q=` | place search, NL/BE first (rate-limited per IP) |
| `GET /api/threats` | Infocon, top ports, 30-day trend, top IPs + countries, Feodo C2, optional KEV, `urlhaus` (NL: `online`, `week`, `threats`, `asns`, `newest` defanged), `threatfox` (`total`, `families`, `newest` defanged; `missing_key`), sources/licences |
| `GET /api/exploits` | Tab Exploits: `exploitdb` (`items`: `id`, `title`, `kind`, `url`, `published`) and `epss` (`date`, `since`, `risers`: `cve`, `epss`, `prev`, `percentile`; `kev`) |
| `GET /api/nlthreat` | Dreigingsbeeld NL: `radar` (`data`: `trend`, `days`, `vectors`, `origins`, `hijacks`, `leaks`, `hijacks_n`, `leaks_n`, `as_names`; or `missing_key`) |
| `GET /api/alerts` | top bar: NCTV level (`level`, `name`, `since`) KNMI summary (`level`, `active`, `onset`, `types`, `areas`, `heat`) and `water` (`level`, `sectors`, `peak`, `peak_at`, `barriers`, `outlook`) |
| `GET /api/traffic` | jams (road, direction, from/to, delay), accidents, closure count, VILD version |
| `GET /api/alarms?city=` | P2000 alerts for a city slug (default from config): per service at most 2, with urgency, units and detail; Lifeliner falls back to national when the city has none |
| `GET /api/energy` | Energieprijzen: hourly `electricity` (today, tomorrow from ~13:00) and `gas` prices in € incl. VAT (+ configured extras) |
| `GET /api/air?lat=&lon=` | Luchtkwaliteit: nearest station (`name`, `distance_km`, `url`), `lki` (`value` 1–11, `at`) and `components` (NO2, PM25, PM10, O3 in µg/m³) |
| `GET /api/trains` | Treinstoringen: `key` (false without an NS key), `calamities`, `disruptions`, `maintenance` (active now, max 5) and `maintenance_total` |
| `GET /api/politics` | Politiek vandaag: `day`, `activities` (time, kind, subject, committee, cancelled, url) and the latest `votes` (result, kind, subject, date, url) |
| `GET /api/pollen?lat=&lon=` | Hooikoorts: `days` (3 × daily maximum per pollen type, grains/m³, and `o3`: the highest ozone value, µg/m³) and `now` |
| `GET /api/utilities` | Kritieke infrastructuur: per grid operator the `active` and `planned` outages (energy, place, status, reported, estimate, customers) and `resolved_24h` |
| `GET /api/economy` | Economie in cijfers: `inflation` and `unemployment` (13 months), `inflation_ea`, `rate` with `rate_since`, `eurusd` (last 2 days) |
| `GET /api/nlalert?lat=&lon=` | NL-Alert: `alerts` of the last 14 days (text, English text, start, stop, withdrawn, `near` for the given point or the configured weather location) and the number `active` |
| `GET /api/fuel` | Brandstofprijzen: `date` and `prices` (fuel, name, price per litre, change in cents) |
| `GET /api/waste?postcode=&number=&suffix=&provider=` | Afvalkalender for the given address (`own`, `pickups`, `provider`, `calendar`, `home`, or `not_found`); `provider` is optional (default: find automatically). Without parameters the server's default address (`needs_address` when there is none). The address is not echoed. The enabled providers are in `/api/catalog` (`waste_providers`) |
| `GET /api/amber?lat=&lon=` | AMBER Alert and Vermist Kind Alert: the active `alerts` (title, text, kind, url, photo via `api/img`, area) with `near` for the given point or the weather location |
| `GET /api/insects?lat=&lon=` | Teken en muggen: 3 `days` with `ticks` and `mosquito` levels 0–3 (an estimate) |
| `GET /api/sky?lat=&lon=` | Vanavond aan de hemel: sunset, dark, dawn, `moon` (phase, rise, set), `planets` (from, until, best time, altitude, direction), `kp` and `aurora` (0–3), `clouds`, `meteor`, `space` (`days`: the R, S and G scales with forecast probabilities; `flare`) |
| `GET /api/sports` | Sportagenda: `f1` (next race with sessions, last podium, standings) and `events` (per sport the current or just-finished events and the next three, with `note`, `tentative` and `headlines`) |
| `GET /api/world` | Aardbevingen en natuurrampen (Wereld and natuurbrandrisico): `fire_risk.regions` (region, phase; 0 = unknown), `quakes.items` (mag, place, time, depth_km, tsunami, alert, url) and `events.items` (kind storm/volcano/flood/landslide/wildfire, title, time, wind_kmh, area_ha, url) |
| `GET /api/icon?s=` | A news site's icon (32×32 PNG) for a configured source; the catalog lists the link per source (`icon`) once the icon is there |
| `GET /api/trending` | Trending: up to 8 `terms` with the number of `sources` in the last 3 hours |
| `GET /api/wiki?term=` | Wikipedia summary for a current trending term: `summary` (`found`, `title`, `description`, `extract`, `url`, `thumb` via `api/img`) and a `search` link; 404 for other terms |
| `GET /api/radiation?lat=&lon=` | Straling: `nearest` station (name, value µSv/h, time), `km`, national `min`/`median`/`max`, `raised`, `above`, `level` |
| `GET /api/solar?lat=&lon=&tilt=&az=` | Zonnestroom: two `days` with `kwh_per_kw` (multiply by your kWp) and the sunniest 3 hours (`best_from`, `best_to`) |
| `GET /api/satellite` | Satellietbeeld: `time` of the image, `image` and `overlay` (URLs on this server that change with every new image) |
| `GET /api/satellite/image` · `/overlay` | The latest satellite image (JPEG) and the coastline overlay (PNG) |
| `GET /api/push` | Push: `enabled`, the VAPID public `key` and the available `topics` |
| `POST /api/push/subscribe` · `/unsubscribe` · `/test` | Register, remove or test this device's subscription (same-origin JSON only) |
| `GET /api/markets` | Beurs: `indices` in config order and `stocks` sorted by daily change (symbol, name, price, change_pct, prev_close, time) |
| `GET /api/quakes` | Aardbevingen: the quakes of the last `days` (time, place, magnitude, depth, induced, KNMI link) |
| `GET /api/today` | Vandaag: `date`, `week`, `holidays_today`, `holidays_next`, `moon` (`phase`, `illumination`, `next_full`, `next_new`, `moment`), `clock_change`, `school.regions` (noord/midden/zuid: current or next holiday), `on_this_day` (`title`, `url`, `events`: `year`, `text`) |
| `GET /api/ransomware` | Ransomware NL: `last7` / `last30` / `last365` counts, `top_groups` (90 days), the 8 newest `victims` (name, website, sector, group, date) and per-country `sources` |
| `GET /api/breaches` | Datalekken: the latest 3 Dutch (`nl`) and 3 other (`other`) breaches with `title`, `domain`, `url`, `breach_date`, `added`, `count`, `data_classes`, plus `total`/`shown`, `phishing` (`items`: `title`, `url`, `published`) |
| `GET /api/outages` | per provider: status (`ok`/`minor`/`major`) and incidents; `internet`: IODA events per country/network |
| `GET /api/advisories?sources=&limit=` | normalised advisories: `{id, source, title, url, published, updated, severity, probability, impact, cves, products, exploited}`; `groups` (source id → `edge`); the limit applies per tab |
| `GET /healthz` | `{"status":"ok", …}` + per-source status for news (`sources`) and threat/advisory feeds (`feeds`) |
| `GET /metrics` | Prometheus metrics (only when enabled) |

---

## Development

```sh
go vet ./... && go test ./...        # unit tests: parsing, dedup, story grouping, NCSC, weather, ip-api batching,
                                     # image proxy + SSRF guard, PWA assets, config, …
cp config.yaml.default config.yaml   # once
go run . -config config.yaml         # http://127.0.0.1:8080
go run . -check-feeds                # verify all feeds
```

**Browser tests** (`tests/e2e`): 29 Playwright suites with almost 1,000 checks of the real page in Chromium: layout on desktop and phone, light and dark, Dutch and English, accessibility (axe), keyboard use, the top bar, every panel, push settings, and hostile feeds (XSS, bad links). `run.sh` builds the binary, writes test configurations derived from `config.yaml.default`, starts five servers (default, mock KNMI warnings, hostile feed, push on, accent colour) and runs the suites; a failing suite is retried once, because the servers fetch the real feeds. GitHub Actions runs them on every push to `main` and on pull requests (workflow *E2E*).

```sh
cd tests/e2e && npm ci && npx playwright install --with-deps chromium
./run.sh                 # all suites (about 10 minutes)
./run.sh v119 swipe      # only these
```

**Repository layout:**
- `main.go`: config, HTTP server, API
- `feeds.go`: fetcher, scheduler, feed parser, news cache, `-check-feeds`
- `panels.go`: weather, threats, advisories
- `web/index.html`: the entire frontend, embedded with `go:embed`. It uses no framework, no build step and no CDN.
- `tests/e2e`: the browser tests (see above).

**Dependency:** `gopkg.in/yaml.v3` is the only one.

---

**Releasing a new version:**
1. Set the version in `VERSION` (e.g. `1.7.3`).
2. Add a `### 1.7.3` entry at the top of the changelog below; it becomes the release notes. Preview them with `sh .github/release-notes.sh 1.7.3`.
3. Commit and push, then tag and push the tag: `git tag -a v1.7.3 -m "Nieuws Hub 1.7.3" && git push origin v1.7.3`.

GitHub Actions then publishes the image (`:1.7.3`, `:1.7`, `:1`, `:latest`), builds the Linux downloads and `SHA256SUMS`, and creates the GitHub release. It stops before publishing anything if the tag doesn't match `VERSION`, the changelog entry is missing, or a test fails.

## Possible extensions

These fit the architecture as extra scheduled jobs, but need a key, an account or a custom parser. So they are not in `config.yaml`:

- abuse.ch **URLhaus** and **ThreatFox** (free Auth-Key; the same key as Feodo)
- **Cloudflare Radar** attack trends per country (free API token)
- **GreyNoise** community / **AbuseIPDB** reputation for the top-IP list (free key)
- **Shadowserver** NL exposure statistics (account)
- **ransomware.live** victims filtered to NL/BE, **Spamhaus DROP** netblock counts, the **Tor exit list** (keyless; check the terms)
- **ENISA EUVD**, **NVD CVE API 2.0**, **FIRST EPSS** to enrich advisories with exploit probability
- **Custom feeds from the UI** (`features.allow_custom_feeds`): the flag is reserved but not implemented yet. The SSRF-safe dialer of the image proxy (see *Privacy*) is the building block for it.

Feeds that were tried and are currently broken are listed in `config.yaml` with `enabled: false` and a `# TODO` explaining why.

---

## Changelog

### 1.31.3
- **Dreigingsbeeld NL:** the incident list only shows incidents at Dutch organisations: the headline or summary must name the Netherlands, a Dutch place or province, a type of Dutch organisation (gemeente, waterschap, ziekenhuis, …) or a well-known Dutch company or authority; headlines from domestic sources (regional broadcasters, NOS Binnenland) also count unless they name another country.

### 1.31.2
- **Security-adviezen:** the tab row (Adviezen · Edge-apparaten · Exploits) had the CSS class `advt`, which EasyList hides on every site with the generic rule `##.advt`; with uBlock Origin, AdBlock Plus or AdGuard the tabs were invisible (seen in Firefox on Windows and Android). Renamed to `sectabs`. The e2e suite now checks every class and id on the rendered page against the current EasyList and EasyList Dutch.

### 1.31.1
- **Edge-apparaten:** Ivanti replaced by **OPNsense** (new advisory format `ghsa`: the GitHub security advisories of a repository, no key). Fortinet advisories now start with the product name ("FortiClient Windows: …"), because Fortinet's own titles only name the weakness.
- **Refresh after an update:** the page carries its build ID (`<meta name="ndb-page">`) and every API answer the server's (`X-NDB-Page`). When they differ, a page that was just opened clears the service worker and its caches and reloads once; a page that has been open for a while shows "Er is een nieuwe versie · Vernieuwen". API requests send the build ID in the header `X-NDB-Page` and answers carry `Vary: X-NDB-Page`, so cached answers of one build are never used by another.

### 1.31.0
- **Security-adviezen:** tabs **Adviezen**, **Edge-apparaten** (new advisory sources `fortinet`, `paloalto`, `cisco`, `ivanti` with `group: edge`; severity read from the feed: Cisco Security Impact Rating, Palo Alto Severity, Fortinet CVSS score) and **Exploits** (Exploit-DB and EPSS risers; new endpoint `/api/exploits`).
- **Cyberdreigingen:** tabs **Malware in NL** (URLhaus country feed) and **IOC's** (ThreatFox, needs the abuse.ch key). Defanged, never links.
- **New panel Dreigingsbeeld NL** (`/api/nlthreat`): incidents from your Dutch news, DDoS attacks on NL and BGP hijacks and leaks (Cloudflare Radar, `CLOUDFLARE_RADAR_TOKEN`).
- **News:** new source Hacker News (front page, 100+ points), category Tech, in the Tech & security preset.
- The fetcher accepts a larger response for known large files (`MaxBody`).

### 1.30.0
- **Gezondheid:** Hooikoorts and Teken en muggen are one panel with two sections. Saved layouts are migrated (whoever kept Teken en muggen visible keeps the combined panel visible).
- **Renamed:** Gezondheid (RIVM alerts) → **RIVM**, Autoriteit Persoonsgegevens acties → **Autoriteit Persoonsgegevens**, Ransomware NL → **Ransomware**.

### 1.29.0
- **Zee en getij** (Weer): the next high and low tides at the nearest of 19 coastal stations (Rijkswaterstaat), with the sea temperature and waves (Open-Meteo Marine). New endpoint `/api/sea`.
- **Op deze dag** (Vandaag): four events on today's date from the Dutch Wikipedia day page, Dutch events first.
- **Oplichting en phishing** (Datalekken): the latest Fraudehelpdesk warnings of the last 30 days.
- Config: `weather.sea`, `weather.tides_url`, `weather.marine_url`, `today.on_this_day`, `today.wiki_url`, `breaches.phishing`, `breaches.phishing_url` (all on by default).

### 1.28.1
- **Beurs** is on the Dagelijks page by default on a phone (a visitor's own choice still applies).

### 1.28.0
- **Two panel pages on a phone:** Overzicht ← Nieuws → **Dagelijks** → **Achtergrond**. By default Achtergrond holds Economie, Politiek, Cyberdreigingen, Security-adviezen, Datalekken, Ransomware, Kritieke infrastructuur, Internetstoringen, AP and Gezondheid; under *Instellingen → Panelen* each panel has a Dagelijks/Achtergrond choice (phone only; desktop and tablet are unchanged). *Ga naar paneel* switches to the right page.

### 1.27.1
- **Internetstoringen:** the Storingen panel is renamed.
- **Swiping on a phone** is more forgiving: the first 14 px of movement decide the direction (so the arc of a thumb no longer spoils it), 45 px sideways or a quick 25 px flick is enough, slow swipes up to 1.5 s count, and the page claims sideways panning (`touch-action: pan-y`) so the browser's own gestures no longer interfere. Previously a swipe had to be 60 px, twice as wide as tall at the end, and under 0.8 s.

### 1.27.0
- **Panel bar:** above the panels, sticky while scrolling: **Ga naar paneel** (a searchable A–Z list of your panels; also the `g` key) and **Alles inklappen / uitklappen**.
- **Summary lines:** a collapsed panel shows one line with its current state (e.g. "14° bewolkt", "nu € 0,24/kWh · goedkoopst 13–16 u", "2 storingen"), or how fresh its data is.
- **Tablet:** a **Panelen ↓** button in the news bar, because the panels are below the news there.

### 1.26.0
- **Hoogwater en stormvloed** (Aardbevingen en natuurrampen, Nederland tab): Rijkswaterstaat's water-safety code per sector, the highest code expected in the next 24 hours, the four storm-surge barriers and the short coast outlook. A **top-bar badge** and the new **push topic "water"** at code orange or red, or when a barrier closes.
- **Hitte en smog** (Luchtkwaliteit): the Nationaal Hitteplan (active while KNMI has a heat warning), RIVM smog warnings and the ozone forecast for 3 days.
- **Ruimteweer** (Vanavond aan de hemel): the NOAA scales R, S and G now and for 3 days, and the strongest solar flare of the last 24 hours.
- Config: `world.water`, `world.water_url`, `air.heat_smog`, `sky.space_weather`, `sky.space_weather_url` (all on by default).

### 1.25.0
- **Shorter history:** Aardbevingen (Nederland tab), NL-Alert, Autoriteit Persoonsgegevens acties and Gezondheid show the last 14 days instead of 31. KNMI is asked for 14 days by default (`quakes.days`).
- **Sportagenda:** finished events stay listed for 14 days instead of 10.
- **Afvalkalender:** refreshed once a day (`waste.interval` default 24h, which now also applies to visitor addresses).

### 1.24.0
- **Aardbevingen en natuurrampen:** the Aardbevingen and Wereldwijd panels are now one panel with two tabs. **Nederland**: earthquakes (KNMI, 31 days), the KNMI wind warning when there is one, and **natuurbrandrisico** per safety region (new, from brandweer.nl: the regions in phase 2, otherwise "overal fase 1"). **Wereld**: the former Wereldwijd content. A summary line above the tabs ("Nederland: rustig · Wereld: 3 stormen"); the chosen tab is remembered. Visitors' panel order, collapsed state and visibility carry over.
- Config: `world.fire_risk` and `world.fire_risk_url`.

### 1.23.3
- **Weer:** the UV line only shows the value, the WHO category and the time; the sun-protection advice is gone.
- **Satellietbeeld:** the caption only says when the image was taken (and credits EUMETSAT).

### 1.23.2
- **Raketlanceringen** only shows launches in the coming 24 hours (and a flight under way); without any, one short line says so (`sky.launches_hours`).
- **Wereldwijd** shows the last 24 hours: setting `world.hours` (1–168, default 24) replaces `world.days`, which still works (days × 24).

### 1.23.1
- **Wereldwijd** shows the last 3 days instead of 7 for earthquakes, and also for volcanoes, floods and wildfires (by their latest update). Change it with `world.days` (1–7).

### 1.23.0
- **Raketlanceringen** in Vanavond aan de hemel: the next three launches with countdown, rocket, mission, organisation and launch site, "nu in de lucht" during a flight (Launch Library 2, open).
- **New panel Wereldwijd** (after Aardbevingen): big earthquakes worldwide from USGS (M6+, last 7 days, with tsunami warning and USGS PAGER impact), and active natural disasters from NASA EONET: hurricanes and typhoons with wind speed, volcanic eruptions, floods and large wildfires. Storm names and distances in Dutch ("Orkaan Rachel", "80 km ONO van …").
- New config section `world`, `sky.launches` / `sky.launches_url`, refresh key `world`.

### 1.22.0
- **Site icons survive a restart:** set `cache.icon_cache_path` (or `NDB_ICON_CACHE_PATH`) to keep only the icons on disk (~85 KB, written about weekly), without writing the news; with `cache.snapshot_path` the icons go to `<snapshot>.icons.json`. They load at startup, so they are visible immediately instead of after 1–2 minutes, and the weekly refresh continues from the stored fetch times. With Docker, uncomment the new optional warm-start lines and the `./data` volume in `docker-compose.yml`.
- **Startup check:** when the folder for the snapshot or the icon cache is not writable (in Docker: not owned by uid 65532), the log says so at startup with the `chown` fix, instead of failing quietly at the first write.
- Browser tests: the Internationaal chip check waits for its article count (it failed on slower test machines).

### 1.21.1
- **Fix: a category with slow sources looked empty with many sources on.** The page loads the 300 newest articles; with all ~85 sources chosen, those cover only the last hours, so investigative outlets (a few pieces a week) never made it in and "Onderzoek" showed no articles. Every chosen source now also keeps its 10 newest articles (`per_source=10`), so its category and chip count stay filled; with the default sources this changes almost nothing.

### 1.21.0
- **Source icons:** the news sites' own small icons instead of coloured dots, in the news list, the coverage view and the source settings. The server picks the sharpest icon a site offers (apple-touch-icon, a declared PNG icon, then `/favicon.ico`, including classic ICO files), scales it to a 32×32 PNG, serves it itself and refreshes it weekly. Icons from a cookie-consent page on another domain are ignored. Sites that show the server only a cookie wall or block it (NU.nl, RTL Nieuws, De Telegraaf, De Tijd) get their icon from DuckDuckGo's or Google's favicon service (`features.icon_services`, only the site's name is sent). In dark mode icons sit on a small light tile. All 65 sites have an icon. Turn it off with `features.source_icons: false`.
- **Data saver** (Instellingen → Weergave: Automatisch / Aan / Uit): refresh 3× less often and no images (thumbnails, source icons; the satellite image on request). Automatic follows the browser's data-saver setting, a 2G connection, or a battery below 20 % that is not charging; a note "databesparing" appears next to the news time.
- RTV Oost's homepage is now `oost.nl` (the old address redirects over plain http).

### 1.20.0
- **De Correspondent is back** as a source (category Onderzoek, € label): it publishes a feed again at `decorrespondent.nl/feed/v1/publications`. It is also part of the "Onderzoek" preset. Pointer (only a podcast feed) and Techzine (blocks every request) stay off; the notes in `config.yaml` are updated.
- **Readable countdowns:** "over 6 maanden" and "over 3,5 jaar" instead of "over 172 dagen" or "over 1348 dagen" (Sportagenda and Vandaag); the exact number of days shows on hover.
- **Browser tests in the repository:** the 27 Playwright suites (almost 900 checks) now live in `tests/e2e` with a runner that starts its own test servers, and GitHub Actions runs them on every push to `main` (workflow *E2E*).

### 1.19.1
- **Sportagenda:** an icon in front of each sport (🏎️ Formule 1, 🚴 Wielrennen, 🚵 Mountainbike, 🏃 Atletiek, ⚽ Voetbal), and "Voetbal (EK/WK)" is now simply "Voetbal".

### 1.19.0
- **Trending from your own sources:** the trending words are now computed from the sources you chose, so every chip leads to articles you can see (before, a word from sources you had not switched on could give "no articles found").
- **Sportagenda:** new sports **Wielrennen** (road cycling: the monuments, Amstel Gold Race, the grand tours for men and women, EK and WK) and **Voetbal** (EK and WK only, men and women); mountain biking and athletics get more events (NK atletiek, the Amsterdam and Rotterdam marathons). Per sport the panel shows what is on now and the next three events, with an extra line (e.g. the race days of a WK) and "datum voorlopig" for dates the organiser has not confirmed. Visitors who chose their own sports get the two new ones switched on once.
- Config: new sports `road` and `football`, event fields `note` and `tentative`, and a new default calendar in `sports.events` (checked 29 September 2026; unpublished dates are `# TODO`).

### 1.18.0
- **UV index** in the Weer panel: the highest UV of the next 24 hours with its time, the WHO category and sun-protection advice from UV 3. It replaces the bare UV number in the sunrise line.
- **Straling** in Luchtkwaliteit: the gamma dose rate at the RIVM station nearest the visitor's air place, with the national range (RIVM Nationaal Meetnet Radioactiviteit via EURDEP). When at least 3 stations measure 0.3 µSv/h or more, a notice "Straling: verhoogd" appears in the top bar and push topic `radiation` sends one message per episode.
- **Zonnestroom** in Energieprijzen: the expected yield of your solar panels today and tomorrow and the sunniest 3 hours, from the Open-Meteo forecast. Enter kWp, direction and tilt under Instellingen → Zonnepanelen (stored only in your browser).
- **Top bar:** NL-Alert only appears while an alert is active, and KNMI only while there is a weather warning; "NL-Alert: geen" and "KNMI: geen waarschuwingen" are no longer shown (the NL-Alert panel still says there are no active alerts). While a source is unreachable it still shows "onbekend".
- New config sections `radiation` and `solar`, refresh keys `radiation` and `solar`, push topic `radiation`.

### 1.17.1
- **Treinstoringen:** "Storingen op het spoor" and "Werkzaamheden" are below each other again. Since 1.16.0 they were shown as two columns, because the new trending-chip wrapper shared its CSS class name with these sections.

### 1.17.0
- **Storingen:** the status of **Google Cloud** (from `status.cloud.google.com/incidents.json`: ongoing incidents with the affected regions, e.g. europe-west4, and those resolved in the last 24 h) and **STACKIT** (its Atlassian Statuspage).
- New outage format `gcp`.

### 1.16.0
- **Satellietbeeld:** a new panel with the latest Meteosat image of the Benelux from EUMETSAT (new every 10 minutes), true colour by day and clouds with city lights at night, with coastlines and borders. The server fetches it and serves it itself.
- **Wikipedia for trending topics:** hover over a trending word (or tap ⓘ) for a short summary from Wikipedia. Only current trending terms can be looked up; answers are cached for 24 hours. Turn it off with `trending.wikipedia.enabled: false`.
- **Search operators:** `bron:nos` / `source:nos`, `"exact words"`, `-word` and `-bron:x`.
- **Paywall label:** a € next to articles from sources marked `paywall: true`; set for de Volkskrant, NRC, Trouw, AD, Het Parool, De Telegraaf, Telegraaf DFT, Nederlands Dagblad, Follow the Money, De Groene Amsterdammer, The Economist, De Tijd and Knack.
- New config section `satellite`, `trending.wikipedia`, the source field `paywall` and the refresh key `satellite`.

### 1.15.0
- **AMBER Alert and Vermist Kind Alert** from Burgernet (the police's open API): while a child is being searched for, a prominent banner above the news and panels with the name and age, the description, the photo (via this server's image proxy), "Heb je informatie? Bel direct 112." and a link to politie.nl. An AMBER Alert (national) is shown to everyone; a Vermist Kind Alert only when the visitor's weather location lies in its circle. The banner can be hidden for the rest of the session. It is also a push topic (on by default for new subscriptions), and it is listed in Bronstatus.
- New config section `amber` and refresh key `amber`.

### 1.14.2
- **Gentler on the sources:** news feeds are fetched every 15 minutes by default instead of 10 (`fetch.default_interval`; about a third fewer requests), and IODA (Storingen: internet) every 30 minutes instead of 15 (`outages.internet.interval`). Your own `config.yaml` keeps its values; change them there if you want the same.
- **Removed:** the panel Kwetsbaarheden in mijn software. An existing `vulns` section, `keys.nvd_api_key` or `refresh.vulns` in `config.yaml` is ignored (with a warning in the log) and can be deleted.
- **Teken en muggen:** the tips about ticks and mosquitoes are gone.

### 1.14.1
- **Afvalkalender:** only the next collection per waste type (for example PMD 5 Oct, Restafval 7 Oct, GFT 12 Oct) instead of the repeating cycle, which showed the same type several times.

### 1.14.0
- **New panel Vanavond aan de hemel** (after Hooikoorts): when it gets dark, the moon (phase, rise and set), the planets visible tonight with the time and direction, the chance of northern lights from NOAA's Kp forecast, tonight's clouds and active meteor showers. Positions are computed locally and checked against NASA/JPL Horizons.
- **New panel Sportagenda** (after Beurs): Formula 1 from the open Jolpica API (next race and qualifying, last podium, standings) and the mountain bike and athletics European/World Championships from a calendar in `config.yaml` (`sports.events`), with matching headlines during and just after a championship. Visitors choose their sports under *Instellingen*; the server offers `sports.sports`.
- **New panel Teken en muggen** (after Hooikoorts): an estimate of tick and mosquito activity for today and the next two days, from temperature, humidity and wind, with links to Tekenradar and the mosquito radar.
- **New panel Kwetsbaarheden in mijn software** (after Security-adviezen): for the products in `vulns.products`, the CVEs of the last 30 days from NVD with their CVSS score, the EPSS chance of exploitation and CISA's actively exploited list. Optional `keys.nvd_api_key`.
- **Weather:** the chance of thunderstorms in the next 24 hours (Open-Meteo lightning potential), also in the overview when there is a chance.
- **Accent colour** for all visitors in `config.yaml` (`ui.accent`), made readable automatically in light and dark mode.
- **Phones:** a third swipe screen with the overview (Overzicht ← Nieuws → Panelen), pull down to refresh the current screen, and a share button on every article (the share sheet, or the link is copied).

### 1.13.0
- **Phones: swipe between news and panels.** On a phone (up to 699 px wide) the page opens on the news; swipe left for all panels from top to bottom, swipe right for the news again. The tabs "Nieuws" and "Panelen" do the same (also with the arrow keys). The top bar (KNMI, Alarmeringen, NL-Alert, Terreurdreiging) and the header are the same in both views and scroll the same way; each view keeps its own scroll position. Swipes from the screen edge (the browser's back gesture) and inside sideways-scrolling parts (the hourly weather) are left alone. Links to a panel (top bar, overview cards, push notifications) open the panels view; searching opens the news view. Tablets and desktops are unchanged.

### 1.12.0
- **NL-Alert in the top bar**, between Alarmeringen and Dreigingsniveau: "NL-Alert: in Leeuwarden" (red) while an alert is active, otherwise "NL-Alert: geen" (green). The place is taken from the first sentence (after the last "in"; after a comma only the last part; longer than 20 characters only the last word). An alert for your own area goes first, "+1" means more active alerts, and the tooltip shows the first sentence of each. Click to go to the NL-Alert panel (it is shown again if you had hidden it).

### 1.11.0
- **Disclaimer** (Dutch and English) at the bottom of the page and of this README: best effort, GPL-3.0, no warranty, no support.
- **Afvalkalender for much more of the Netherlands:** 51 built-in waste collection providers instead of 16, after the Home Assistant integration [afvalwijzer](https://github.com/xirixiz/homeassistant-afvalwijzer) (MIT): municipal calendars, 14 Ximmio companies (Almere, Twente Milieu, Avalex, Blink, …), Amsterdam, RD4, ROVA, Irado, Reinis, RWM, Kliko, Straatbeeld and three iCal calendars. Tested with real addresses: every provider found its own address automatically.
- **App providers** (off by default, `waste.app_providers`): Mijn Afvalwijzer (e.g. Utrecht, Eindhoven, Breda), Burgerportaal (Groningen, Tilburg, Assen, BAR, Nijkerk, RMN), Omrin and Circulus. They use the vendor app's key or a guest login instead of a public API.
- **Faster, smarter search:** the municipality of an address comes from PDOK, so only that municipality's calendar and the regional providers are asked; the provider found is remembered per address.
- **Choose your provider** under *Instellingen → Adres voor de afvalkalender*, or leave it on "Automatisch zoeken". The panel links to the provider.
- **Standard waste names:** codes such as GREEN, PAPER or pbd and long descriptions become GFT, Papier, PMD, Restafval, and so on.
- Config: `waste.providers` now takes provider ids (URLs still add extra opzet calendars), new `waste.app_providers`, and `waste.provider: auto` (the old `opzet` still works).

### 1.10.0
- **New panel NL-Alert** (after Alarmeringen): active and recent NL-Alerts of the last 31 days, with the Dutch or English text, and "in jouw omgeving" when the visitor's weather location lies inside the alert area. Source: the public API behind actueel.nl-alert.nl.
- **New panel Afvalkalender** (after Vandaag): the next collection days. Every visitor sets an own address under *Instellingen*, like the places for alarms and air quality; the server finds the municipal calendar that knows it (16 calendars with the opzet API, e.g. Den Haag, HVC, GAD, DAR, Cyclus). An optional default address can use these calendars, an iCal link or Home Assistant sensors. The push reminder uses each device's own address.
- **New panel Brandstofprijzen** (after Energieprijzen): the daily national average recommended price (GLA) for Euro95, diesel and LPG, with the change since yesterday (UnitedConsumers; see *Data sources*).
- **Push notifications** (off by default; see [Push notifications](#push-notifications)): NL-Alert in your area, KNMI code orange/red, NCTV threat level, earthquakes, big news and the waste reminder. Web Push with end-to-end encryption, built on the Go standard library; `-gen-vapid` creates the key.
- **Trending** above the news: names and words that suddenly appear in many headlines in the last 3 hours; click to search. Can be turned off under *Weergave*.
- **Coverage view** per grouped story: every outlet with its time, the first one marked, and how much later the others followed.
- **Bewaard:** notes and labels per article, a label filter, search in notes, and export to Markdown or JSON.
- **OPML** export and import of sources under *Instellingen → Bronnen* (import only turns on feeds that exist on this server).
- **Aardbevingen:** only quakes from the last 31 days, like AP and Gezondheid. The overview card still covers the last 7 days.
- **Overview:** an NL-Alert card (active or last 24 h), a waste card (collection today or tomorrow) and fuel prices in the Beurs en economie card.
- **Energieprijzen:** a price just below zero shows as €0,00 instead of -€0,00.
- `/api/catalog` now includes each source's feed URL (for the OPML export).
- New config sections `nlalert`, `fuel`, `waste`, `trending` and `push`, and their `refresh` keys.

### 1.9.1
- **Logo:** the favicon's icon now appears in front of "Nieuws Hub" in the top bar.
- **Icon colour:** the stripes of the favicon, the app icons and the logo are blue (#00A4DC); the dark background stays.
- **Phones:** on very narrow screens (320 px) the Alarmeringen line in the top bar no longer makes the page scroll sideways.

### 1.9.0
- **New panel Economie in cijfers** (after Energieprijzen): Dutch inflation with the euro-area figure and a 12-month trend line, unemployment with the change from the previous month, the ECB deposit rate and since when, and the euro in dollars. Sources: Eurostat and the ECB, no key.
- **New panel Beurs** (after Economie in cijfers): the AEX, AMX, BEL 20, DAX, Euro Stoxx 50, S&P 500, Nasdaq, Brent oil, gold and bitcoin with their daily change, and the top 3 risers and fallers of the AEX. Prices are delayed and come from Yahoo Finance's unofficial endpoint (personal use only; see *Data sources*). Indices and stocks are configurable.
- **Overview:** a "Beurs en economie" card.
- **New category Onderzoeksjournalistiek** with Follow the Money, Investico, De Groene Amsterdammer, Lighthouse Reports and Bellingcat, and a preset for it. De Correspondent and Pointer publish no feed and are listed as disabled.
- **New source The Register** (security headlines) under Tech.
- New config sections `economy` and `markets`, and their `refresh` keys.

### 1.8.1
- **Autoriteit Persoonsgegevens acties and Gezondheid:** only items from the last 31 days. Each panel says so when there are none.
- **Kritieke infrastructuur:** the note about grid operators without open data (Enexis and others) is removed; the 0800-9009 outage number stays.
- **Storingen:** the footer no longer refers to Kritieke infrastructuur.

### 1.8.0
- **New panel Hooikoorts** (after Luchtkwaliteit): the pollen forecast for today, tomorrow and the day after (grass, birch, alder, mugwort, ragweed), with indicative levels from none to very high. It uses the same place as Luchtkwaliteit. Source: Open-Meteo (CAMS, Copernicus).
- **New panel Aardbevingen** (after Alarmeringen): KNMI earthquakes in and around the Netherlands, with the number in 30 days, the strongest in 90 days and the latest quakes. Each quake shows its magnitude, depth and an "induced" tag where relevant, and links to its KNMI page.
- **New panel Kritieke infrastructuur** (before Storingen): current electricity and gas outages at **Liander** and **Stedin**, with place, status, expected repair time and customers affected, plus planned work and the number resolved in 24 hours. Enexis, the smaller grid operators and drinking water publish no open outage data; the panel says so and links to the national postcode check.
- **Storingen:**
  - **Internet in the Netherlands** at the top: outage events from IODA (Georgia Tech) for the country and KPN, VodafoneZiggo, Odido and DELTA Fiber, over the last 7 days. The networks are configurable.
  - **Akamai** is added.
  - The providers are shown in the order of `config.yaml` (now alphabetical) instead of problems first.
- **Overview:** a Kritieke infrastructuur card, an Aardbevingen card (when there was a quake in the last 7 days), a hay-fever line in the Luchtkwaliteit card, and internet events in the Storingen card.
- New config sections `pollen`, `utilities`, `quakes` and `outages.internet`, and their `refresh` keys.

### 1.7.2
- **Download or build, your choice at every update:**
  - `docker compose pull && docker compose up -d` runs the ready-made image from GitHub.
  - `docker compose build && docker compose up -d` builds it on your server from your checkout.

  The compose template now has both `image:` and `build:`. Whichever you ran last is what runs.
- **Updates are done by hand;** the automatic-update timer was removed from the README.
- **Releases are created automatically:** pushing a version tag builds the image, the Linux downloads and `SHA256SUMS`, and publishes the GitHub release. The notes come from this changelog.

### 1.7.1
- **Published images:** `ghcr.io/digibaro/news-dashboard-docker`, for linux/amd64 and linux/arm64, built by GitHub Actions for every release tag. Tags: `1.7.1`, `1.7`, `1` and `latest`. A failing vet or test stops the publish.
- **The compose template uses the published image** (`NDB_TAG`, default `1`); building from source moves to an override.
- **Automatic updates:** a systemd timer (or cron line) pulls the image daily and recreates the container only when it changed.
- **CI:** formatting, vet and tests run on every push to `main`.
- **Dockerfile:** cross-compiles for the target platform (no emulation), and labels the image with its source and licence.

### 1.7.0
- **New name:** the app is now called **Nieuws Hub** (browser tab, header and installed app). The program, container and repository keep their technical names.
- **New panel Vandaag** (after Weer):
  - the date and week number, sunrise and sunset with the change in day length, and the moon phase with the next full or new moon
  - the next public holiday and the next clock change
  - school holidays for regio Noord, Midden and Zuid, with the visitor's region highlighted
- **New panel Ransomware NL** (after Datalekken): claims by ransomware groups against Dutch organisations (ransomware.live), with counts for 30 days and 12 months, the most active groups and the latest claims. Countries are configurable.
- **Energieprijzen:** late in the day, the cheapest 3 hours can run into tomorrow ("morgen 12:00–15:00").
- **Overview:** a Vandaag card, and the ransomware count in the Veiligheid card.
- New config sections `today` and `ransomware`, and their `refresh` keys.

### 1.6.2
- **`env_file: .env` in `docker-compose.yml.default`:** every variable in `.env` reaches the container, so new keys no longer need an extra line in your own compose file. Add the `env_file:` block to your existing `docker-compose.yml` once (see the template). It needs Docker Compose 2.24 or newer.
- **Startup warnings:** a missing NS key and the example User-Agent are logged as warnings at startup and after a config reload; a missing abuse.ch key is logged as info.
- **Quick start:** keys and contact details go in `.env` instead of shell exports.

### 1.6.1
- **Treinstoringen checked against live NS data:** the NS API format matches the parser. Display fixes:
  - no trailing period in titles
  - the cause is no longer repeated when NS's situation text already contains it
  - engineering works show NS's period ("… t/m zondag 4 oktober 23:58 uur")
  - the heading says "Werkzaamheden (n)" instead of a misleading "planned in total"

### 1.6.0
- **New panels:**
  - **Luchtkwaliteit:** the nearest Luchtmeetnet station's index and pollutants, for a place chosen per visitor (default: the weather location).
  - **Treinstoringen:** NS disruptions and engineering works (NS Disruptions API; free key via `keys.ns_api_key` / `NS_API_KEY`).
  - **Energieprijzen:** hourly electricity prices for today and tomorrow, the gas price, a chart and the cheapest 3 hours (EnergyZero). Optional extras in config for an all-in price.
  - **Politiek vandaag:** Tweede Kamer meetings of today or the next sitting day, and the latest votes.
- **Overview and kiosk mode**, per visitor or via `?mode=digest` / `?mode=kiosk`.
- **Top bar order:** KNMI, Alarmeringen, then the NCTV threat level.
- New config sections `energy`, `air`, `trains`, `politics`, `keys.ns_api_key` and their `refresh` keys.

### 1.5.2
- **Compose file name:** the template is now `docker-compose.yml.default`; copy it to `docker-compose.yml`, the familiar name. It is still ignored by git. A `docker-compose.yaml` from 1.5.1 keeps working (see *Updating*).

### 1.5.1
- **Docker Compose template:** the repository ships `docker-compose.yaml.default`. Copy it to `docker-compose.yaml`, which is not tracked, so `git pull` never touches your local compose file (see *Updating* for the one-time step).
- **Version:** the version now comes from the `VERSION` file instead of a build argument in the compose file.

### 1.5.0
- **Datalekken panel:** the latest 3 Dutch and 3 other data breaches at organisations from Have I Been Pwned (CC BY 4.0), below Security-adviezen.
  - Each shows the number of accounts, the domain, when the breach happened and when it was added, and the kinds of data leaked (in Dutch or English).
  - Links go to the breach page on HIBP.
  - Spam lists, malware/stealer logs, fabricated, unverified and sensitive breaches are left out.
- New config section `breaches:` and `refresh.breaches`.
- **New news category Datalekken:** DataBreaches.net (new), plus The Record, SecurityWeek, The Hacker News and Autoriteit Persoonsgegevens (moved from Tech).

### 1.4.0
- **English interface:** Nederlands / English / Auto (browser language).
  - The switch is at the top on screens ≥ 700 px, and under Instellingen → Weergave on all screens.
  - Dates and numbers follow the language. News, advisories and alerts stay in their original language.
  - Categories and presets take optional `name_en` / `short_en` / `description_en` from `config.yaml`.
- **Weerlocatie:** an Opslaan button next to the search field; Enter also saves the best match.
- **`refresh:`** in `config.yaml`: how often the browser refreshes each panel (previously fixed in the page).

### 1.3.1
- **Top-bar counts:** P2000 alerts in the last hour per service (Brandweer, Ambulance, Politie, Lifeliner, KNRM) for a configured area, default Den Haag.
  - Feeds are polled and alerts counted over a sliding 60-minute window.
  - "≥" shows while the hour is not yet fully covered (after a restart or an outage).
- **Alarmeringen panel:** the 112 notice was removed from the panel (it remains in this README).

### 1.3.0
- **Alarmeringen panel** (after Verkeer): P2000 alerts from Zwaailicht.nl per city.
  - Grouped as Brandweer, Ambulance, Politie and Lifeliner, at most 2 each, with urgency, units and details.
  - The city is chosen in Instellingen, e.g. "Den Haag" or "Alphen aan den Rijn". It is checked before saving, and "zelfde als weerlocatie" is available.
  - The Lifeliner block shows the latest flights elsewhere in the country when there was none in the city.

### 1.2.1
- **Category chips:** only for categories you follow; "+ Onderwerpen" opens the topic presets.
- **Gezondheid:** now below the "Autoriteit Persoonsgegevens acties" panel (renamed from "AP-acties"). Panel orders saved by 1.2.0 are migrated once.

### 1.2.0
- **Top bar:** the NCTV terrorism threat level and the KNMI weather code.
- **New panels:**
  - **Verkeer** (NDW open data, with VILD road names via range requests)
  - **Storingen** (Azure, Microsoft 365, AWS, Cloudflare; extensible via `statuspage`/`rss`)
  - **Gezondheid** (RIVM health alerts)
- **Panel order:** for existing users, new panels appear at their intended place in the saved order.
- **Test:** a new one catches duplicate top-level names in the page script.

### 1.1.0
- **Story grouping:** the same story from several outlets becomes one item ("Ook bij: …").
- **Read and saved:** read state, a "Bewaard" list, and an option to hide read articles.
- **First visit:** topic presets (configurable), including a regional one based on the weather province. Every category is shown, with one-click recommended sources.
- **Watchlist and mute words:** matching advisories are pinned to the top.
- **Freshness:** per panel, plus an offline banner.
- **Installable:** manifest, icons and a service worker for offline use.
- **Thumbnails:** optional, through the SSRF-safe image proxy.
- **Keyboard shortcuts:** press `?`.
- **Monitoring:** a Bronstatus view, and opt-in `/metrics`.
- **Hardening:**
  - CSP with script hashes instead of `'unsafe-inline'`
  - bidi-override stripping
  - bounded rate limiter
  - trusted-proxy setting for Docker
  - fixed two stale-response races: switching weather location or sources quickly could show old data
  - tests run during the Docker build

### 1.0.0
- News from ~80 feeds, weather (forecast, rain 2 h, warnings), live cyber threats, NCSC advisories, AP actions, themes, Docker and ISPConfig/systemd deployment.

---

## License

Copyright (C) 2026 digibaro

This program is free software: you can redistribute it and/or modify it under the terms of the GNU General Public License as published by the Free Software Foundation, either version 3 of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See [`LICENSE`](LICENSE) for the full text of the GNU General Public License v3.0.

The news, weather, threat and alert data shown by the dashboard belong to their publishers; see [Data sources, terms and licences](#data-sources-terms-and-licences).

## Disclaimer

**English**

This software is provided on a best effort basis. It was developed with care, but it may contain bugs, errors or incomplete features.

**License:** This program is free software, released under the GNU General Public License v3.0 (GPL-3.0). You may redistribute and/or modify it under the terms of that license. See the [`LICENSE`](LICENSE) file or https://www.gnu.org/licenses/gpl-3.0.html for the full text.

**No warranty:** This software is provided "as is", without warranty of any kind, express or implied, including but not limited to the warranties of merchantability, fitness for a particular purpose and non-infringement. As stated in sections 15 and 16 of the GPL-3.0, the author(s) shall not be liable for any damages, data loss or other consequences arising from the use of, or inability to use, this software.

**No support:** No support is provided. The author(s) are under no obligation to answer questions, fix bugs, provide updates or implement feature requests. Issues and contributions may be considered, but without any guarantee of response.

**Use at your own risk.**

**Nederlands**

Deze software wordt aangeboden op best effort-basis. De software is met zorg ontwikkeld, maar kan fouten, bugs of onvolledige functionaliteit bevatten.

**Licentie:** Dit programma is vrije software, uitgebracht onder de GNU General Public License v3.0 (GPL-3.0). Je mag het verspreiden en/of aanpassen onder de voorwaarden van deze licentie. Zie het bestand [`LICENSE`](LICENSE) of https://www.gnu.org/licenses/gpl-3.0.html voor de volledige tekst.

**Geen garantie:** Deze software wordt geleverd "zoals hij is", zonder enige garantie, expliciet of impliciet, waaronder begrepen maar niet beperkt tot garanties van verkoopbaarheid, geschiktheid voor een bepaald doel of het niet inbreuk maken op rechten van derden. Conform artikel 15 en 16 van de GPL-3.0 zijn de auteur(s) niet aansprakelijk voor schade, dataverlies of andere gevolgen voortvloeiend uit het gebruik van, of het niet kunnen gebruiken van, deze software.

**Geen support:** Er wordt geen ondersteuning geboden. De auteur(s) zijn niet verplicht vragen te beantwoorden, bugs op te lossen, updates te leveren of wensen voor nieuwe functies te implementeren. Meldingen en bijdragen worden mogelijk bekeken, maar zonder garantie op een reactie.

**Gebruik is volledig op eigen risico.**
