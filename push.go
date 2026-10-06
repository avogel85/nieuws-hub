package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Push notifications (Web Push: RFC 8030, message encryption RFC 8291, VAPID
// RFC 8292), standard library only. A browser subscribes via the service worker;
// the subscription (a push-service URL plus two keys) is kept in memory and, when
// cache.snapshot_path is set, next to the snapshot. The page re-sends it on every
// visit, so after a restart without snapshot notifications resume once a device
// opens the dashboard again.

var pushTopics = []string{"amber", "nctv", "knmi", "nlalert", "radiation", "quakes", "water", "breaking", "waste"}

// pushHosts are the push services of the major browsers; subscriptions to any
// other host are refused, so the server never posts to arbitrary URLs.
var pushHosts = []string{"fcm.googleapis.com", "updates.push.services.mozilla.com", "push.services.mozilla.com",
	".push.apple.com", ".notify.windows.com"}

type pushSub struct {
	Endpoint string     `json:"endpoint"`
	P256dh   []byte     `json:"p256dh"`
	Auth     []byte     `json:"auth"`
	Topics   []string   `json:"topics"`
	Lang     string     `json:"lang"`
	Lat      *float64   `json:"lat,omitempty"`
	Lon      *float64   `json:"lon,omitempty"`
	Waste    *wasteAddr `json:"waste,omitempty"` // the visitor's address for the waste reminder
	Seen     time.Time  `json:"seen"`
}

type pushState struct {
	Subs     []*pushSub           `json:"subs"`
	NCTV     int                  `json:"nctv"`
	KNMI     string               `json:"knmi"`
	Alerts   map[string]time.Time `json:"alerts"`
	Quakes   map[string]time.Time `json:"quakes"`
	Stories  map[string]time.Time `json:"stories"`
	WasteDay string               `json:"waste_day"`
	Init     bool                 `json:"init"`
}

type pushHub struct {
	mu      sync.Mutex
	subs    map[string]*pushSub
	st      pushState // watcher memory (Subs is only used when saving)
	dirty   bool
	limiter *rateLimiter
	keyB64  string
	sign    *ecdsa.PrivateKey
	pub     []byte // uncompressed P-256 public key (65 bytes)
}

func newPushHub() *pushHub {
	return &pushHub{subs: map[string]*pushSub{}, limiter: newRateLimiter(12, 20),
		st: pushState{Alerts: map[string]time.Time{}, Quakes: map[string]time.Time{}, Stories: map[string]time.Time{}}}
}

// setKey installs the VAPID key (base64url of the 32-byte P-256 private scalar).
func (p *pushHub) setKey(b64 string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b64 == p.keyB64 && p.sign != nil {
		return nil
	}
	sign, pub, err := parseVAPIDKey(b64)
	if err != nil {
		return err
	}
	if p.keyB64 != "" {
		p.subs = map[string]*pushSub{} // subscriptions belong to the old key
	}
	p.keyB64, p.sign, p.pub = b64, sign, pub
	return nil
}

func (p *pushHub) ready() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sign != nil
}

func parseVAPIDKey(b64 string) (*ecdsa.PrivateKey, []byte, error) {
	d, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(b64), "="))
	if err != nil || len(d) != 32 {
		return nil, nil, errors.New("push: vapid_private_key must be the base64url of a 32-byte P-256 key (generate one with -gen-vapid)")
	}
	ek, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return nil, nil, fmt.Errorf("push: invalid vapid key: %w", err)
	}
	sign, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return nil, nil, fmt.Errorf("push: invalid vapid key: %w", err)
	}
	return sign, ek.PublicKey().Bytes(), nil
}

// genVAPIDKey prints a new key for NDB_VAPID_PRIVATE_KEY / push.vapid_private_key.
func genVAPIDKey() (string, error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(k.Bytes()), nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// vapidAuth builds the Authorization header for one push service (RFC 8292).
func (p *pushHub) vapidAuth(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	hdr := b64url([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	input := hdr + "." + b64url(claims)
	sum := sha256.Sum256([]byte(input))
	der, err := ecdsa.SignASN1(rand.Reader, p.sign, sum[:])
	if err != nil {
		return "", err
	}
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		return "", err
	}
	raw := make([]byte, 64)
	sig.R.FillBytes(raw[:32])
	sig.S.FillBytes(raw[32:])
	return "vapid t=" + input + "." + b64url(raw) + ", k=" + b64url(p.pub), nil
}

// encryptPush encrypts one message for a subscription (RFC 8291, aes128gcm, one record).
func encryptPush(uaPub, auth, payload []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, err
	}
	secret, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	info := "WebPush: info\x00" + string(uaPub) + string(asPub)
	ikm, err := hkdf.Key(sha256.New, secret, auth, info, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	var hdr bytes.Buffer
	hdr.Write(salt)
	_ = binary.Write(&hdr, binary.BigEndian, uint32(4096))
	hdr.WriteByte(byte(len(asPub)))
	hdr.Write(asPub)
	plain := append(append([]byte{}, payload...), 2) // 0x02: last record, no padding
	return gcm.Seal(hdr.Bytes(), nonce, plain, nil), nil
}

func validPushEndpoint(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.User != nil || len(s) > 1024 || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range pushHosts {
		if host == strings.TrimPrefix(h, ".") || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			return true
		}
	}
	return false
}

type pushMsg struct {
	Topic  string
	Title  [2]string // nl, en
	Body   [2]string
	URL    string // relative to the dashboard ("#panel-nlalert") or an https article link
	Tag    string
	TTL    int
	Urgent bool
	For    func(*pushSub) bool // optional extra filter
}

func (m pushMsg) payload(lang string) []byte {
	i := 0
	if lang == "en" {
		i = 1
	}
	title, body := m.Title[i], m.Body[i]
	if title == "" {
		title = m.Title[0]
	}
	if body == "" {
		body = m.Body[0]
	}
	b, _ := json.Marshal(map[string]string{"title": truncate(title, 120), "body": truncate(body, 300), "url": m.URL, "tag": m.Tag})
	return b
}

// send delivers a message to one subscription; gone reports a subscription the push service no longer knows.
func (a *App) sendPush(ctx context.Context, sub *pushSub, m pushMsg) (gone bool, err error) {
	p := a.push
	if !p.ready() {
		return false, errors.New("push: no VAPID key")
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return false, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return false, err
	}
	body, err := encryptPush(sub.P256dh, sub.Auth, m.payload(sub.Lang), as, salt)
	if err != nil {
		return false, err
	}
	auth, err := p.vapidAuth(sub.Endpoint, a.config().Push.Subject, time.Now())
	if err != nil {
		return false, err
	}
	ttl, urgency := m.TTL, "normal"
	if ttl <= 0 {
		ttl = 6 * 3600
	}
	if m.Urgent {
		urgency = "high"
	}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: sub.Endpoint, Method: http.MethodPost, Body: body, Header: map[string]string{
		"Authorization": auth, "TTL": fmt.Sprint(ttl), "Urgency": urgency, "Topic": pushTopicHeader(m.Tag),
		"Content-Type": "application/octet-stream", "Content-Encoding": "aes128gcm"}})
	if resp != nil && (resp.Status == http.StatusNotFound || resp.Status == http.StatusGone) {
		return true, err
	}
	return false, err
}

// pushTopicHeader turns a tag into the Topic header (≤ 32 base64url characters), so a
// newer message replaces an undelivered older one with the same tag.
func pushTopicHeader(tag string) string {
	sum := sha256.Sum256([]byte(tag))
	return b64url(sum[:24])
}

// broadcast sends m to every subscription with the message's topic.
func (a *App) broadcast(ctx context.Context, m pushMsg) {
	p := a.push
	p.mu.Lock()
	var targets []*pushSub
	for _, s := range p.subs {
		if slices.Contains(s.Topics, m.Topic) && (m.For == nil || m.For(s)) {
			targets = append(targets, s)
		}
	}
	p.mu.Unlock()
	sent := 0
	for _, s := range targets {
		gone, err := a.sendPush(ctx, s, m)
		if gone {
			p.mu.Lock()
			delete(p.subs, s.Endpoint)
			p.dirty = true
			p.mu.Unlock()
			continue
		}
		if err != nil {
			slog.Warn("push failed", "topic", m.Topic, "host", hostOf(s.Endpoint), "err", err)
			continue
		}
		sent++
	}
	if len(targets) > 0 {
		slog.Info("push sent", "topic", m.Topic, "tag", m.Tag, "devices", sent)
	}
}

func hostOf(s string) string {
	if u, err := url.Parse(s); err == nil {
		return u.Hostname()
	}
	return ""
}

// ---------------------------------------------------------------------------
// Watcher: runs every minute and turns changes into messages. The first run only
// records the current state, so a restart never replays old events.

var knmiRank = map[string]int{"none": 0, "yellow": 1, "orange": 2, "red": 3}

func (a *App) runPushWatch(ctx context.Context) error {
	cfg := a.config()
	p := a.push
	now := time.Now()
	var msgs []pushMsg

	p.mu.Lock()
	first := !p.st.Init
	st := &p.st
	if v, ok := a.threats.get("nctv").Data.(NCTVLevel); ok && v.Level > 0 {
		if !first && st.NCTV > 0 && v.Level != st.NCTV {
			dir := [2]string{"verhoogd", "raised"}
			if v.Level < st.NCTV {
				dir = [2]string{"verlaagd", "lowered"}
			}
			msgs = append(msgs, pushMsg{Topic: "nctv", Tag: "nctv", Urgent: v.Level > st.NCTV, TTL: 24 * 3600, URL: "#top",
				Title: [2]string{"Dreigingsniveau " + dir[0], "Threat level " + dir[1]},
				Body:  [2]string{fmt.Sprintf("De NCTV heeft het dreigingsniveau op %d van 5 gezet (%s).", v.Level, strings.ToLower(v.Name)), fmt.Sprintf("The NCTV set the threat level to %d of 5.", v.Level)}})
		}
		st.NCTV = v.Level
	}
	for _, al := range asSlice[NLAlert](a.threats.get("nlalert").Data) {
		if _, seen := st.Alerts[al.ID]; seen {
			continue
		}
		st.Alerts[al.ID] = al.Start
		if first || !al.active(now) {
			continue
		}
		al := al
		msgs = append(msgs, pushMsg{Topic: "nlalert", Tag: "nlalert-" + al.ID, Urgent: true, TTL: 2 * 3600, URL: "#panel-nlalert",
			Title: [2]string{"NL-Alert", "NL-Alert"}, Body: [2]string{al.Text, firstNonEmpty(al.TextEN, al.Text)},
			For: func(s *pushSub) bool { return s.Lat == nil || s.Lon == nil || al.inArea(*s.Lat, *s.Lon) }})
	}
	for _, al := range asSlice[AmberAlert](a.threats.get("burgernet:amber").Data) {
		key := "amber:" + al.ID
		if _, seen := st.Alerts[key]; seen {
			continue
		}
		st.Alerts[key] = now
		if first {
			continue
		}
		al := al
		name := [2]string{"Vermist Kind Alert", "Missing Child Alert"}
		if al.National {
			name = [2]string{"AMBER Alert", "AMBER Alert"}
		}
		url := al.URL
		if url == "" {
			url = "#amber"
		}
		msgs = append(msgs, pushMsg{Topic: "amber", Tag: "amber-" + al.ID, Urgent: true, TTL: 6 * 3600, URL: url,
			Title: [2]string{name[0] + ": " + al.Title, name[1] + ": " + al.Title},
			Body:  [2]string{al.Text + " Heb je informatie? Bel 112.", al.Text + " Any information? Call 112."},
			For: func(s *pushSub) bool {
				return al.National || (s.Lat != nil && s.Lon != nil && al.covers(*s.Lat, *s.Lon))
			}})
	}
	// Radiation: one message per episode of raised levels (several stations above the alert level).
	if s, ok := a.radiationSummary(cfg.Weather.Location.Lat, cfg.Weather.Location.Lon); ok && s.Stations > 0 {
		if !s.Raised {
			delete(st.Alerts, "radiation:raised")
		} else if _, seen := st.Alerts["radiation:raised"]; !seen {
			st.Alerts["radiation:raised"] = now
			if !first {
				v := strings.Replace(fmt.Sprintf("%.2f", s.Max), ".", ",", 1)
				msgs = append(msgs, pushMsg{Topic: "radiation", Tag: "radiation", Urgent: true, TTL: 6 * 3600, URL: "#panel-air",
					Title: [2]string{"Verhoogde straling gemeten", "Raised radiation measured"},
					Body: [2]string{fmt.Sprintf("%d meetposten van het RIVM meten %s µSv/u of meer (hoogste %s µSv/u in %s). Volg het nieuws en NL-Alert.", s.Above, strings.Replace(fmt.Sprintf("%.2f", s.Level), ".", ",", 1), v, s.MaxName),
						fmt.Sprintf("%d RIVM stations measure %.2f µSv/h or more (highest %.2f µSv/h at %s). Follow the news and NL-Alert.", s.Above, s.Level, s.Max, s.MaxName)}})
			}
		}
	}
	// Water: a sector reaching code oranje or rood, or a storm-surge barrier closing; one message
	// per new episode (a sector that drops back, or a barrier that reopens, can notify again).
	if ws, ok := a.threats.get("rws:water").Data.(WaterStatus); ok && cfg.World.Enabled && cfg.World.Water {
		msgs = append(msgs, waterPush(ws, st.Alerts, now, first)...)
	}
	for _, q := range asSlice[Quake](a.threats.get("knmi:quakes").Data) {
		if _, seen := st.Quakes[q.ID]; seen {
			continue
		}
		st.Quakes[q.ID] = q.Time
		if first || q.Mag == nil || *q.Mag < cfg.Push.QuakeMinMag || now.Sub(q.Time) > 6*time.Hour {
			continue
		}
		mag := fmt.Sprintf("%.1f", *q.Mag)
		msgs = append(msgs, pushMsg{Topic: "quakes", Tag: "quake-" + q.ID, TTL: 6 * 3600, URL: "#panel-quakes",
			Title: [2]string{"Aardbeving M" + strings.Replace(mag, ".", ",", 1), "Earthquake M" + mag},
			Body:  [2]string{fmt.Sprintf("%s, %s uur, diepte %.1f km (KNMI).", q.Place, q.Time.In(amsterdam).Format("15:04"), q.Depth), fmt.Sprintf("%s at %s, depth %.1f km (KNMI).", q.Place, q.Time.In(amsterdam).Format("15:04"), q.Depth)}})
	}
	wasteDay := ""
	if cfg.Waste.Enabled && cfg.Push.WasteHour >= 0 {
		local := now.In(amsterdam)
		today := local.Format("2006-01-02")
		if local.Hour() == cfg.Push.WasteHour && st.WasteDay != today {
			st.WasteDay = today
			wasteDay = local.AddDate(0, 0, 1).Format("2006-01-02")
		}
	}
	for id, t := range st.Alerts {
		if now.Sub(t) > 60*24*time.Hour {
			delete(st.Alerts, id)
		}
	}
	for id, t := range st.Quakes {
		if now.Sub(t) > 400*24*time.Hour {
			delete(st.Quakes, id)
		}
	}
	p.mu.Unlock()

	knmiMsg, knmiLevel, knmiOK := a.knmiPush(ctx, cfg, now)
	breaking := a.breakingPush(cfg, now, first)

	p.mu.Lock()
	if knmiOK {
		if !first && knmiRank[knmiLevel] >= 2 && knmiRank[knmiLevel] > knmiRank[st.KNMI] {
			msgs = append(msgs, knmiMsg)
		}
		st.KNMI = knmiLevel
	}
	st.Init = true
	p.dirty = true
	p.mu.Unlock()
	msgs = append(msgs, breaking...)

	for _, m := range msgs {
		a.broadcast(ctx, m)
	}
	if wasteDay != "" {
		a.wastePush(ctx, cfg, wasteDay)
	}
	a.savePushState()
	return nil
}

// wastePush sends the evening reminder: each device for its own address, or the
// server's default address when the visitor did not set one.
func (a *App) wastePush(ctx context.Context, cfg *Config, day string) {
	p := a.push
	p.mu.Lock()
	groups := map[string][]*pushSub{}
	addrs := map[string]*wasteAddr{}
	for _, s := range p.subs {
		if !slices.Contains(s.Topics, "waste") {
			continue
		}
		k := ""
		if s.Waste != nil {
			k = s.Waste.key()
			addrs[k] = s.Waste
		}
		groups[k] = append(groups[k], s)
	}
	p.mu.Unlock()
	for k, subs := range groups {
		var pickups []WastePickup
		if k == "" {
			if !hasWasteDefault(cfg) {
				continue
			}
			pickups = asSlice[WastePickup](a.threats.get("waste:calendar").Data)
		} else {
			w := *addrs[k]
			res, _, _, err := a.waste.get(k, cfg.Waste.Interval.D(), func() (WasteResult, error) { return a.wasteForAddress(ctx, w) })
			if err != nil {
				continue
			}
			pickups = res.Pickups
		}
		var types []string
		for _, pk := range pickups {
			if pk.Date == day {
				types = append(types, pk.Type)
			}
		}
		if len(types) == 0 {
			continue
		}
		list := strings.Join(types, ", ")
		m := pushMsg{Topic: "waste", Tag: "waste-" + day, TTL: 12 * 3600, URL: "#panel-waste",
			Title: [2]string{"Morgen afval ophalen", "Waste collection tomorrow"},
			Body:  [2]string{"Zet vanavond klaar: " + list + ".", "Put out tonight: " + list + "."}}
		for _, s := range subs {
			if gone, err := a.sendPush(ctx, s, m); gone {
				p.mu.Lock()
				delete(p.subs, s.Endpoint)
				p.dirty = true
				p.mu.Unlock()
			} else if err != nil {
				slog.Warn("push failed", "topic", "waste", "host", hostOf(s.Endpoint), "err", err)
			}
		}
		slog.Info("push sent", "topic", "waste", "devices", len(subs))
	}
}

func asSlice[T any](v any) []T {
	s, _ := v.([]T)
	return s
}

func (a *App) knmiPush(ctx context.Context, cfg *Config, now time.Time) (pushMsg, string, bool) {
	if !cfg.Alerts.KNMI {
		return pushMsg{}, "", false
	}
	ws, _, _, err := a.wx.warnings.get("nl", 10*time.Minute, func() ([]WxWarning, error) { return a.fetchWarnings(ctx, "nl") })
	if err != nil {
		return pushMsg{}, "", false
	}
	s := knmiSummary(ws, now)
	names := map[string][2]string{"orange": {"oranje", "orange"}, "red": {"rood", "red"}}[s.Level]
	types := strings.Join(s.Types, ", ")
	areas := strings.Join(s.Areas, ", ")
	return pushMsg{Topic: "knmi", Tag: "knmi-" + s.Level, Urgent: true, TTL: 12 * 3600, URL: "#panel-weather",
		Title: [2]string{"KNMI code " + names[0], "KNMI code " + names[1]},
		Body:  [2]string{strings.TrimSpace(types + " · " + areas), strings.TrimSpace(types + " · " + areas)}}, s.Level, true
}

// breakingPush: a story that at least push.breaking_sources outlets reported within
// an hour, the last of them in the past 30 minutes. Each story is sent once.
func (a *App) breakingPush(cfg *Config, now time.Time, first bool) []pushMsg {
	need := cfg.Push.BreakingSources
	if need < 2 {
		return nil
	}
	var ids []string
	lang := map[string]string{}
	for _, s := range cfg.Sources {
		if s.IsEnabled() {
			ids = append(ids, s.ID)
			lang[s.ID] = s.Lang
		}
	}
	lists, _ := a.news.collect(ids)
	groups := groupStories(mergeItems(lists, now.Add(-3*time.Hour), 1000, lang), lang)
	p := a.push
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, t := range p.st.Stories {
		if now.Sub(t) > 72*time.Hour {
			delete(p.st.Stories, id)
		}
	}
	var out []pushMsg
	for _, g := range groups {
		if len(g.Related)+1 < need {
			continue
		}
		times := []time.Time{g.Published}
		members := []string{g.ID}
		for _, r := range g.Related {
			times = append(times, r.Published)
			members = append(members, r.ID)
		}
		known := false
		for _, m := range members {
			if _, ok := p.st.Stories[m]; ok {
				known = true
			}
		}
		for _, m := range members {
			p.st.Stories[m] = now
		}
		slices.SortFunc(times, func(x, y time.Time) int { return x.Compare(y) })
		nth := times[need-1]
		if known || first || nth.Sub(times[0]) > time.Hour || now.Sub(nth) > 30*time.Minute {
			continue
		}
		out = append(out, pushMsg{Topic: "breaking", Tag: "story-" + g.ID, TTL: 3 * 3600, URL: g.URL,
			Title: [2]string{fmt.Sprintf("Veel gemeld (%d bronnen)", len(members)), fmt.Sprintf("Widely reported (%d sources)", len(members))},
			Body:  [2]string{g.Title, g.Title}})
	}
	return out
}

// ---------------------------------------------------------------------------
// Persistence (only with cache.snapshot_path): <snapshot>.push.json, mode 0600.

func (a *App) pushStatePath() string {
	if p := a.config().Cache.SnapshotPath; p != "" {
		return p + ".push.json"
	}
	return ""
}

func (a *App) savePushState() {
	path := a.pushStatePath()
	p := a.push
	p.mu.Lock()
	if path == "" || !p.dirty {
		p.mu.Unlock()
		return
	}
	st := p.st
	st.Subs = make([]*pushSub, 0, len(p.subs))
	for _, s := range p.subs {
		st.Subs = append(st.Subs, s)
	}
	b, err := json.Marshal(st)
	p.dirty = false
	p.mu.Unlock()
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		slog.Error("push state write failed", "path", path, "err", err)
	}
}

func (a *App) loadPushState() {
	path := a.pushStatePath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var st pushState
	if json.Unmarshal(b, &st) != nil {
		return
	}
	p := a.push
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range st.Subs {
		if s != nil && validPushEndpoint(s.Endpoint) {
			p.subs[s.Endpoint] = s
		}
	}
	if st.Alerts == nil {
		st.Alerts = map[string]time.Time{}
	}
	if st.Quakes == nil {
		st.Quakes = map[string]time.Time{}
	}
	if st.Stories == nil {
		st.Stories = map[string]time.Time{}
	}
	st.Subs = nil
	p.st = st
	slog.Info("push state loaded", "devices", len(p.subs))
}

// ---------------------------------------------------------------------------
// HTTP: GET /api/push, POST /api/push/{subscribe,unsubscribe,test}.

func (a *App) pushTopicsAvailable(cfg *Config) []string {
	var out []string
	for _, t := range pushTopics {
		ok := map[string]bool{"amber": cfg.Amber.Enabled, "nctv": cfg.Alerts.NCTV.Enabled, "knmi": cfg.Alerts.KNMI, "nlalert": cfg.NLAlert.Enabled,
			"quakes": cfg.Quakes.Enabled, "water": cfg.World.Enabled && cfg.World.Water, "breaking": cfg.Push.BreakingSources >= 2, "waste": cfg.Waste.Enabled, "radiation": cfg.Radiation.Enabled}[t]
		if ok {
			out = append(out, t)
		}
	}
	return out
}

func (a *App) handlePushInfo(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	p := a.push
	p.mu.Lock()
	pub := p.pub
	p.mu.Unlock()
	if !cfg.Push.Enabled || pub == nil {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": true, "key": b64url(pub), "topics": a.pushTopicsAvailable(cfg),
		"quake_min_mag": cfg.Push.QuakeMinMag, "breaking_sources": cfg.Push.BreakingSources, "waste_hour": cfg.Push.WasteHour})
}

// pushRequest checks a POST: same origin, JSON, small body, rate limit.
func (a *App) pushRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	cfg := a.config()
	if !cfg.Push.Enabled || !a.push.ready() {
		writeError(w, r, http.StatusNotFound, "meldingen staan uit")
		return false
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" {
		writeError(w, r, http.StatusForbidden, "alleen vanaf deze site")
		return false
	} else if sfs == "" {
		o, err := url.Parse(r.Header.Get("Origin"))
		if err != nil || o.Host == "" || !strings.EqualFold(o.Host, r.Host) {
			writeError(w, r, http.StatusForbidden, "alleen vanaf deze site")
			return false
		}
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, "JSON verwacht")
		return false
	}
	if !a.push.limiter.allow(a.clientIP(r)) {
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, r, http.StatusBadRequest, "ongeldig verzoek")
		return false
	}
	return true
}

type pushSubReq struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Topics []string   `json:"topics"`
	Lang   string     `json:"lang"`
	Lat    *float64   `json:"lat"`
	Lon    *float64   `json:"lon"`
	Waste  *wasteAddr `json:"waste"`
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(s), "=")
	return base64.RawURLEncoding.DecodeString(s)
}

func (a *App) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var req pushSubReq
	if !a.pushRequest(w, r, &req) {
		return
	}
	p256, err1 := decodeB64(req.Keys.P256dh)
	auth, err2 := decodeB64(req.Keys.Auth)
	if !validPushEndpoint(req.Endpoint) || err1 != nil || err2 != nil || len(auth) != 16 {
		writeError(w, r, http.StatusBadRequest, "onbekende of ongeldige pushdienst")
		return
	}
	if _, err := ecdh.P256().NewPublicKey(p256); err != nil {
		writeError(w, r, http.StatusBadRequest, "ongeldige sleutel")
		return
	}
	cfg := a.config()
	avail := a.pushTopicsAvailable(cfg)
	var topics []string
	for _, t := range req.Topics {
		if slices.Contains(avail, t) && !slices.Contains(topics, t) {
			topics = append(topics, t)
		}
	}
	sub := &pushSub{Endpoint: req.Endpoint, P256dh: p256, Auth: auth, Topics: topics, Lang: "nl", Seen: time.Now().UTC()}
	if req.Lang == "en" {
		sub.Lang = "en"
	}
	if req.Lat != nil && req.Lon != nil && *req.Lat >= 50 && *req.Lat <= 54 && *req.Lon >= 3 && *req.Lon <= 8 {
		la, lo := roundTo(*req.Lat, 0.01), roundTo(*req.Lon, 0.01) // ~1 km is enough for NL-Alert areas
		sub.Lat, sub.Lon = &la, &lo
	}
	if req.Waste != nil {
		if w, ok := parseWasteAddr(req.Waste.Postcode, strconv.Itoa(req.Waste.Number), req.Waste.Suffix); ok {
			if req.Waste.Provider != "" && wasteProviderIDRe.MatchString(req.Waste.Provider) {
				w.Provider = req.Waste.Provider
			}
			sub.Waste = &w
		}
	}
	p := a.push
	p.mu.Lock()
	if _, exists := p.subs[sub.Endpoint]; !exists && len(p.subs) >= cfg.Push.MaxSubscriptions {
		var oldest *pushSub
		for _, s := range p.subs {
			if oldest == nil || s.Seen.Before(oldest.Seen) {
				oldest = s
			}
		}
		delete(p.subs, oldest.Endpoint)
	}
	p.subs[sub.Endpoint] = sub
	p.dirty = true
	p.mu.Unlock()
	a.savePushState()
	writeJSON(w, r, http.StatusOK, 0, map[string]any{"ok": true, "topics": topics})
}

func (a *App) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if !a.pushRequest(w, r, &req) {
		return
	}
	p := a.push
	p.mu.Lock()
	if _, ok := p.subs[req.Endpoint]; ok {
		delete(p.subs, req.Endpoint)
		p.dirty = true
	}
	p.mu.Unlock()
	a.savePushState()
	writeJSON(w, r, http.StatusOK, 0, map[string]any{"ok": true})
}

func (a *App) handlePushTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if !a.pushRequest(w, r, &req) {
		return
	}
	p := a.push
	p.mu.Lock()
	sub := p.subs[req.Endpoint]
	p.mu.Unlock()
	if sub == nil {
		writeError(w, r, http.StatusNotFound, "dit apparaat is niet aangemeld")
		return
	}
	m := pushMsg{Topic: "test", Tag: "test", TTL: 600, URL: "",
		Title: [2]string{"Nieuws Hub", "Nieuws Hub"}, Body: [2]string{"Meldingen werken op dit apparaat.", "Notifications work on this device."}}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	gone, err := a.sendPush(ctx, sub, m)
	if gone {
		p.mu.Lock()
		delete(p.subs, sub.Endpoint)
		p.dirty = true
		p.mu.Unlock()
		writeError(w, r, http.StatusGone, "de pushdienst kent dit apparaat niet meer; zet meldingen opnieuw aan")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "versturen mislukt: "+err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, 0, map[string]any{"ok": true})
}

func roundTo(v, step float64) float64 { return float64(int64(v/step+0.5)) * step }

var waterCodeName = [5][2]string{{}, {"groen", "green"}, {"geel", "yellow"}, {"oranje", "orange"}, {"rood", "red"}}

// waterPush records the current water alarms in seen ("water:…" keys) and returns a message
// for the new ones; keys of alarms that are over are removed.
func waterPush(ws WaterStatus, seen map[string]time.Time, now time.Time, first bool) []pushMsg {
	cur := map[string]bool{}
	var sectors [2][]string
	var barriers []string
	top := 0
	for _, s := range ws.Sectors {
		if s.Code < 3 {
			continue
		}
		k := fmt.Sprintf("water:sector:%s:%d", s.ID, s.Code)
		cur[k] = true
		if _, ok := seen[k]; !ok {
			seen[k] = now
			sectors[0] = append(sectors[0], s.Name)
			top = max(top, s.Code)
		}
	}
	for _, b := range ws.Barriers {
		if b.Open {
			continue
		}
		k := "water:barrier:" + b.Name
		cur[k] = true
		if _, ok := seen[k]; !ok {
			seen[k] = now
			barriers = append(barriers, b.Name)
		}
	}
	for k := range seen {
		if strings.HasPrefix(k, "water:") && !cur[k] {
			delete(seen, k)
		}
	}
	if first || (len(sectors[0]) == 0 && len(barriers) == 0) {
		return nil
	}
	var title, body [2]string
	if len(sectors[0]) > 0 {
		c := waterCodeName[top]
		title = [2]string{"Hoogwater: code " + c[0], "High water: code " + c[1]}
		body = [2]string{"Rijkswaterstaat: code " + c[0] + " voor " + strings.Join(sectors[0], ", ") + ".",
			"Rijkswaterstaat: code " + c[1] + " for " + strings.Join(sectors[0], ", ") + "."}
	}
	if len(barriers) > 0 {
		if title[0] == "" {
			title = [2]string{"Stormvloedkering dicht", "Storm-surge barrier closed"}
		}
		body[0] = strings.TrimSpace(body[0] + " Gesloten: " + strings.Join(barriers, ", ") + ".")
		body[1] = strings.TrimSpace(body[1] + " Closed: " + strings.Join(barriers, ", ") + ".")
	}
	return []pushMsg{{Topic: "water", Tag: "water", Urgent: top >= 4, TTL: 6 * 3600, URL: "#panel-quakes", Title: title, Body: body}}
}
