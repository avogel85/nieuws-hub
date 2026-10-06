package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseNLAlerts(t *testing.T) {
	body := `{"data":[
	 {"id":"aaf5f94b681c","message":"Brand met veel rook in Leeuwarden. Blijf uit de rook! *** Dutch Public Warning System. Fire with a lot of smoke in Leeuwarden.","type":"alert",
	  "start_at":"2026-09-20T03:10:31Z","stop_at":"2026-09-20T05:10:31Z","area":["53.0,5.0 53.0,6.0 54.0,6.0 54.0,5.0"]},
	 {"id":"b2","message":"NL-Alert ingetrokken voor brand in Zwaag.","type":"alert","start_at":"2026-09-21T05:13:53Z","area":[]},
	 {"id":"bad id!","message":"x","type":"alert","start_at":"2026-09-21T05:13:53Z"},
	 {"id":"t1","message":"Test","type":"test","start_at":"2026-09-21T05:13:53Z"}]}`
	list, err := parseNLAlerts([]byte(body))
	if err != nil || len(list) != 2 {
		t.Fatalf("got %d alerts, err %v", len(list), err)
	}
	if list[0].ID != "b2" || !list[0].Withdrawn {
		t.Errorf("newest first and withdrawn detected: %+v", list[0])
	}
	a := list[1]
	if a.Text != "Brand met veel rook in Leeuwarden. Blijf uit de rook!" || a.TextEN != "Fire with a lot of smoke in Leeuwarden." {
		t.Errorf("Dutch/English split: %q / %q", a.Text, a.TextEN)
	}
	if !a.inArea(53.2, 5.8) || a.inArea(52.1, 5.1) {
		t.Error("point in polygon")
	}
	if !a.active(time.Date(2026, 9, 20, 4, 0, 0, 0, time.UTC)) || a.active(time.Date(2026, 9, 20, 6, 0, 0, 0, time.UTC)) {
		t.Error("active window")
	}
	if _, err := parseNLAlerts([]byte(`{"message":"x"}`)); err == nil {
		t.Error("a response without data must fail")
	}
}

func TestParseFuelPrices(t *testing.T) {
	row := func(slug, name, price, change, arrow string) string {
		return `<div><div><a class="_root_3zm9c_1" href="/tanken/brandstofprijzen/product/` + slug + `" title="` + name + `">` + name + `</a></div>` +
			`<div class="_mobileLabelColumn">GLA <sup>*</sup></div><div><span class="_root_170bn_1" data-sentry-component="Price">€` + " " + price + `</span></div>` +
			`<div>Verschil <sup>**</sup></div><div><span class="_root_rojly_1 _icon-` + arrow + `_1y0x3_223"></span>` + change + `</div></div>`
	}
	page := `<html>` + row("euro95", "Euro95", "2,729", "1,2", "pijl-omlaag") + row("diesel", "Diesel", "2,788", "0,0", "pijl-linksrechts") +
		row("lpg", "LPG", "1,265", "+0,4", "pijl-omhoog") + row("super", "Super", "2,939", "0,0", "pijl-linksrechts") +
		`<div class="_tableFooter">Datum overzicht <!-- -->27 september 2026<br/>* GLA</div></html>`
	d, err := parseFuelPrices([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	if d.Date != "2026-09-27" || len(d.Prices) != 3 {
		t.Fatalf("date %q, %d prices", d.Date, len(d.Prices))
	}
	want := []FuelPrice{{"euro95", "Euro95 (E10)", 2.729, -1.2}, {"diesel", "Diesel", 2.788, 0}, {"lpg", "LPG", 1.265, 0.4}}
	for i, w := range want {
		if d.Prices[i] != w {
			t.Errorf("price %d: %+v, want %+v", i, d.Prices[i], w)
		}
	}
	if _, err := parseFuelPrices([]byte(`<html>onderhoud</html>`)); err == nil {
		t.Error("a page without prices must fail")
	}
}

func TestWasteParsers(t *testing.T) {
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, amsterdam)
	op, err := parseOpzetStreams([]byte(`[{"title":"GFT","menu_title":"GFT","ophaaldatum":null},{"title":"Papier","menu_title":"Papier","ophaaldatum":"2026-10-06"},
		{"title":"Rest","menu_title":"Rest","ophaaldatum":"2026-10-02"},{"title":"Oud","menu_title":"Oud","ophaaldatum":"2026-09-01"}]`))
	if err != nil {
		t.Fatal(err)
	}
	got := sortPickups(op, now)
	cycle := sortPickups([]WastePickup{{"PMD", "2026-10-05"}, {"Restafval", "2026-10-07"}, {"PMD", "2026-10-19"}, {"GFT", "2026-10-12"},
		{"Restafval", "2026-10-21"}, {"GFT", "2026-10-26"}, {"Oud", "2026-09-01"}}, now)
	if len(cycle) != 3 || cycle[0] != (WastePickup{"PMD", "2026-10-05"}) || cycle[1] != (WastePickup{"Restafval", "2026-10-07"}) || cycle[2] != (WastePickup{"GFT", "2026-10-12"}) {
		t.Errorf("only the next date per type: %+v", cycle)
	}
	if len(got) != 2 || got[0] != (WastePickup{"Rest", "2026-10-02"}) || got[1].Type != "Papier" {
		t.Errorf("opzet: %+v", got)
	}
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20260929\r\nSUMMARY:Plastic\\, blik en\r\n  drinkpakken\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART:20261001T070000Z\r\nSUMMARY:GFT\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20270601\r\nSUMMARY:Ver weg\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	ic, err := parseWasteICS([]byte(ics), now)
	if err != nil || len(ic) != 2 || ic[0] != (WastePickup{"Plastic, blik en drinkpakken", "2026-09-29"}) || ic[1].Date != "2026-10-01" {
		t.Errorf("ics: %+v %v", ic, err)
	}
	if _, err := parseWasteICS([]byte("<html>"), now); err == nil {
		t.Error("non-calendar must fail")
	}
	for body, want := range map[string]string{
		`{"entity_id":"sensor.afval_rest","state":"2026-10-02","attributes":{"friendly_name":"Restafval"}}`:                     "Restafval 2026-10-02",
		`{"entity_id":"sensor.gft","state":"02-10-2026","attributes":{"friendly_name":"GFT"}}`:                                  "GFT 2026-10-02",
		`{"entity_id":"sensor.papier","state":"Morgen","attributes":{"friendly_name":"Papier","Sort-date":20260928}}`:           "Papier 2026-09-28",
		`{"entity_id":"sensor.pmd","state":"over 3 dagen","attributes":{"friendly_name":"PMD","days_until_collection_date":3}}`: "PMD 2026-09-30",
		`{"entity_id":"sensor.glas","state":"vandaag","attributes":{}}`:                                                         "sensor.glas 2026-09-27",
	} {
		p, ok := parseHAWasteState([]byte(body), now)
		if !ok || p.Type+" "+p.Date != want {
			t.Errorf("home assistant %s: %+v %v, want %s", body, p, ok, want)
		}
	}
	if _, ok := parseHAWasteState([]byte(`{"entity_id":"sensor.x","state":"unknown","attributes":{}}`), now); ok {
		t.Error("an unknown state has no date")
	}
}

func TestComputeTrending(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	var items []Item
	add := func(src, title string, age time.Duration) {
		items = append(items, Item{Source: src, Title: title, Published: now.Add(-age)})
	}
	for i, s := range []string{"nos", "nu", "rtl", "ad", "nrc"} {
		add(s, "Storm Ciarán raast over Nederland", time.Duration(i)*20*time.Minute)
	}
	for i, s := range []string{"nos", "nu", "rtl", "ad"} { // always in the news: no burst
		add(s, "Kabinet "+[]string{"twijfelt", "praat", "beslist", "wacht"}[i], time.Duration(i)*20*time.Minute)
		for d := 0; d < 12; d++ {
			add(s, "Kabinet over begroting", time.Duration(4+d*4)*time.Hour)
		}
	}
	add("nos", "Enkele melding over treinen", time.Hour)
	terms := computeTrending(items, now, 8)
	if len(terms) == 0 || terms[0].Term != "Storm Ciarán" || terms[0].Sources != 5 {
		t.Fatalf("trending: %+v", terms)
	}
	for _, tt := range terms {
		if strings.EqualFold(tt.Term, "storm") || strings.EqualFold(tt.Term, "ciarán") || strings.EqualFold(tt.Term, "kabinet") || strings.Contains(tt.Term, "treinen") {
			t.Errorf("unexpected term %q in %+v", tt.Term, terms)
		}
	}
}

// decryptPush is the browser side of RFC 8291, to check encryptPush.
func decryptPush(t *testing.T, body []byte, ua *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	if rs != 4096 || idlen != 65 {
		t.Fatalf("header rs=%d idlen=%d", rs, idlen)
	}
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := ua.ECDH(as)
	ikm, _ := hkdf.Key(sha256.New, secret, auth, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPub), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatal("missing last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func TestPushEndToEnd(t *testing.T) {
	key, err := genVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)

	var gotHdr http.Header
	var gotBody []byte
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr = r.Header
		gotBody, _ = io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer svc.Close()

	cfgY := validConfig + "push: { enabled: true, subject: \"mailto:test@example.nl\", vapid_private_key: \"" + key + "\" }\n"
	a := newTestApp(t, cfgY)
	a.push = newPushHub()
	if err := a.push.setKey(key); err != nil {
		t.Fatal(err)
	}
	sub := &pushSub{Endpoint: svc.URL + "/push/abc", P256dh: ua.PublicKey().Bytes(), Auth: auth, Topics: []string{"quakes"}, Lang: "en"}
	m := pushMsg{Topic: "quakes", Tag: "q1", Title: [2]string{"Aardbeving", "Earthquake"}, Body: [2]string{"nl", "en"}, URL: "#panel-quakes"}
	if gone, err := a.sendPush(context.Background(), sub, m); err != nil || gone {
		t.Fatalf("send: gone=%v err=%v", gone, err)
	}
	if gotHdr.Get("Content-Encoding") != "aes128gcm" || gotHdr.Get("TTL") == "" || len(gotHdr.Get("Topic")) > 32 {
		t.Errorf("headers: %v", gotHdr)
	}
	var payload map[string]string
	if err := json.Unmarshal(decryptPush(t, gotBody, ua, auth), &payload); err != nil || payload["title"] != "Earthquake" || payload["url"] != "#panel-quakes" {
		t.Errorf("payload %v %v", payload, err)
	}
	// VAPID: the JWT verifies with the public key in k=
	authz := gotHdr.Get("Authorization")
	tok, k, _ := strings.Cut(strings.TrimPrefix(authz, "vapid t="), ", k=")
	parts := strings.Split(tok, ".")
	pubBytes, _ := base64.RawURLEncoding.DecodeString(k)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pubBytes)
	if err != nil || len(parts) != 3 {
		t.Fatalf("vapid header %q: %v", authz, err)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("VAPID signature does not verify")
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !bytes.Contains(claims, []byte(`"aud":"`+svc.URL+`"`)) || !bytes.Contains(claims, []byte("mailto:test@example.nl")) {
		t.Errorf("claims %s", claims)
	}
	// 410 Gone drops the subscription
	a.push.subs[svc.URL+"/push/gone"] = &pushSub{Endpoint: svc.URL + "/push/gone", P256dh: ua.PublicKey().Bytes(), Auth: auth, Topics: []string{"quakes"}}
	a.broadcast(context.Background(), m)
	if _, ok := a.push.subs[svc.URL+"/push/gone"]; ok {
		t.Error("a gone subscription must be removed")
	}
}

func TestPushHTTP(t *testing.T) {
	key, _ := genVAPIDKey()
	a := newTestApp(t, validConfig+"push: { enabled: true, subject: \"mailto:t@example.nl\", vapid_private_key: \""+key+"\" }\n")
	a.push = newPushHub()
	if err := a.push.setKey(key); err != nil {
		t.Fatal(err)
	}
	h := a.routes("/")
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := func(endpoint string) string {
		b, _ := json.Marshal(map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": b64url(ua.PublicKey().Bytes()), "auth": b64url(make([]byte, 16))},
			"topics": []string{"quakes", "nctv", "bogus"}, "lang": "en", "lat": 52.0712, "lon": 4.3009})
		return string(b)
	}
	post := func(path, body string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Host = "dash.example"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	ok := map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	fcm := "https://fcm.googleapis.com/fcm/send/abc123"
	if rec := post("/api/push/subscribe", sub(fcm), ok); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"topics":["quakes","nctv"]`) {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	s := a.push.subs[fcm]
	if s == nil || s.Lang != "en" || s.Lat == nil || *s.Lat != 52.07 {
		t.Errorf("stored subscription: %+v", s)
	}
	for name, c := range map[string]struct {
		path, body string
		hdr        map[string]string
		code       int
	}{
		"cross-site":         {"/api/push/subscribe", sub(fcm), map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}, 403},
		"foreign origin":     {"/api/push/subscribe", sub(fcm), map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}, 403},
		"same origin header": {"/api/push/subscribe", sub(fcm), map[string]string{"Content-Type": "application/json", "Origin": "https://dash.example"}, 200},
		"form post":          {"/api/push/subscribe", sub(fcm), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin"}, 415},
		"unknown service":    {"/api/push/subscribe", sub("https://evil.example/push"), ok, 400},
		"internal service":   {"/api/push/subscribe", sub("https://127.0.0.1/push"), ok, 400},
		"unknown field":      {"/api/push/subscribe", `{"endpoint":"x","evil":1}`, ok, 400},
		"too large":          {"/api/push/subscribe", `{"endpoint":"` + strings.Repeat("a", 5000) + `"}`, ok, 400},
		"test unknown":       {"/api/push/test", `{"endpoint":"https://fcm.googleapis.com/other"}`, ok, 404},
		"post elsewhere":     {"/api/news", `{}`, ok, 405},
	} {
		if rec := post(c.path, c.body, c.hdr); rec.Code != c.code {
			t.Errorf("%s: %d, want %d (%s)", name, rec.Code, c.code, rec.Body)
		}
	}
	if rec := post("/api/push/unsubscribe", `{"endpoint":"`+fcm+`"}`, ok); rec.Code != 200 || a.push.subs[fcm] != nil {
		t.Errorf("unsubscribe: %d", rec.Code)
	}
	info := get(h, "GET", "/api/push", nil).Body.String()
	if !strings.Contains(info, `"enabled":true`) || !strings.Contains(info, `"key":"`) {
		t.Errorf("push info: %s", info)
	}
	for _, e := range []string{"https://fcm.googleapis.com/fcm/send/x", "https://updates.push.services.mozilla.com/wpush/v2/x", "https://web.push.apple.com/x", "https://db5p.notify.windows.com/w/?token=x"} {
		if !validPushEndpoint(e) {
			t.Errorf("%s should be allowed", e)
		}
	}
	for _, e := range []string{"http://fcm.googleapis.com/x", "https://fcm.googleapis.com.evil.example/x", "https://evilpush.apple.com.example/x", "https://fcm.googleapis.com:8443/x", "https://user@fcm.googleapis.com/x"} {
		if validPushEndpoint(e) {
			t.Errorf("%s should be refused", e)
		}
	}
}

func TestPushConfigValidation(t *testing.T) {
	if _, err := parseConfig([]byte(validConfig + "push: { enabled: true, subject: \"mailto:t@example.nl\" }\n")); err == nil || !strings.Contains(err.Error(), "-gen-vapid") {
		t.Errorf("push without key: %v", err)
	}
	key, _ := genVAPIDKey()
	if _, err := parseConfig([]byte(validConfig + "push: { enabled: true, subject: \"t@example.nl\", vapid_private_key: \"" + key + "\" }\n")); err == nil {
		t.Error("subject must be mailto: or https")
	}
	if _, err := parseConfig([]byte(validConfig + "waste: { enabled: true, provider: opzet, postcode: \"2522 aa\", number: 3 }\n")); err != nil {
		t.Errorf("waste opzet: %v", err)
	}
	if _, err := parseConfig([]byte(validConfig + "waste: { enabled: true, provider: home_assistant, home_assistant: { url: \"http://ha.local:8123\", token: \"x\", entities: [\"sensor.afval; rm\"] } }\n")); err == nil {
		t.Error("an invalid entity id must fail")
	}
}

// RFC 8291 Appendix A: the exact encrypted body for the published keys and salt.
func TestRFC8291Vector(t *testing.T) {
	d := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	as, err := ecdh.P256().NewPrivateKey(d("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := encryptPush(d("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"), d("BTBZMqHH6r4Tts7J_aSIgg"),
		[]byte("When I grow up, I want to be a watermelon"), as, d("DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := base64.RawURLEncoding.EncodeToString(body); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
}

func TestWasteVisitorAddress(t *testing.T) {
	var lookups int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups++
		io.WriteString(w, `[]`)
	}))
	defer other.Close()
	mine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/adressen/2522AA-3":
			io.WriteString(w, `[{"bagId":"0518200000640437","huisletter":"","huisnummerToevoeging":""},{"bagId":"0518200000640438","huisletter":"A","huisnummerToevoeging":""}]`)
		case "/rest/adressen/0518200000640438/afvalstromen":
			d := time.Now().In(amsterdam).AddDate(0, 0, 1).Format("2006-01-02")
			io.WriteString(w, `[{"title":"Rest","menu_title":"Rest","ophaaldatum":"`+d+`"},{"title":"GFT","ophaaldatum":null}]`)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	defer mine.Close()
	pdok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"response":{"docs":[{"gemeentenaam":"Testgemeente"}]}}`)
	}))
	defer pdok.Close()
	defer func(u string) { pdokURL = u }(pdokURL)
	pdokURL = pdok.URL
	a := newTestApp(t, validConfig)
	a.cfg.Waste.Providers = []string{other.URL, mine.URL} // extra opzet calendars; the built-in list is then off
	h := a.routes("/")
	if b := get(h, "GET", "/api/waste", nil).Body.String(); !strings.Contains(b, `"needs_address":true`) {
		t.Errorf("without a default address: %s", b)
	}
	b := get(h, "GET", "/api/waste?postcode=2522+aa&number=3&suffix=a", nil).Body.String()
	if !strings.Contains(b, `"type":"Restafval"`) || !strings.Contains(b, `"own":true`) || strings.Contains(b, "GFT") || strings.Contains(b, "2522") {
		t.Errorf("visitor address: %s", b)
	}
	before := lookups
	get(h, "GET", "/api/waste?postcode=2522AA&number=3&suffix=A", nil) // cached: no new lookups
	if lookups != before {
		t.Error("the address must be cached")
	}
	if b := get(h, "GET", "/api/waste?postcode=9999ZZ&number=1", nil).Body.String(); !strings.Contains(b, `"not_found":true`) {
		t.Errorf("unknown address: %s", b)
	}
	for _, q := range []string{"postcode=12&number=3", "postcode=2522AA&number=0", "postcode=2522AA&number=3&suffix=<x>", "postcode=2522AA"} {
		if rec := get(h, "GET", "/api/waste?"+q, nil); rec.Code != 400 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestWasteLabels(t *testing.T) {
	for in, want := range map[string]string{"GREEN": "GFT", "PAPER": "Papier", "PACKAGES": "PMD", "pbd": "PMD", "rst": "Restafval", "BESTAFR": "BEST-tas",
		"Rolcontainer GFT en etensresten": "GFT", "Plastic, Metaal en Drankkartons": "PMD", "Oud papier & karton": "Papier", "Grijze container / Sortibak": "Restafval",
		"MOBILETRANSFERPOINT": "Milieustraat op wielen", "BULKYRESTWASTE": "Grofvuil", "MAAS": "Maas", "Recyclewagen": "Recyclewagen", "Plastic+": "Plastic"} {
		if got := wasteLabel(in); got != want {
			t.Errorf("wasteLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAmsterdamPickups(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, amsterdam) // Monday, ISO week 40 (even)
	got := amsterdamPickups([]amsterdamItem{
		{Code: "Rest", Name: "Rest", Days: "maandag, donderdag", Where: "Aan de stoep"},
		{Code: "Papier", Name: "Papier", Days: "woensdag", Frequency: "oneven weken"},
		{Code: "GFT", Name: "GFT", Days: "maandag, dinsdag", Where: "Container"}, // container: no pickups
		{Code: "GA", Name: "Grof afval", Days: "vrijdag", Frequency: "9-10, 23-10-26"},
	}, now)
	byType := map[string][]string{}
	slices.SortFunc(got, func(a, b WastePickup) int { return strings.Compare(a.Date, b.Date) })
	for _, p := range got {
		byType[p.Type] = append(byType[p.Type], p.Date)
	}
	if r := byType["Restafval"]; len(r) < 3 || r[0] != "2026-09-28" || r[1] != "2026-10-01" {
		t.Errorf("weekly: %v", r)
	}
	if p := byType["Papier"]; len(p) == 0 || p[0] != "2026-10-07" { // week 41 is odd
		t.Errorf("odd weeks: %v", p)
	}
	if len(byType["GFT"]) != 0 {
		t.Error("containers have no collection days")
	}
	if g := byType["Grof afval"]; len(g) != 2 || g[0] != "2026-10-09" || g[1] != "2026-10-23" {
		t.Errorf("explicit dates: %v", g)
	}
}

func TestWasteProviderConfig(t *testing.T) {
	c, err := parseConfig([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	all := enabledWasteProviders(c)
	if len(all) < 45 || slices.ContainsFunc(all, func(p wasteProvider) bool { return p.App }) {
		t.Errorf("default: %d providers, app providers must be off", len(all))
	}
	c.Waste.AppProviders = true
	if n := len(enabledWasteProviders(c)); n != len(wasteProviders) {
		t.Errorf("with app providers: %d of %d", n, len(wasteProviders))
	}
	c.Waste.Providers = []string{"denhaag", "https://afval.example.nl"}
	if l := enabledWasteProviders(c); len(l) != 2 || l[0].ID != "opzet:afval.example.nl" || l[1].ID != "denhaag" {
		t.Errorf("explicit list: %+v", l)
	}
	for yaml, ok := range map[string]bool{
		"waste: { providers: [nosuchprovider] }":                                           false,
		"waste: { provider: omrin, postcode: \"9022CB\", number: 1 }":                      false, // app provider without app_providers
		"waste: { app_providers: true, provider: omrin, postcode: \"9022CB\", number: 1 }": true,
		"waste: { provider: opzet }":                                                       true, // 1.10.0 name
		"waste: { providers: [\"http://insecure.example\"] }":                              false,
	} {
		if _, err := parseConfig([]byte(validConfig + yaml + "\n")); (err == nil) != ok {
			t.Errorf("%s: err=%v, want ok=%v", yaml, err, ok)
		}
	}
}

// Discovery asks the municipality's own calendar and the regional ones, never
// another municipality's calendar.
func TestWasteDiscovery(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]int{}
	cal := func(name string, knows bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			asked[name]++
			mu.Unlock()
			switch {
			case strings.HasSuffix(r.URL.Path, "/afvalstromen"):
				io.WriteString(w, `[{"title":"GREEN","ophaaldatum":"`+time.Now().In(amsterdam).AddDate(0, 0, 2).Format("2006-01-02")+`"}]`)
			case knows:
				io.WriteString(w, `[{"bagId":"123"}]`)
			default:
				io.WriteString(w, `[]`)
			}
		}))
	}
	own, other, regional := cal("own", false), cal("other", true), cal("regional", true)
	defer own.Close()
	defer other.Close()
	defer regional.Close()
	pdok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"response":{"docs":[{"gemeentenaam":"Eigenstad"}]}}`)
	}))
	defer pdok.Close()
	defer func(u string) { pdokURL = u }(pdokURL)
	pdokURL = pdok.URL
	a := newTestApp(t, validConfig)
	list := []wasteProvider{
		{ID: "other", Name: "Anderstad", Kind: "opzet", URL: other.URL, Gemeenten: []string{"Anderstad"}},
		{ID: "own", Name: "Eigenstad", Kind: "opzet", URL: own.URL, Gemeenten: []string{"Eigenstad"}},
		{ID: "regional", Name: "Regio", Kind: "opzet", URL: regional.URL},
	}
	p, pk, err := a.discoverWaste(context.Background(), wasteAddr{Postcode: "1234AB", Number: 1}, list)
	if err != nil || p.ID != "regional" || len(pk) != 1 || pk[0].Type != "GFT" {
		t.Fatalf("discovery: %v %+v %v", p.ID, pk, err)
	}
	if asked["other"] != 0 || asked["own"] != 1 {
		t.Errorf("asked: %v (another municipality's calendar must not be asked)", asked)
	}
}

// Reference values: NASA/JPL Horizons, astrometric J2000, 2026-09-28 20:00 UTC,
// and the moon's rise/set for Den Haag (19:40 and 11:30 local time).
func TestSkyAgainstHorizons(t *testing.T) {
	at := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		k       string
		ra, dec float64
	}{{"jupiter", 141.44638, 15.74114}, {"saturn", 11.51069, 2.00016}, {"mars", 122.45225, 21.09359}, {"venus", 213.01399, -20.56331}} {
		ra, dec := planetRADec(c.k, at)
		if math.Abs(ra-c.ra) > 0.2 || math.Abs(dec-c.dec) > 0.2 {
			t.Errorf("%s: %.2f %.2f, JPL %.2f %.2f", c.k, ra, dec, c.ra, c.dec)
		}
	}
	s := computeSky(52.08, 4.30, time.Date(2026, 9, 28, 18, 0, 0, 0, amsterdam))
	near := func(p *time.Time, hm string) bool {
		if p == nil {
			return false
		}
		want, _ := time.ParseInLocation("2006-01-02 15:04", hm, amsterdam)
		return p.Sub(want).Abs() <= 6*time.Minute
	}
	if !near(s.Moon.Rise, "2026-09-28 19:40") || !near(s.Moon.Set, "2026-09-29 11:30") {
		t.Errorf("moon rise/set: %v %v", s.Moon.Rise, s.Moon.Set)
	}
	if !near(s.Sunset, "2026-09-28 19:27") || s.Dark == nil || s.Dawn == nil {
		t.Errorf("sun: %v %v %v", s.Sunset, s.Dark, s.Dawn)
	}
	names := []string{}
	for _, p := range s.Planets {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "Saturnus,Mars,Jupiter" {
		t.Errorf("planets tonight: %v", names)
	}
	if lv := auroraLevel(7.3); lv != 3 || auroraLevel(4.7) != 0 {
		t.Error("aurora levels")
	}
	kp, err := parseKpForecast([]byte(`[{"time_tag":"2026-09-28T18:00:00","kp":2.33,"observed":"predicted"},{"time_tag":"2026-09-28T21:00:00","kp":6.0,"observed":"predicted"},{"time_tag":"2026-09-29T09:00:00","kp":8,"observed":"predicted"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if m := maxKp(kp, time.Date(2026, 9, 28, 18, 40, 0, 0, time.UTC), time.Date(2026, 9, 29, 4, 27, 0, 0, time.UTC)); m == nil || *m != 6 {
		t.Errorf("max Kp tonight: %v", m)
	}
}

func TestThunderAndInsects(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	th := computeThunder([]int64{1, 2, 3}, []*float64{f(0), f(1.5), nil}, []*float64{f(100), f(800), f(2500)}, []int{3, 80, 3})
	if th.Level != 2 || th.Peak != 2 {
		t.Errorf("thunder: %+v", th)
	}
	if thunderRisk(0, 0, 96) != 3 || thunderRisk(0, 50, 3) != 0 {
		t.Error("thunder codes")
	}
	body := `{"hourly":{"time":["2026-06-10T12:00","2026-06-10T20:00","2026-06-11T12:00","2026-06-11T20:00","2026-01-10T12:00","2026-01-10T20:00"],
	 "temperature_2m":[20,19,4,10,6,3],"relative_humidity_2m":[85,80,60,90,90,90],"wind_speed_10m":[5,8,30,30,10,10]}}`
	d, err := parseInsects([]byte(body), time.Date(2026, 1, 1, 9, 0, 0, 0, amsterdam))
	if err != nil {
		t.Fatal(err)
	}
	// warm and humid June day: high; cold day: none; mild January day: ticks lowered in winter
	if len(d.Days) != 3 || d.Days[0].Date != "2026-06-10" || d.Days[0].Ticks != 3 || d.Days[0].Mosquito != 3 ||
		d.Days[1].Ticks != 0 || d.Days[1].Mosquito != 0 || d.Days[2].Date != "2026-01-10" || d.Days[2].Ticks != 0 {
		t.Errorf("insects: %+v", d.Days)
	}
}

func TestParseF1(t *testing.T) {
	sched := `{"MRData":{"RaceTable":{"season":"2026","Races":[
	 {"round":"15","raceName":"Azerbaijan Grand Prix","date":"2026-09-26","time":"11:00:00Z","Circuit":{"circuitName":"Baku City Circuit","Location":{"locality":"Baku","country":"Azerbaijan"}}},
	 {"round":"16","raceName":"Singapore Grand Prix","date":"2026-10-11","time":"12:00:00Z","Circuit":{"circuitName":"Marina Bay","Location":{"locality":"Marina Bay","country":"Singapore"}},
	  "Qualifying":{"date":"2026-10-10","time":"13:00:00Z"},"Sprint":{"date":"2026-10-10","time":"09:00:00Z"}}]}}}`
	next, season, err := parseF1Schedule([]byte(sched), time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC))
	if err != nil || season != "2026" || next == nil || next.Round != 16 || next.Place != "Marina Bay, Singapore" || len(next.Sessions) != 2 || next.Sessions[0].Name != "Sprint" {
		t.Fatalf("schedule: %+v %v", next, err)
	}
	last, err := parseF1Last([]byte(`{"MRData":{"RaceTable":{"Races":[{"round":"15","raceName":"Azerbaijan Grand Prix","date":"2026-09-26","time":"11:00:00Z",
	 "Results":[{"position":"1","Driver":{"givenName":"Kimi","familyName":"Antonelli"},"Constructor":{"name":"Mercedes"}},{"position":"2","Driver":{"givenName":"George","familyName":"Russell"},"Constructor":{"name":"Mercedes"}},
	 {"position":"4","Driver":{"givenName":"X","familyName":"Y"},"Constructor":{"name":"Z"}}]}]}}}`))
	if err != nil || len(last.Podium) != 2 || last.Podium[0].Driver != "Kimi Antonelli" {
		t.Errorf("last: %+v %v", last, err)
	}
	st, err := parseF1Standings([]byte(`{"MRData":{"StandingsTable":{"StandingsLists":[{"DriverStandings":[{"position":"1","points":"302","Driver":{"givenName":"Kimi","familyName":"Antonelli"},"Constructors":[{"name":"Mercedes"}]}]}]}}}`))
	if err != nil || len(st) != 1 || st[0].Points != 302 || st[0].Team != "Mercedes" {
		t.Errorf("standings: %+v %v", st, err)
	}
}

func TestSportEvents(t *testing.T) {
	a := newTestApp(t, validConfig+`sports:
  events:
    - { sport: athletics, name: "EK atletiek", start: "2026-08-10", end: "2026-08-16", keywords: ["EK atletiek"] }
    - { sport: athletics, name: "EK indoor", start: "2027-03-04", end: "2027-03-07" }
    - { sport: athletics, name: "WK atletiek", start: "2027-09-10", end: "2027-09-19" }
    - { sport: mtb, name: "WK mountainbike", start: "2026-09-20", end: "2026-09-27", keywords: ["WK mountainbike"] }
`)
	evs := a.sportEvents(a.cfg, time.Date(2026, 9, 28, 12, 0, 0, 0, amsterdam))
	var got []string
	for _, e := range evs {
		got = append(got, e.Sport+":"+e.Name+":"+e.Status)
	}
	// config order: f1, road, mtb, athletics, football; per sport the recent one and up to three coming ones
	if strings.Join(got, "|") != "mtb:WK mountainbike:done|athletics:EK indoor:upcoming|athletics:WK atletiek:upcoming" {
		t.Errorf("events: %v", got)
	}
	b := validConfig + "sports:\n  events:\n"
	for i := 1; i <= 5; i++ {
		b += fmt.Sprintf("    - { sport: road, name: \"Koers %d\", start: \"2027-0%d-01\", end: \"2027-0%d-01\" }\n", i, i, i)
	}
	b += "    - { sport: football, name: \"WK vrouwen 2099\", start: \"2099-06-24\", end: \"2099-07-25\", note: \"Brazilië\", tentative: true }\n" // far ahead: the handler below uses the real clock
	a2 := newTestApp(t, b)
	got = nil
	for _, e := range a2.sportEvents(a2.cfg, time.Date(2026, 12, 1, 12, 0, 0, 0, amsterdam)) {
		got = append(got, e.Name)
	}
	if strings.Join(got, "|") != "Koers 1|Koers 2|Koers 3|WK vrouwen 2099" {
		t.Errorf("at most three coming events per sport: %v", got)
	}
	if body := get(a2.routes("/"), "GET", "/api/sports", nil).Body.String(); !strings.Contains(body, `"note":"Brazilië"`) || !strings.Contains(body, `"tentative":true`) {
		t.Errorf("note and tentative: %s", body)
	}
	for _, bad := range []string{`sports: { events: [ { sport: f1, name: "x", start: "2026-01-01", end: "2026-01-02" } ] }`,
		`sports: { events: [ { sport: mtb, name: "x", start: "2026-01-05", end: "2026-01-02" } ] }`, `sports: { sports: [darts] }`} {
		if _, err := parseConfig([]byte(validConfig + bad + "\n")); err == nil {
			t.Errorf("must fail: %s", bad)
		}
	}
}

// A config.yaml from 1.14.0/1.14.1 with the removed Kwetsbaarheden panel still loads, with a warning.
func TestRemovedVulnsConfig(t *testing.T) {
	c, err := parseConfig([]byte(validConfig + "vulns:\n  enabled: true\n  products: [Fortinet]\n  days: 30\nkeys: { nvd_api_key: \"x\" }\nrefresh: { vulns: 60m, news: 5m }\n"))
	if err != nil {
		t.Fatalf("an old config must still load: %v", err)
	}
	warn, _ := configWarnings(c)
	if !slices.ContainsFunc(warn, func(w string) bool { return strings.Contains(w, "Kwetsbaarheden panel was removed") }) {
		t.Errorf("warnings: %v", warn)
	}
	if _, ok := refreshSeconds(c.Refresh)["vulns"]; ok {
		t.Error("refresh.vulns must not reach the browser")
	}
	a := newTestApp(t, validConfig)
	if rec := get(a.routes("/"), "GET", "/api/vulns", nil); rec.Code != 404 {
		t.Errorf("/api/vulns: %d", rec.Code)
	}
}

// testdata/burgernet-test-alerts.json: the four messages of Burgernet's test feed
// (AMBER Alert start and close, Vermist Kind Alert start and close).
func TestParseAmber(t *testing.T) {
	body, err := os.ReadFile("testdata/burgernet-test-alerts.json")
	if err != nil {
		t.Fatal(err)
	}
	list, err := parseAmber(body)
	if err != nil || len(list) != 2 {
		t.Fatalf("closing messages must be left out: %d alerts, %v", len(list), err)
	}
	amber, vka := list[0], list[1]
	if !amber.National || amber.Title != "Voornaam (leeftijd)" || amber.Kind != "Vermist" || amber.URL != "https://www.politie.nl/amberalert" ||
		amber.Image != "https://services.burgernet.nl/fototest/9999.jpg" || !strings.HasPrefix(amber.Text, "Laatst gezien") || amber.Sent.IsZero() {
		t.Errorf("amber: %+v", amber)
	}
	if vka.National || vka.Area != "Amsterdam" || vka.radiusKm != 5 {
		t.Errorf("vermist kind alert: %+v", vka)
	}
	// Amsterdam Centraal is within the 5 km circle, Den Haag is not; an AMBER Alert covers everyone
	if !vka.covers(52.3791, 4.9003) || vka.covers(52.0705, 4.3007) || !amber.covers(51.44, 5.47) {
		t.Error("covers")
	}
	if l, err := parseAmber([]byte(`[]`)); err != nil || len(l) != 0 {
		t.Errorf("empty feed: %v %v", l, err)
	}
	if _, err := parseAmber([]byte(`<html>`)); err == nil {
		t.Error("an HTML page must fail")
	}
	// the handler: the photo only via this server's image proxy, "near" per visitor
	a := newTestApp(t, validConfig+"features: { show_images: true, proxy_images: true }\n")
	a.threats.ok("burgernet:amber", list, "", "")
	h := a.routes("/")
	b := get(h, "GET", "/api/amber?lat=52.0705&lon=4.3007", nil).Body.String()
	if strings.Contains(b, "services.burgernet.nl") || !strings.Contains(b, `"image":"api/img?u=`) || strings.Count(b, `"near":true`) != 1 {
		t.Errorf("handler (Den Haag): %s", b)
	}
	if b := get(h, "GET", "/api/amber?lat=52.3791&lon=4.9003", nil).Body.String(); strings.Count(b, `"near":true`) != 2 {
		t.Errorf("handler (Amsterdam): %s", b)
	}
	a2 := newTestApp(t, validConfig+"features: { proxy_images: false }\n")
	a2.threats.ok("burgernet:amber", list, "", "")
	if b := get(a2.routes("/"), "GET", "/api/amber", nil).Body.String(); strings.Contains(b, `"image"`) {
		t.Errorf("without the image proxy no photo link: %s", b)
	}
}

func TestSatellite(t *testing.T) {
	caps := []byte(`<Layer><Name>rgb_geocolour</Name><Dimension name="time" default="2026-09-28T19:20:00Z" units="ISO8601" nearestValue="1">2024-09-23T00:00:00.000Z/2026-09-28T19:20:00.000Z/PT10M</Dimension></Layer>`)
	if tm, err := parseSatTime(caps); err != nil || !tm.Equal(time.Date(2026, 9, 28, 19, 20, 0, 0, time.UTC)) {
		t.Errorf("time: %v %v", tm, err)
	}
	if _, err := parseSatTime([]byte(`<ServiceException>no layer</ServiceException>`)); err == nil {
		t.Error("capabilities without a time must fail")
	}
	u := satMapURL("https://view.eumetsat.int/geoserver", "a:b,c:d", "image/png", "")
	if !strings.Contains(u, "styles=%2C&") || !strings.Contains(u, "transparent=true") || strings.Contains(u, "time=") {
		t.Errorf("overlay url %s", u)
	}
	jpeg, png := []byte("\xff\xd8\xff\xe0jpeg"), []byte("\x89PNG\r\n\x1a\nrest")
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		calls = append(calls, q.Get("request")+" "+q.Get("format"))
		switch {
		case q.Get("request") == "GetCapabilities" && r.URL.Path == "/mtg_fd/rgb_geocolour/ows":
			w.Write(caps)
		case q.Get("format") == "image/jpeg" && q.Get("time") == "2026-09-28T19:20:00Z":
			w.Write(jpeg)
		case q.Get("format") == "image/png":
			w.Write(png)
		default:
			w.Write([]byte(`<ServiceExceptionReport/>`))
		}
	}))
	defer srv.Close()
	a := newTestApp(t, validConfig)
	a.cfg.Satellite.URL = srv.URL
	if err := a.satelliteJob(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.satelliteJob(context.Background()); err != nil || len(calls) != 4 { // 2nd run: same time, only capabilities
		t.Fatalf("second run: %v, calls %v", err, calls)
	}
	h := a.routes("/")
	b := get(h, "GET", "/api/satellite", nil).Body.String()
	if !strings.Contains(b, `"image":"api/satellite/image?t=1790623200"`) || !strings.Contains(b, `"overlay":"api/satellite/overlay?t=`) || strings.Contains(b, srv.URL) {
		t.Errorf("handler: %s", b)
	}
	rec := get(h, "GET", "/api/satellite/image?t=1", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(rec.Body.Bytes(), jpeg) {
		t.Errorf("image: %d %v", rec.Code, rec.Header())
	}
	if rec := get(h, "GET", "/api/satellite/overlay", nil); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("overlay: %d", rec.Code)
	}
	// an XML error instead of a JPEG is an error, the previous image stays
	a.cfg.Satellite.Layer = "mtg_fd:other"
	if err := a.satelliteJob(context.Background()); err == nil {
		t.Error("missing capabilities must fail")
	}
	if rec := get(h, "GET", "/api/satellite/image", nil); rec.Code != 200 {
		t.Error("the last image must stay available")
	}
	if b := get(h, "GET", "/api/satellite", nil).Body.String(); !strings.Contains(b, `"error"`) {
		t.Errorf("error not reported: %s", b)
	}
	a2 := newTestApp(t, validConfig+"satellite: { enabled: false }\n")
	if rec := get(a2.routes("/"), "GET", "/api/satellite/image", nil); rec.Code != 404 {
		t.Errorf("disabled: %d", rec.Code)
	}
	if _, err := parseConfig([]byte(validConfig + "satellite: { layer: \"x&request=evil\" }\n")); err == nil {
		t.Error("a layer with other characters must be rejected")
	}
}

func TestWiki(t *testing.T) {
	for _, c := range []struct {
		term, title string
		want        bool
	}{{"Trump", "Donald Trump", true}, {"Poetin", "Vladimir Poetin", true}, {"Grand Prix", "Grand Prix Formule 1 van Spanje", false},
		{"Strafhof Historische", "Internationaal Strafhof", false}, {"Max Verstappen", "Max Verstappen", true}} {
		if got := wikiTitleFits(c.term, c.title); got != c.want {
			t.Errorf("fits(%q, %q) = %v", c.term, c.title, got)
		}
	}
	const base = "https://nl.wikipedia.org"
	std := `{"type":"standard","title":"Donald Trump","description":"president","extract":"Donald John Trump is een <b>Amerikaans</b> politicus.",
		"thumbnail":{"source":"https://upload.wikimedia.org/x.jpg"},"content_urls":{"desktop":{"page":"https://nl.wikipedia.org/wiki/Donald_Trump"}}}`
	s, err := parseWikiSummary([]byte(std), base)
	if err != nil || !s.Found || s.URL != "https://nl.wikipedia.org/wiki/Donald_Trump" || s.Thumb == "" || strings.Contains(s.Extract, "<b>") {
		t.Errorf("standard: %+v %v", s, err)
	}
	if _, err := parseWikiSummary([]byte(`{"type":"disambiguation","title":"Trump","extract":"Trump kan verwijzen naar:"}`), base); err != errWikiNotFound {
		t.Error("a disambiguation page is not a summary")
	}
	evil := strings.NewReplacer("https://nl.wikipedia.org/wiki", "https://evil.example/wiki", "upload.wikimedia.org", "evil.example").Replace(std)
	if s, _ := parseWikiSummary([]byte(evil), base); s.URL != "" || s.Thumb != "" {
		t.Errorf("foreign links must be dropped: %+v", s)
	}

	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/api/rest_v1/page/summary/Trump":
			w.Write([]byte(`{"type":"disambiguation","title":"Trump","extract":"Trump kan verwijzen naar:"}`))
		case "/w/rest.php/v1/search/page":
			w.Write([]byte(`{"pages":[{"key":"Trump","title":"Trump"},{"key":"Melania_Trump_en_Barron","title":"Melania Trump en Barron"},{"key":"Donald_Trump","title":"Donald Trump"}]}`))
		case "/api/rest_v1/page/summary/Donald_Trump":
			w.Write([]byte(strings.ReplaceAll(std, base, "http://"+r.Host)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := newTestApp(t, validConfig+"features: { show_images: true, proxy_images: true }\n")
	a.cfg.Trending.Wikipedia.URL = srv.URL
	a.trend.recent = map[string]time.Time{"trump": time.Now(), "onbekend woord": time.Now()} // chips served in the last hour
	h := a.routes("/")
	b := get(h, "GET", "/api/wiki?term=trump", nil).Body.String()
	if !strings.Contains(b, `"title":"Donald Trump"`) || !strings.Contains(b, `"thumb":"api/img?u=`) || !strings.Contains(b, `"url":"`+srv.URL+`/wiki/Donald_Trump"`) {
		t.Errorf("lookup: %s", b)
	}
	n := len(hits)
	get(h, "GET", "/api/wiki?term=Trump", nil)
	if len(hits) != n {
		t.Error("the summary must be cached")
	}
	if b := get(h, "GET", "/api/wiki?term=Onbekend%20Woord", nil).Body.String(); !strings.Contains(b, `"found":false`) {
		t.Errorf("not found: %s", b)
	}
	if rec := get(h, "GET", "/api/wiki?term=Nederland", nil); rec.Code != 404 {
		t.Errorf("only trending terms may be looked up: %d", rec.Code)
	}
	a2 := newTestApp(t, validConfig+"trending: { wikipedia: { enabled: false } }\n")
	if b := get(a2.routes("/"), "GET", "/api/wiki?term=Trump", nil).Body.String(); !strings.Contains(b, `"enabled":false`) {
		t.Errorf("disabled: %s", b)
	}
}

func TestParseGCP(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	body := `[
	 {"begin":"2026-09-28T09:00:00+00:00","end":null,"modified":"2026-09-28T11:00:00+00:00","external_desc":"Cloud Run <b>errors</b>","severity":"medium",
	  "status_impact":"SERVICE_DISRUPTION","uri":"incidents/abc","currently_affected_locations":[{"title":"Netherlands (europe-west4)","id":"europe-west4"}]},
	 {"begin":"2026-09-27T20:00:00+00:00","end":"2026-09-28T02:00:00+00:00","modified":"2026-09-28T03:00:00+00:00","external_desc":"BigQuery outage","severity":"high",
	  "status_impact":"SERVICE_OUTAGE","uri":"incidents/def","currently_affected_locations":[]},
	 {"begin":"2026-09-01T14:44:00+00:00","end":"2026-09-01T18:52:00+00:00","modified":"2026-09-10T21:20:16+00:00","external_desc":"Old incident","severity":"high",
	  "status_impact":"SERVICE_OUTAGE","uri":"incidents/old"},
	 {"begin":"2026-09-28T10:00:00+00:00","end":null,"modified":"2026-09-28T10:00:00+00:00","external_desc":"Planned change notice","severity":"low",
	  "status_impact":"SERVICE_INFORMATION","uri":"incidents/info"}]`
	v, err := parseGCP([]byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(OutageData)
	if d.Status != "minor" || len(d.Incidents) != 3 {
		t.Fatalf("status %q, %d incidents: %+v", d.Status, len(d.Incidents), d.Incidents)
	}
	in := d.Incidents[0]
	if in.Title != "Cloud Run errors (europe-west4)" || in.URL != "https://status.cloud.google.com/incidents/abc" || in.Status != "verstoring" || in.Resolved {
		t.Errorf("ongoing: %+v", in)
	}
	if !d.Incidents[1].Resolved || d.Incidents[1].Status != "opgelost" || d.Incidents[2].Status != "informatie" {
		t.Errorf("resolved/info: %+v", d.Incidents[1:])
	}
	v, _ = parseGCP([]byte(`[{"end":"","modified":"2026-09-28T11:00:00+00:00","external_desc":"Global outage","severity":"high","status_impact":"SERVICE_OUTAGE","uri":"incidents/x"}]`), now)
	if v.(OutageData).Status != "major" {
		t.Error("an ongoing outage is major")
	}
	if v, _ := parseGCP([]byte(`[]`), now); v.(OutageData).Status != "ok" {
		t.Error("no incidents is ok")
	}
	if _, err := parseGCP([]byte(`<html>`), now); err == nil {
		t.Error("HTML must fail")
	}
}

func TestUV(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	uv := computeUV([]int64{100, 200, 300}, []*float64{f(1.2), f(6.4), nil})
	if uv == nil || uv.Max != 6.4 || uv.Peak != 200 || uv.Level != 2 {
		t.Errorf("uv %+v", uv)
	}
	for v, want := range map[float64]int{0: 0, 2.4: 0, 2.6: 1, 5.4: 1, 7.4: 2, 8: 3, 10.4: 3, 11: 4} {
		if uvLevel(v) != want {
			t.Errorf("level(%v) = %d, want %d", v, uvLevel(v), want)
		}
	}
	if computeUV([]int64{1}, []*float64{nil}) != nil {
		t.Error("no values: no UV")
	}
}

func TestRadiation(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	feat := func(id, name string, lon, lat, v float64, end string) string {
		return fmt.Sprintf(`{"type":"Feature","geometry":{"type":"Point","coordinates":[%v,%v]},"properties":{"id":%q,"name":%q,"site_status":1,
			"analyzed_range_in_h":6,"end_measure":%q,"value":%v,"unit":"µSv/h","nuclide":"Gamma-ODL-Brutto","duration":"1h"}}`, lon, lat, id, name, end, v)
	}
	body := `{"type":"FeatureCollection","features":[` + strings.Join([]string{
		feat("NL0902", "WIERINGERWERF", 5.05, 52.8, 0.088, "2026-09-29T05:00:00Z"),
		feat("NL0902", "WIERINGERWERF", 5.05, 52.8, 0.5, "2026-09-29T05:00:00Z"), // duplicate range: ignored
		feat("NL1001", "DEN HAAG-ZUID", 4.3, 52.05, 0.071, "2026-09-29T05:00:00Z"),
		feat("NL1002", "DELFZIJL", 6.93, 53.33, 0.125, "2026-09-29T04:00:00Z"),
		feat("NL1003", "OUD", 5.0, 52.0, 0.9, "2026-09-27T04:00:00Z"), // older than 12 h: left out of the summary
		feat("DE0001", "BERLIN", 13.4, 52.5, 0.1, "2026-09-29T05:00:00Z"),
	}, ",") + `]}`
	list, err := parseRadiation([]byte(body))
	if err != nil || len(list) != 4 || list[0].Value != 0.088 || list[1].Name != "Den Haag-Zuid" {
		t.Fatalf("parse: %v %+v", err, list)
	}
	s := summarizeRadiation(list, 52.08, 4.31, 0.3, 3, now)
	if s.Stations != 3 || s.Nearest == nil || s.Nearest.ID != "NL1001" || s.Km != 3 || s.Min != 0.071 || s.Max != 0.125 || s.MaxName != "Delfzijl" || s.Raised {
		t.Errorf("summary %+v", s)
	}
	if s := summarizeRadiation(list, 52.08, 4.31, 0.08, 2, now); !s.Raised || s.Above != 2 {
		t.Errorf("raised: %+v", s)
	}
	if _, err := parseRadiation([]byte(`{"features":[]}`)); err == nil {
		t.Error("no stations must fail")
	}
	a := newTestApp(t, validConfig)
	for i := range list { // the handler uses the real clock: make the readings three hours old
		if list[i].ID != "NL1003" {
			list[i].Time = time.Now().Add(-3 * time.Hour)
		}
	}
	a.threats.ok(radKey, list, "", "")
	b := get(a.routes("/"), "GET", "/api/radiation?lat=53.3&lon=6.9", nil).Body.String()
	if !strings.Contains(b, `"name":"Delfzijl"`) || !strings.Contains(b, `"raised":false`) {
		t.Errorf("handler: %s", b)
	}
	if get(a.routes("/"), "GET", "/api/radiation?lat=x", nil).Code != 400 {
		t.Error("bad lat")
	}
}

func TestSolar(t *testing.T) {
	// two days of hourly values, Dutch time: 10:00-14:00 at 500 W/m² on day 1, 100 W/m² at noon on day 2
	var times []int64
	var gti []string
	start := time.Date(2026, 9, 29, 0, 0, 0, 0, amsterdam)
	for i := 0; i < 48; i++ {
		tm := start.Add(time.Duration(i) * time.Hour)
		times = append(times, tm.Unix())
		v := "0"
		if i >= 10 && i <= 14 {
			v = "500"
		}
		if i == 36 {
			v = "100"
		}
		gti = append(gti, v)
	}
	b, _ := json.Marshal(times)
	body := fmt.Sprintf(`{"hourly":{"time":%s,"global_tilted_irradiance":[%s]}}`, b, strings.Join(gti, ","))
	days, err := parseSolar([]byte(body))
	if err != nil || len(days) != 2 {
		t.Fatalf("%v %+v", err, days)
	}
	// 5 h × 500 W/m² = 2.5 kWh/m² × 0.8 = 2.0 kWh per kWp
	if days[0].Date != "2026-09-29" || days[0].KWhPerKW != 2 || days[1].KWhPerKW != 0.08 {
		t.Errorf("yield %+v", days)
	}
	if from := time.Unix(days[0].BestFrom, 0).In(amsterdam); from.Hour() != 9 || days[0].BestTo-days[0].BestFrom != 3*3600 {
		t.Errorf("best window %v-%v", from, time.Unix(days[0].BestTo, 0).In(amsterdam))
	}
	if _, err := parseSolar([]byte(`{"hourly":{}}`)); err == nil {
		t.Error("empty must fail")
	}
	a := newTestApp(t, validConfig)
	h := a.routes("/")
	for _, q := range []string{"tilt=91", "tilt=x", "az=200", "lat=100&lon=5"} {
		if get(h, "GET", "/api/solar?"+q, nil).Code != 400 {
			t.Errorf("%s must be rejected", q)
		}
	}
	if _, err := parseConfig([]byte(validConfig + "solar: { kwp: -1 }\n")); err == nil {
		t.Error("negative kwp must be rejected")
	}
}

func TestTrendingPerSources(t *testing.T) {
	cfg := validConfig
	for _, id := range []string{"b", "c", "d", "e"} {
		cfg += fmt.Sprintf("  - { id: %s, name: %q, category: nl, url: \"https://%s.example/rss\" }\n", id, strings.ToUpper(id), id)
	}
	a := newTestApp(t, cfg)
	now := time.Now()
	for i, id := range []string{"b", "c", "d", "e"} {
		a.news.sources[id] = &SourceState{Items: []Item{{ID: id + "1", Source: id, Title: "Storm Ciarán raast over Nederland", Published: now.Add(-time.Duration(i) * 10 * time.Minute)}}}
	}
	a.news.sources["a"] = &SourceState{Items: []Item{{ID: "a1", Source: "a", Title: "Iets heel anders", Published: now}}}
	h := a.routes("/")
	// detected across all sources (4 use it); shown to a visitor with any of those sources
	if b := get(h, "GET", "/api/trending?sources=b", nil).Body.String(); !strings.Contains(b, `"term":"Storm Ciarán"`) || !strings.Contains(b, `"sources":4`) {
		t.Errorf("a chosen source has the story: %s", b)
	}
	if b := get(h, "GET", "/api/trending?sources=a", nil).Body.String(); strings.Contains(b, "Ciarán") || !strings.Contains(b, `"terms":[]`) {
		t.Errorf("none of the chosen sources has it: %s", b)
	}
	if _, ok := a.trendedRecently("storm ciarán", now); !ok {
		t.Error("a served chip can be looked up on Wikipedia")
	}
	if _, ok := a.trendedRecently("Nederland", now); ok {
		t.Error("a word that was never a chip cannot")
	}
}

// A slow source (EUMETSAT renders a new image on request) may get its own, longer timeout.
func TestFetchTimeoutOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	f := newFetcher(2, func() string { return "test" }, func() time.Duration { return 100 * time.Millisecond })
	if _, err := f.Do(context.Background(), FetchReq{URL: srv.URL}); err == nil {
		t.Error("the configured timeout must still apply")
	}
	if resp, err := f.Do(context.Background(), FetchReq{URL: srv.URL, Timeout: 2 * time.Second}); err != nil || string(resp.Body) != "ok" {
		t.Errorf("with a longer timeout: %v", err)
	}
}

func TestIconCandidates(t *testing.T) {
	page, _ := url.Parse("https://www.example.nl/nieuws/")
	html := `<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="mask-icon" href="/mask.png">
<link rel="icon" type="image/png" sizes="32x32" href="/icon-32.png">
<link rel="icon" type="image/png" sizes="192x192" href='https://cdn.example.nl/icon-192.png'>
<link rel="apple-touch-icon" href="apple.png">
<link rel="shortcut icon" href="/favicon.ico">
<link rel="stylesheet" href="/x.css">`
	got := strings.Join(iconCandidates(page, html), " ")
	want := "https://www.example.nl/nieuws/apple.png https://cdn.example.nl/icon-192.png https://www.example.nl/icon-32.png https://www.example.nl/favicon.ico https://www.example.nl/favicon.ico"
	if got != want {
		t.Errorf("candidates:\n got %s\nwant %s", got, want)
	}
	if got := iconCandidates(page, "<html>no icons</html>"); len(got) != 1 || got[0] != "https://www.example.nl/favicon.ico" {
		t.Errorf("fallback: %v", got)
	}
}

// icoFile builds an .ico with one entry.
func icoFile(w int, data []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint16{0, 1, 1})
	b.Write([]byte{byte(w), byte(w), 0, 0})
	binary.Write(&b, binary.LittleEndian, []uint16{1, 32})
	binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(data)), 22})
	b.Write(data)
	return b.Bytes()
}

// dib builds a bottom-up BMP (as in .ico) of w×w pixels; px gives the colour index or BGRA per pixel.
func dib(w, bpp int, palette [][4]byte, px func(x, y int) []byte, mask func(x, y int) bool) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint32{40, uint32(w), uint32(2 * w)})
	binary.Write(&b, binary.LittleEndian, []uint16{1, uint16(bpp)})
	binary.Write(&b, binary.LittleEndian, []uint32{0, 0, 0, 0, uint32(len(palette)), 0})
	for _, c := range palette {
		b.Write(c[:])
	}
	stride := ((w*bpp + 31) / 32) * 4
	for y := w - 1; y >= 0; y-- {
		row := make([]byte, stride)
		for x := 0; x < w; x++ {
			v := px(x, y)
			if bpp == 8 {
				row[x] = v[0]
			} else {
				copy(row[x*bpp/8:], v)
			}
		}
		b.Write(row)
	}
	ms := ((w + 31) / 32) * 4
	for y := w - 1; y >= 0; y-- {
		row := make([]byte, ms)
		for x := 0; x < w; x++ {
			if mask(x, y) {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		b.Write(row)
	}
	return b.Bytes()
}

func TestDecodeIcon(t *testing.T) {
	// 32-bit BMP entry with alpha: red, left half transparent
	red := dib(16, 32, nil, func(x, y int) []byte {
		if x < 8 {
			return []byte{0, 0, 0, 0}
		}
		return []byte{0, 0, 255, 255}
	}, func(int, int) bool { return false })
	img, err := decodeIcon(icoFile(16, red))
	if err != nil {
		t.Fatal(err)
	}
	if c := color.NRGBAModel.Convert(img.At(12, 3)).(color.NRGBA); c != (color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("32-bit pixel %v", c)
	}
	if _, _, _, a := img.At(2, 3).RGBA(); a != 0 {
		t.Error("32-bit transparent pixel")
	}
	// 8-bit palette entry with the AND mask: blue, top row transparent
	blue := dib(16, 8, [][4]byte{{0, 0, 0, 0}, {255, 0, 0, 0}}, func(x, y int) []byte { return []byte{1} }, func(x, y int) bool { return y == 0 })
	img, err = decodeIcon(icoFile(16, blue))
	if err != nil {
		t.Fatal(err)
	}
	if c := color.NRGBAModel.Convert(img.At(5, 5)).(color.NRGBA); c != (color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("8-bit pixel %v", c)
	}
	if _, _, _, a := img.At(5, 0).RGBA(); a != 0 {
		t.Error("mask not applied")
	}
	// PNG inside the .ico, then scaled to 32×32 with transparency kept
	src := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < 64*32; i++ {
		src.Pix[4*i], src.Pix[4*i+3] = 200, 255 // top half opaque
	}
	var pb bytes.Buffer
	png.Encode(&pb, src)
	img, err = decodeIcon(icoFile(64, pb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	out, err := iconPNG(img)
	if err != nil {
		t.Fatal(err)
	}
	small, err := png.Decode(bytes.NewReader(out))
	if err != nil || small.Bounds().Dx() != 32 {
		t.Fatalf("scaled: %v %v", small.Bounds(), err)
	}
	if _, _, _, a := small.At(10, 5).RGBA(); a != 0xffff {
		t.Error("opaque half lost")
	}
	if _, _, _, a := small.At(10, 25).RGBA(); a != 0 {
		t.Error("transparent half lost")
	}
	for _, bad := range [][]byte{nil, []byte("<html>"), icoFile(16, []byte("short")), {0, 0, 1, 0, 200, 0}} {
		if _, err := decodeIcon(bad); err == nil {
			t.Errorf("must fail: %q", bad)
		}
	}
	if _, err := iconPNG(image.NewNRGBA(image.Rect(0, 0, 32, 32))); err == nil {
		t.Error("an empty icon must be rejected")
	}
}

func TestIconHandler(t *testing.T) {
	a := newTestApp(t, validConfig) // source a: https://a.example/rss
	h := a.routes("/")
	if rec := get(h, "GET", "/api/icon?s=a", nil); rec.Code != 404 {
		t.Errorf("no icon yet: %d", rec.Code)
	}
	a.icons.set("a.example", []byte("\x89PNG fake"))
	rec := get(h, "GET", "/api/icon?s=a", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Content-Security-Policy") != "default-src 'none'" {
		t.Errorf("icon: %d %v", rec.Code, rec.Header())
	}
	if rec := get(h, "GET", "/api/icon?s=nope", nil); rec.Code != 404 {
		t.Error("only configured sources")
	}
	if b := get(h, "GET", "/api/catalog", nil).Body.String(); !strings.Contains(b, `"icon":"api/icon?s=a\u0026v=`) {
		t.Errorf("catalog icon link: %s", b[strings.Index(b, `"sources"`):])
	}
	a2 := newTestApp(t, validConfig+"features: { source_icons: false }\n")
	a2.icons.set("a.example", []byte("\x89PNG fake"))
	if rec := get(a2.routes("/"), "GET", "/api/icon?s=a", nil); rec.Code != 404 {
		t.Error("switched off in config")
	}
}

func TestBaseDomain(t *testing.T) {
	for in, want := range map[string]string{"www.nu.nl": "nu.nl", "myprivacy.dpgmedia.nl": "dpgmedia.nl", "nos.nl": "nos.nl", "feeds.nos.nl.": "nos.nl", "localhost": "localhost"} {
		if got := baseDomain(in); got != want {
			t.Errorf("baseDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIconServices(t *testing.T) {
	got := iconServices("www.nu.nl")
	if len(got) != 5 || got[0] != "https://icons.duckduckgo.com/ip3/www.nu.nl.ico" || got[1] != "https://www.google.com/s2/favicons?sz=64&domain=www.nu.nl" || got[4] != got[1]+"&retry=3" {
		t.Errorf("services: %v", got)
	}
	if c, err := parseConfig([]byte(validConfig + "features: { icon_services: false }\n")); err != nil || c.Features.IconServices || !c.Features.SourceIcons {
		t.Errorf("icon_services can be turned off separately: %v %+v", err, c.Features)
	}
}

// A slow source keeps its newest articles in the list, even with many busy sources.
func TestTopUpPerSource(t *testing.T) {
	now := time.Now()
	mk := func(src string, i int, age time.Duration) Item {
		u := fmt.Sprintf("https://%s.example/%d", src, i)
		title := fmt.Sprintf("%s bericht %d", src, i)
		return Item{ID: u, Source: src, Title: title, URL: u, canon: canonicalURL(u), normTitle: normalizeTitle(title), Published: now.Add(-age)}
	}
	var busy, slow []Item
	for i := 0; i < 30; i++ {
		busy = append(busy, mk("busy", i, time.Duration(i)*time.Minute))
	}
	for i := 0; i < 5; i++ {
		slow = append(slow, mk("slow", i, time.Duration(48+i*24)*time.Hour))
	}
	lists := [][]Item{busy, slow}
	base := mergeItems(lists, time.Time{}, 10, nil)
	if slices.ContainsFunc(base, func(it Item) bool { return it.Source == "slow" }) {
		t.Fatal("setup: the slow source should fall outside the newest 10")
	}
	got := topUpPerSource(base, lists, time.Time{}, 3)
	n := map[string]int{}
	for i, it := range got {
		n[it.Source]++
		if i > 0 && it.Published.After(got[i-1].Published) {
			t.Fatal("not newest first")
		}
	}
	if n["busy"] != 10 || n["slow"] != 3 || got[len(got)-1].Title != "slow bericht 2" {
		t.Errorf("per source: %v, last %q", n, got[len(got)-1].Title)
	}
	if again := topUpPerSource(got, lists, time.Time{}, 3); len(again) != len(got) {
		t.Error("no duplicates when a source already has enough")
	}
	if got := topUpPerSource(base, lists, now.Add(-72*time.Hour), 3); len(got) != 11 {
		t.Errorf("since is respected: %d items", len(got))
	}
	// the handler: per_source is opt-in
	a := newTestApp(t, validConfig+"  - { id: b, name: \"B\", category: nl, url: \"https://b.example/rss\" }\n")
	for i := 0; i < 30; i++ {
		busy[i].Source = "a"
	}
	for i := range slow {
		slow[i].Source = "b"
	}
	a.news.sources["a"], a.news.sources["b"] = &SourceState{Items: busy}, &SourceState{Items: slow}
	h := a.routes("/")
	if b := get(h, "GET", "/api/news?sources=a,b&limit=10&group=0", nil).Body.String(); strings.Contains(b, `"source":"b"`) {
		t.Error("without per_source the slow source is outside the limit")
	}
	if b := get(h, "GET", "/api/news?sources=a,b&limit=10&group=0&per_source=4", nil).Body.String(); strings.Count(b, `"source":"b"`) != 4 {
		t.Errorf("per_source=4: %d from the slow source", strings.Count(b, `"source":"b"`))
	}
}

// With cache.snapshot_path the icons survive a restart, with their fetch time.
func TestIconSnapshot(t *testing.T) {
	dir := t.TempDir()
	cfg := validConfig + "cache: { snapshot_path: \"" + dir + "/cache.json.gz\" }\n"
	a := newTestApp(t, cfg)
	png := []byte("\x89PNG\r\n\x1a\nfake icon")
	a.icons.set("a.example", png)
	a.icons.set("broken.example", nil) // no icon: not stored
	if err := a.saveIcons(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir + "/cache.json.gz.icons.json")
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("file: %v %v", st, err)
	}
	if err := a.saveIcons(); err != nil { // nothing changed: no write
		t.Fatal(err)
	}
	b := newTestApp(t, cfg)
	n, err := b.loadIcons(dir + "/cache.json.gz.icons.json")
	if err != nil || n != 1 {
		t.Fatalf("load: %d %v", n, err)
	}
	e, ok := b.icons.get("a.example")
	orig, _ := a.icons.get("a.example")
	if !ok || !bytes.Equal(e.png, png) || !e.at.Equal(orig.at.UTC()) {
		t.Errorf("restored %+v", e)
	}
	if _, ok := b.icons.get("broken.example"); ok {
		t.Error("failed sites are not restored (they are retried)")
	}
	if rec := get(b.routes("/"), "GET", "/api/icon?s=a", nil); rec.Code != 200 {
		t.Errorf("served after a restart: %d", rec.Code)
	}
	// a damaged or foreign file is skipped
	os.WriteFile(dir+"/bad.json", []byte(`{"version":1,"icons":{"x.example":{"png":"PHNjcmlwdD4=","at":"2026-01-01T00:00:00Z"}}}`), 0o644)
	if n, err := b.loadIcons(dir + "/bad.json"); err != nil || n != 0 {
		t.Errorf("non-PNG data must be skipped: %d %v", n, err)
	}
	if _, err := b.loadIcons(dir + "/missing.json"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	// without snapshot_path nothing is written
	c := newTestApp(t, validConfig)
	c.icons.set("a.example", png)
	if err := c.saveIcons(); err != nil || c.iconsPath() != "" {
		t.Error("no path, no write")
	}
}

// The icon cache can be on without the news snapshot (less disk wear).
func TestIconCachePath(t *testing.T) {
	for _, c := range []struct{ yaml, want string }{
		{"", ""},
		{"cache: { snapshot_path: /d/cache.json.gz }\n", "/d/cache.json.gz.icons.json"},
		{"cache: { icon_cache_path: /d/icons.json }\n", "/d/icons.json"},
		{"cache: { snapshot_path: /d/cache.json.gz, icon_cache_path: /e/icons.json }\n", "/e/icons.json"},
	} {
		cfg, err := parseConfig([]byte(validConfig + c.yaml))
		if err != nil {
			t.Fatal(err)
		}
		if got := iconsPathFor(cfg); got != c.want {
			t.Errorf("%q: %q, want %q", c.yaml, got, c.want)
		}
	}
	t.Setenv("NDB_ICON_CACHE_PATH", "/env/icons.json")
	if cfg, _ := parseConfig([]byte(validConfig)); iconsPathFor(cfg) != "/env/icons.json" || cfg.Cache.SnapshotPath != "" {
		t.Error("NDB_ICON_CACHE_PATH alone: icons only, no news snapshot")
	}
	dir := t.TempDir()
	if err := dirWritable(dir); err != nil {
		t.Errorf("writable: %v", err)
	}
	ro := filepath.Join(dir, "ro")
	os.Mkdir(ro, 0o555)
	if os.Getuid() != 0 && dirWritable(ro) == nil {
		t.Error("a read-only folder must be reported")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the test file must be removed: %v", entries)
	}
}

func TestFileInDir(t *testing.T) {
	dir := t.TempDir()
	for in, want := range map[string]string{"": "", dir: dir + "/icons.json", "/x/y/": "/x/y/icons.json", dir + "/icons.json": dir + "/icons.json", "/no/such/file.json": "/no/such/file.json"} {
		if got := fileInDir(in, "icons.json"); got != want {
			t.Errorf("fileInDir(%q) = %q, want %q", in, got, want)
		}
	}
	cfg, err := parseConfig([]byte(validConfig + "cache: { icon_cache_path: \"" + dir + "\", snapshot_path: \"" + dir + "/\" }\n"))
	if err != nil || cfg.Cache.IconCachePath != dir+"/icons.json" || cfg.Cache.SnapshotPath != dir+"/cache.json.gz" {
		t.Errorf("folders in config.yaml: %+v %v", cfg.Cache, err)
	}
}

func TestWorld(t *testing.T) {
	now := time.Now().UTC() // the handlers filter against the real clock
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	usgs := fmt.Sprintf(`{"type":"FeatureCollection","features":[
	 {"properties":{"mag":6.6,"place":"80 km ENE of Tadine, New Caledonia","time":%d,"url":"https://earthquake.usgs.gov/x","tsunami":1,"alert":"yellow","type":"earthquake"},"geometry":{"coordinates":[168,-21,10.4]}},
	 {"properties":{"mag":5.6,"place":"Costa Rica","time":%d,"type":"earthquake"},"geometry":{"coordinates":[0,0,8]}},
	 {"properties":{"mag":7.1,"place":"too old","time":%d,"type":"earthquake"},"geometry":{"coordinates":[0,0,8]}},
	 {"properties":{"mag":6.2,"place":"Alaska","time":%d,"alert":"<script>","type":"earthquake"},"geometry":{"coordinates":[0,0,35]}}]}`,
		ms(6*24*time.Hour), ms(time.Hour), ms(8*24*time.Hour), ms(2*time.Hour))
	if q3, _ := parseUSGS([]byte(usgs), 6, 72, now); len(q3) != 1 || q3[0].Place != "Alaska" {
		t.Errorf("72 hours: the 6-day-old quake must be left out: %+v", q3)
	}
	q, err := parseUSGS([]byte(usgs), 6, 168, now)
	if err != nil || len(q) != 2 || q[0].Place != "Alaska" || q[1].Mag != 6.6 || !q[1].Tsunami || q[1].Alert != "yellow" || q[1].DepthKm != 10 || q[0].Alert != "" {
		t.Errorf("usgs: %+v %v", q, err)
	}
	if _, err := parseUSGS([]byte(`{"type":"Feature"}`), 6, 168, now); err == nil {
		t.Error("usgs: not a feed")
	}

	date := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	eonet := fmt.Sprintf(`{"events":[
	 {"title":"Hurricane Rachel","categories":[{"id":"severeStorms"}],"sources":[{"url":"https://www.nhc.noaa.gov/"}],"geometry":[{"magnitudeValue":60,"magnitudeUnit":"kts","date":"%s"},{"magnitudeValue":90,"magnitudeUnit":"kts","date":"%s"}]},
	 {"title":"Tropical Storm Old","categories":[{"id":"severeStorms"}],"geometry":[{"magnitudeValue":40,"magnitudeUnit":"kts","date":"%s"}]},
	 {"title":"Wildfire Small, Texas","categories":[{"id":"wildfires"}],"geometry":[{"magnitudeValue":510,"magnitudeUnit":"acres","date":"%s"}]},
	 {"title":"Wildfire Big, Washington","categories":[{"id":"wildfires"}],"geometry":[{"magnitudeValue":10000,"magnitudeUnit":"acres","date":"%s"}]},
	 {"title":"Iceberg A23a","categories":[{"id":"seaLakeIce"}],"geometry":[{"magnitudeValue":100,"magnitudeUnit":"NM^2","date":"%s"}]},
	 {"title":"Etna Volcano, Italy","categories":[{"id":"volcanoes"}],"geometry":[{"date":"%s"}]},
	 {"title":"Tropical Storm Choi-wan","categories":[{"id":"severeStorms"}],"geometry":[{"magnitudeValue":50,"magnitudeUnit":"kts","date":"%s"}]}]}`,
		date(30*time.Hour), date(6*time.Hour), date(5*24*time.Hour), date(time.Hour), date(2*time.Hour), date(time.Hour), date(70*time.Hour), date(3*time.Hour))
	if e1, _ := parseEONET([]byte(eonet), 2000, 24, now); len(e1) != 3 { // 24 hours: Etna (3 days ago) drops out
		t.Errorf("24 hours: %d events", len(e1))
	}
	ev, err := parseEONET([]byte(eonet), 2000, 72, now)
	var got []string
	for _, e := range ev {
		got = append(got, fmt.Sprintf("%s:%s:%d:%d", e.Kind, e.Title, e.WindKmh, e.AreaHa))
	}
	if err != nil || strings.Join(got, "|") != "storm:Hurricane Rachel:167:0|storm:Tropical Storm Choi-wan:93:0|volcano:Etna Volcano, Italy:0:0|wildfire:Wildfire Big, Washington:0:4047" {
		t.Errorf("eonet: %v %v", got, err)
	}
	if ev[0].URL != "https://www.nhc.noaa.gov/" {
		t.Errorf("source link: %q", ev[0].URL)
	}

	ll := fmt.Sprintf(`{"results":[
	 {"name":"Falcon 9 Block 5 | Crew-13","net":"%s","status":{"abbrev":"Success"}},
	 {"name":"Falcon 9 Block 5 | Transporter 18","net":"%s","status":{"abbrev":"In Flight"},"launch_service_provider":{"name":"SpaceX","abbrev":"SpX"},"rocket":{"configuration":{"name":"Falcon 9"}},"mission":{"name":"Transporter 18"},"pad":{"location":{"name":"Vandenberg SFB, CA, USA"}},"net_precision":{"name":"Minute"}},
	 {"name":"Long March 12 | Unknown Payload","net":"%s","status":{"abbrev":"Go"},"launch_service_provider":{"name":"China Aerospace Science and Technology Corporation","abbrev":"CASC"},"net_precision":{"name":"Day"}},
	 {"name":"Ariane 6 | Galileo L14","net":"%s","status":{"abbrev":"TBC"},"launch_service_provider":{"name":"Arianespace"}}]}`,
		now.Add(-3*time.Hour).Format(time.RFC3339), now.Add(-30*time.Minute).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339), now.Add(30*24*time.Hour).Format(time.RFC3339))
	ls, err := parseLaunches([]byte(ll), now)
	if err != nil || len(ls) != 3 {
		t.Fatalf("launches: %+v %v", ls, err)
	}
	if l := ls[0]; l.Status != "flight" || l.Rocket != "Falcon 9" || l.Mission != "Transporter 18" || l.Provider != "SpaceX" || !l.Exact || l.Place != "Vandenberg SFB, CA, USA" {
		t.Errorf("in flight: %+v", l)
	}
	if l := ls[1]; l.Provider != "CASC" || l.Exact || l.Rocket != "Long March 12" {
		t.Errorf("long provider name and day precision: %+v", l)
	}
	if l := ls[2]; l.Status != "tbd" || l.Mission != "Galileo L14" {
		t.Errorf("tbc: %+v", l)
	}

	a := newTestApp(t, validConfig)
	a.threats.ok("usgs:world", q, "", "")
	a.threats.ok("eonet:events", ev, "", "")
	a.threats.ok("ll2:launches", ls, "", "")
	h := a.routes("/")
	if b := get(h, "GET", "/api/world", nil).Body.String(); !strings.Contains(b, `"place":"Alaska"`) || !strings.Contains(b, `"wind_kmh":167`) || !strings.Contains(b, `"min_mag":6`) {
		t.Errorf("world handler: %s", b)
	}
	if b := get(h, "GET", "/api/sky", nil).Body.String(); !strings.Contains(b, `"launches":`) || !strings.Contains(b, `"mission":"Transporter 18"`) {
		t.Errorf("sky handler: %s", b[:min(len(b), 300)])
	}
	if _, err := parseConfig([]byte(validConfig + "world: { hours: 169 }\n")); err == nil {
		t.Error("hours above the feed's week must be rejected")
	}
	for in, want := range map[string]int{"": 24, "world: { days: 3 }\n": 72, "world: { hours: 12, days: 3 }\n": 12} {
		if c, err := parseConfig([]byte(validConfig + in)); err != nil || c.World.Hours != want {
			t.Errorf("%q: hours %d, want %d (%v)", in, c.World.Hours, want, err)
		}
	}
	// launches: the coming 24 hours, and a flight under way
	soon := []Launch{{Mission: "flight", Status: "flight", Time: now.Add(-2 * time.Hour)}, {Mission: "in 3h", Status: "go", Time: now.Add(3 * time.Hour)},
		{Mission: "in 30h", Status: "go", Time: now.Add(30 * time.Hour)}, {Mission: "long ago", Status: "go", Time: now.Add(-3 * time.Hour)}}
	var names []string
	for _, l := range upcomingLaunches(soon, 24, now) {
		names = append(names, l.Mission)
	}
	if strings.Join(names, ",") != "flight,in 3h" {
		t.Errorf("upcoming 24h: %v", names)
	}
	if _, err := parseConfig([]byte(validConfig + "world: { quake_min_mag: 3 }\n")); err == nil {
		t.Error("quake_min_mag below the feed's 4.5 must be rejected")
	}
}

func TestFireRisk(t *testing.T) {
	body, err := os.ReadFile("testdata/natuurbrandrisico.html")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := parseFireRisk(body)
	if err != nil || len(rs) != 25 {
		t.Fatalf("regions: %d %v", len(rs), err)
	}
	ph := map[string]int{}
	for _, r := range rs {
		ph[r.Region] = r.Phase
	}
	if ph["Fryslân"] != 1 || ph["Kennemerland"] != 2 || ph["Noord-Holland-Noord"] != 2 || ph["Zaanstreek-Waterland"] != 0 {
		t.Errorf("phases: %v", ph)
	}
	if _, err := parseFireRisk([]byte(`<html><div class="risk">Utrecht<div class="risk-phase">Fase 1</div></div></html>`)); err == nil {
		t.Error("a page with too few regions (changed layout) must be an error")
	}
	a := newTestApp(t, validConfig)
	a.threats.ok("brandweer:firerisk", rs, "", "")
	if b := get(a.routes("/"), "GET", "/api/world", nil).Body.String(); !strings.Contains(b, `"region":"Kennemerland","phase":2`) || !strings.Contains(b, `"url":"https://www.brandweer.nl/natuurbrandrisico/"`) {
		t.Errorf("world handler: %s", b[:min(len(b), 400)])
	}
}

func TestWater(t *testing.T) {
	read := func(f string) []byte {
		b, err := os.ReadFile("testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ss, err := parseWaterSectors(read("rws-sectors.json"))
	if err != nil || len(ss) != 21 || ss[0].Name != "Bedijkte Maas" || ss[0].Code != 2 || ss[1].Code != 1 {
		t.Fatalf("sectors: %d %v %+v", len(ss), err, ss[:min(2, len(ss))])
	}
	if _, err := parseWaterSectors([]byte(`{"sectors":[{"sectorName":"IJssel","statusCode":1}]}`)); err == nil {
		t.Error("a handful of sectors (changed format) must be an error")
	}
	now := time.Date(2026, 10, 2, 11, 30, 0, 0, time.UTC)
	peak, at, err := parseWaterDay(read("rws-day.json"), now)
	if err != nil || peak != 2 || at == nil || !at.Equal(time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("day: %d %v %v", peak, at, err)
	}
	keringen := read("rws-keringen.html")
	bs, err := parseBarriers(keringen)
	if err != nil || len(bs) != 4 || bs[0].Name != "Oosterscheldekering" || !bs[0].Open || bs[0].Status != "Geopend" || bs[2].Name != "Europoortkering (Maeslant- en Hartelkering)" {
		t.Fatalf("barriers: %v %+v", err, bs)
	}
	// the Maeslantkering closing: any status other than "open" counts as closed
	parts := strings.SplitN(string(keringen), `data-status="open"`, 4) // the third barrier is the Europoortkering
	if len(parts) != 4 {
		t.Fatalf("fixture: %d barrier cells", len(parts)-1)
	}
	third := regexp.MustCompile(`>\s*Geopend`).ReplaceAllString(parts[3][:strings.Index(parts[3], "</p>")], ">Gesloten") + parts[3][strings.Index(parts[3], "</p>"):]
	closed := parts[0] + `data-status="open"` + parts[1] + `data-status="open"` + parts[2] + `data-status="gesloten"` + third
	bs2, _ := parseBarriers([]byte(closed))
	if len(bs2) != 4 || !bs2[0].Open || bs2[2].Open || bs2[2].Status != "Gesloten" {
		t.Errorf("closed barrier: %+v", bs2)
	}
	if txt := parseWaterText(read("rws-waterbeeld.html")); !strings.HasPrefix(txt, "Er worden voor de komende dagen geen afwijkingen") {
		t.Errorf("outlook: %q", txt)
	}
	if _, err := parseBarriers([]byte("<html>maintenance</html>")); err == nil {
		t.Error("a page without rows must be an error")
	}

	// push: nothing for code geel; a message for oranje and a closed barrier, once per episode
	st := WaterStatus{Sectors: ss, Barriers: bs}
	seen := map[string]time.Time{}
	if m := waterPush(st, seen, now, false); m != nil {
		t.Errorf("code geel must not notify: %+v", m)
	}
	st.Sectors = append([]WaterSector{{ID: "ijssel", Name: "IJssel", Code: 3}}, ss...)
	st.Barriers = bs2
	m := waterPush(st, seen, now, false)
	if len(m) != 1 || m[0].Topic != "water" || m[0].Title[0] != "Hoogwater: code oranje" || !strings.Contains(m[0].Body[0], "IJssel") || !strings.Contains(m[0].Body[0], "Gesloten: Europoortkering") {
		t.Fatalf("push: %+v", m)
	}
	if m := waterPush(st, seen, now, false); m != nil {
		t.Errorf("the same episode must not notify twice: %+v", m)
	}
	st.Sectors, st.Barriers = ss, bs
	waterPush(st, seen, now, false)
	if len(seen) != 0 {
		t.Errorf("keys of finished episodes must be removed: %v", seen)
	}
	if m := waterPush(WaterStatus{Barriers: bs2}, seen, now, true); m != nil {
		t.Error("the first run only records the current state")
	}

	a := newTestApp(t, validConfig)
	a.threats.ok("rws:water", WaterStatus{Level: 2, Sectors: ss, Barriers: bs}, "", "")
	if b := get(a.routes("/"), "GET", "/api/alerts", nil).Body.String(); !strings.Contains(b, `"water":{`) || !strings.Contains(b, `"name":"Bedijkte Maas","code":2`) || !strings.Contains(b, `"url":"https://waterberichtgeving.rws.nl/owb/"`) {
		t.Errorf("alerts handler: %s", b[:min(len(b), 500)])
	}
}

func TestSpaceWeather(t *testing.T) {
	b, err := os.ReadFile("testdata/swpc-scales.json")
	if err != nil {
		t.Fatal(err)
	}
	days, err := parseSpaceScales(b)
	if err != nil || len(days) != 4 || !days[0].Now || days[0].G != 0 || days[0].R != 0 || days[1].G != 1 || days[1].R != -1 || days[1].RProb != 10 || days[1].SProb != 1 {
		t.Fatalf("scales: %v %+v", err, days)
	}
	if _, err := parseSpaceScales([]byte(`{"-1":{"DateStamp":"2026-10-01"}}`)); err == nil {
		t.Error("no data for today must be an error")
	}
	f, err := os.ReadFile("testdata/swpc-flares.json")
	if err != nil {
		t.Fatal(err)
	}
	fl, err := parseFlares(f, time.Date(2026, 10, 2, 11, 20, 0, 0, time.UTC))
	if err != nil || fl == nil || fl.Class != "C1.4" {
		t.Errorf("flare: %+v %v", fl, err)
	}
	if fl, _ := parseFlares(f, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)); fl != nil {
		t.Errorf("no flare in the last 24 hours: %+v", fl)
	}
	for _, c := range [][2]string{{"X1.0", "M9.9"}, {"X10", "X9.3"}, {"M1.2", "C9.9"}, {"C1.0", "B9.9"}} {
		if flareRank(c[0]) <= flareRank(c[1]) {
			t.Errorf("%s should outrank %s", c[0], c[1])
		}
	}
	a := newTestApp(t, validConfig)
	a.threats.ok("noaa:space", SpaceWeather{Days: days, Flare: fl}, "", "")
	if b := get(a.routes("/"), "GET", "/api/sky", nil).Body.String(); !strings.Contains(b, `"space":{`) || !strings.Contains(b, `"g":1`) {
		t.Errorf("sky handler: %s", b[:min(len(b), 400)])
	}
}

func TestKNMIHeat(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	ws := []WxWarning{{Level: "orange", Type: "Wind", Area: "Zeeland", Country: "nl"}, {Level: "yellow", Type: "Hitte", Area: "Limburg", Country: "nl"}}
	s := knmiSummary(ws, now)
	if s.Level != "orange" || s.Heat == nil || s.Heat.Level != "yellow" || !s.Heat.Active {
		t.Errorf("heat behind a higher wind code: %+v %+v", s, s.Heat)
	}
	if s := knmiSummary(ws[:1], now); s.Heat != nil {
		t.Errorf("no heat warning: %+v", s.Heat)
	}
}

func TestScamAlerts(t *testing.T) {
	b, err := os.ReadFile("testdata/fraudehelpdesk-alerts.xml")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	as, err := parseScamAlerts(b, now)
	if err != nil || len(as) < 3 || as[0].Title != "Pop-up met beschuldiging van kijken kinderporno" || !strings.HasPrefix(as[0].URL, "https://www.fraudehelpdesk.nl/") {
		t.Fatalf("alerts: %v %+v", err, as[:min(2, len(as))])
	}
	for i, a := range as {
		if now.Sub(a.Published) > 60*24*time.Hour || (i > 0 && a.Published.After(as[i-1].Published)) || strings.Contains(a.Title, "&#") {
			t.Errorf("alert %d: %+v (60 days, newest first, entities decoded)", i, a)
		}
	}
	if as, _ := parseScamAlerts(b, now.AddDate(1, 0, 0)); len(as) != 0 {
		t.Errorf("a year later nothing is recent: %d", len(as))
	}
	a := newTestApp(t, validConfig)
	a.threats.ok("fhd:alerts", as, "", "")
	if body := get(a.routes("/"), "GET", "/api/breaches", nil).Body.String(); !strings.Contains(body, `"phishing":{`) || !strings.Contains(body, "Valse telefoontjes") {
		t.Errorf("breaches handler: %s", body[:min(len(body), 300)])
	}
}

func TestOnThisDay(t *testing.T) {
	b, err := os.ReadFile("testdata/wiki-3-oktober.json")
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Parse struct {
			Wikitext string `json:"wikitext"`
		} `json:"parse"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	all := parseDayEvents(r.Parse.Wikitext)
	if len(all) < 40 {
		t.Fatalf("events: %d", len(all))
	}
	for _, e := range all {
		if strings.ContainsAny(e.Text, "[]{}<>|") || strings.Contains(e.Text, "''") || e.Year < 1 || e.Year > 2100 {
			t.Errorf("markup left or bad year: %+v", e)
		}
	}
	if e := all[0]; e.Year != 382 || !strings.HasPrefix(e.Text, "Keizer Theodosius I sluit een verbond met de Visigoten") || !strings.Contains(e.Text, "Balkangebergte") {
		t.Errorf("first event (links with labels resolved): %+v", e)
	}
	pick := pickDayEvents(all, 4)
	dutch := 0
	for i, e := range pick {
		if dutchTopicRe.MatchString(e.Text) {
			dutch++
		}
		if i > 0 && e.Year < pick[i-1].Year {
			t.Errorf("picked events oldest first: %+v", pick)
		}
	}
	if len(pick) != 4 || dutch < 1 || dutch > 2 {
		t.Errorf("pick: %d events, %d Dutch: %+v", len(pick), dutch, pick)
	}
	if got := cleanWikitext(`De [[Elfstedentocht|tocht]] {{nowrap|x}} begint<ref name="a">bron</ref> in '''[[Leeuwarden]]'''.`); got != "De tocht begint in Leeuwarden." {
		t.Errorf("clean: %q", got)
	}
	if dayPageTitle(time.Date(2026, 10, 3, 9, 0, 0, 0, amsterdam)) != "3 oktober" || dayPageTitle(time.Date(2026, 1, 21, 9, 0, 0, 0, amsterdam)) != "21 januari" {
		t.Error("page title")
	}
	if parseDayEvents("== Geboren ==\n* [[1900]] – Iemand") != nil {
		t.Error("no Gebeurtenissen section: no events")
	}
	a := newTestApp(t, validConfig)
	today := time.Now().In(amsterdam).Format("2006-01-02")
	a.threats.ok("wiki:onthisday", OnThisDay{Date: today, Title: "3 oktober", URL: "https://nl.wikipedia.org/wiki/3_oktober", Events: pick}, "", "")
	if body := get(a.routes("/"), "GET", "/api/today", nil).Body.String(); !strings.Contains(body, `"on_this_day":{`) || !strings.Contains(body, `"events":[{"year":`) {
		t.Errorf("today handler: %s", body[:min(len(body), 300)])
	}
	a.threats.ok("wiki:onthisday", OnThisDay{Date: "2000-01-01", Title: "1 januari", Events: pick}, "", "")
	if body := get(a.routes("/"), "GET", "/api/today", nil).Body.String(); strings.Contains(body, `"events":[`) {
		t.Error("yesterday's events must not be served")
	}
}

func TestTidesAndSea(t *testing.T) {
	b, err := os.ReadFile("testdata/rws-tides.json")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := parseTides(b)
	if err != nil || len(ts) < 6 {
		t.Fatalf("tides: %v %d", err, len(ts))
	}
	if ts[0].High || ts[0].CM != -59 || !ts[1].High || ts[1].CM != 95 || !ts[0].Time.Equal(time.Date(2026, 10, 3, 1, 14, 0, 0, time.UTC)) {
		t.Errorf("first tides (low -59 at 02:14+01:00, high 95): %+v", ts[:2])
	}
	for i := 1; i < len(ts); i++ {
		if ts[i].High == ts[i-1].High {
			t.Errorf("high and low alternate: %+v", ts)
			break
		}
	}
	if _, err := parseTides([]byte(`{"Succesvol":false,"Foutmelding":"Geen gegevens gevonden!"}`)); err == nil || !strings.Contains(err.Error(), "Geen gegevens") {
		t.Errorf("error reply: %v", err)
	}
	m, err := os.ReadFile("testdata/openmeteo-marine.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseMarine(m)
	if err != nil || s.Temp == nil || s.Wave == nil || *s.Temp < 0 || *s.Temp > 30 || s.WaveMax == nil {
		t.Errorf("sea: %v %+v", err, s)
	}
	if _, err := parseMarine([]byte(`{"current":{"wave_height":null}}`)); err == nil {
		t.Error("no values must be an error")
	}
	for _, c := range []struct {
		lat, lon float64
		want     string
	}{{52.08, 4.31, "Scheveningen"}, {51.44, 3.57, "Vlissingen"}, {53.18, 5.42, "Harlingen"}, {52.09, 5.12, "IJmuiden"}, {53.22, 6.57, "Delfzijl"}} {
		if st, _ := nearestCoast(c.lat, c.lon); st.Name != c.want {
			t.Errorf("nearest to %.2f,%.2f: %s, want %s", c.lat, c.lon, st.Name, c.want)
		}
	}
	a := newTestApp(t, validConfig)
	a.tides.get("scheveningen", time.Hour, func() ([]Tide, error) {
		now := time.Now().UTC()
		return []Tide{{now.Add(-3 * time.Hour), false, -50}, {now.Add(2 * time.Hour), true, 110}, {now.Add(8 * time.Hour), false, -60}, {now.Add(14 * time.Hour), true, 100}, {now.Add(20 * time.Hour), false, -55}}, nil
	})
	a.sea.get("scheveningen", time.Hour, func() (SeaNow, error) { return s, nil })
	body := get(a.routes("/"), "GET", "/api/sea?lat=52.08&lon=4.31", nil).Body.String()
	if !strings.Contains(body, `"name":"Scheveningen"`) || strings.Contains(body, `"cm":-50`) || !strings.Contains(body, `"cm":110`) || !strings.Contains(body, `"temp":`) {
		t.Errorf("sea handler (past tides dropped): %s", body)
	}
	if code := get(a.routes("/"), "GET", "/api/sea?lat=x", nil).Code; code != 400 {
		t.Errorf("bad lat: %d", code)
	}
}

func TestExploitsAndEPSS(t *testing.T) {
	b, err := os.ReadFile("testdata/exploitdb.xml")
	if err != nil {
		t.Fatal(err)
	}
	ex, err := parseExploitDB(b, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC))
	if err != nil || len(ex) < 5 || ex[0].Kind != "remote" || !strings.HasPrefix(ex[0].Title, "Teltonika_RutOS") || ex[0].ID != "52692" {
		t.Fatalf("exploits: %v %+v", err, ex[:min(2, len(ex))])
	}
	gz := func(date string, rows ...string) []byte {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		fmt.Fprintf(zw, "#model_version:v2025.03.14,score_date:%sT00:00:00+0000\ncve,epss,percentile\n", date)
		for i := 0; i < 1200; i++ { // filler: a real file has ~380,000 rows
			fmt.Fprintf(zw, "CVE-2000-%04d,0.00100,0.10000\n", i)
		}
		for _, r := range rows {
			fmt.Fprintln(zw, r)
		}
		zw.Close()
		return buf.Bytes()
	}
	older := gz("2026-09-26", "CVE-2026-1000,0.01000,0.50000", "CVE-2026-2000,0.40000,0.90000", "CVE-2026-3000,0.05000,0.70000")
	newer := gz("2026-10-03", "CVE-2026-1000,0.62000,0.99000", "CVE-2026-2000,0.42000,0.95000", "CVE-2026-3000,0.30000,0.96000", "CVE-2026-4000,0.15000,0.93000")
	d, err := epssRisers(older, newer, 10)
	if err != nil || d.Date != "2026-10-03" || d.Since != "2026-09-26" {
		t.Fatalf("epss: %v %+v", err, d)
	}
	got := []string{}
	for _, r := range d.Risers {
		got = append(got, r.CVE)
	}
	// 1000 rose 61 points, 3000 rose 25, 4000 is new (15); 2000 rose only 2 points
	if strings.Join(got, ",") != "CVE-2026-1000,CVE-2026-3000,CVE-2026-4000" || d.Risers[0].Prev != float64(float32(0.01)) {
		t.Errorf("risers: %v %+v", got, d.Risers)
	}
	if _, err := epssRisers([]byte("not gzip"), newer, 10); err == nil {
		t.Error("a broken file must be an error")
	}
	a := newTestApp(t, validConfig)
	a.threats.ok("exploitdb", ex, "", "")
	a.threats.ok("epss:risers", d, "", "")
	a.threats.ok("cisa:kev", []KEVItem{{CVE: "CVE-2026-3000"}}, "", "")
	body := get(a.routes("/"), "GET", "/api/exploits", nil).Body.String()
	if !strings.Contains(body, `"risers":[{"cve":"CVE-2026-1000"`) || !strings.Contains(body, `"kev":["CVE-2026-3000"]`) || !strings.Contains(body, `"kind":"remote"`) {
		t.Errorf("exploits handler: %s", body[:min(len(body), 400)])
	}
}

func TestVendorSeverity(t *testing.T) {
	for in, want := range map[string]string{
		"Cisco IOS XE … Security Impact Rating:  Critical …":                  "critical",
		"CVE-2026-0307 GlobalProtect App: Local Privilege (Severity: MEDIUM)": "medium",
		"CVSSv3 Score: 9.8 An Improper Limitation of a Pathname":              "critical",
		"CVSSv3 Score: 7.2 Heap overflow":                                     "high",
		"CVSS Base Score: 4.7":                                                "medium",
		"September 2026 Security Update":                                      "",
	} {
		if got := explicitSeverity(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	a := newTestApp(t, validConfig+`
advisories:
  - { id: ncsc, name: "NCSC-NL", format: ncsc, url: "https://advisories.ncsc.nl/rss/advisories" }
  - { id: cisco, name: "Cisco", format: rss, group: edge, url: "https://sec.cloudapps.cisco.com/x.xml" }
`)
	now := time.Now()
	var many []Advisory
	for i := 0; i < 40; i++ {
		many = append(many, Advisory{ID: fmt.Sprint("c", i), Source: "cisco", Title: "x", URL: "https://x", Published: now.Add(-time.Duration(i) * time.Minute), Severity: "high", CVEs: []string{}, Products: []string{}})
	}
	a.threats.ok("adv:cisco", many, "", "")
	a.threats.ok("adv:ncsc", []Advisory{{ID: "NCSC-1", Source: "ncsc", Title: "y", URL: "https://y", Published: now.Add(-48 * time.Hour), Severity: "medium", CVEs: []string{}, Products: []string{}}}, "", "")
	body := get(a.routes("/"), "GET", "/api/advisories?limit=30", nil).Body.String()
	if !strings.Contains(body, `"NCSC-1"`) || strings.Count(body, `"source":"cisco"`) != 8 || !strings.Contains(body, `"groups":{"cisco":"edge"}`) {
		t.Errorf("advisories: an older NCSC advisory survives 40 vendor items, at most 8 per vendor; groups listed: %s", body[len(body)-200:])
	}
}

func TestThreatIntel(t *testing.T) {
	if got := defang("http://176.65.148.144/bins/Hilix.ppc"); got != "hxxp://176.65.148.144/bins/Hilix[.]ppc" {
		t.Errorf("defang url: %q", got)
	}
	if got := defang("petroazaran.com"); got != "petroazaran[.]com" {
		t.Errorf("defang domain: %q", got)
	}
	b, err := os.ReadFile("testdata/urlhaus-nl.csv")
	if err != nil {
		t.Fatal(err)
	}
	u, err := parseURLhausNL(b, time.Date(2026, 10, 4, 14, 0, 0, 0, time.UTC))
	if err != nil || u.Online != 120 || len(u.ASNs) == 0 || len(u.Newest) != 5 || !strings.HasPrefix(u.Newest[0].URL, "hxxp") || u.Threats["malware_download"] == 0 {
		t.Fatalf("urlhaus: %v online=%d asns=%+v newest=%+v", err, u.Online, u.ASNs, u.Newest[:min(1, len(u.Newest))])
	}
	if _, err := parseURLhausNL([]byte("# empty\n"), time.Now()); err == nil {
		t.Error("no rows must be an error")
	}
	f, err := os.ReadFile("testdata/threatfox-iocs.json")
	if err != nil {
		t.Fatal(err)
	}
	tf, err := parseThreatFox(f)
	if err != nil || tf.Total != 40 || len(tf.Families) == 0 || tf.Families[0].Count < tf.Families[len(tf.Families)-1].Count {
		t.Fatalf("threatfox: %v %+v", err, tf)
	}
	for _, i := range tf.Newest {
		if strings.Contains(i.IOC, "http://") || i.Confidence < 75 || (!strings.Contains(i.Type, "hash") && !strings.Contains(i.IOC, "[.]") && !strings.Contains(i.IOC, "hxxp")) { // hashes have nothing to defang
			t.Errorf("ioc not defanged or low confidence: %+v", i)
		}
	}
	if _, err := parseThreatFox([]byte(`{"query_status":"unknown_auth_key"}`)); err == nil || !strings.Contains(err.Error(), "unknown_auth_key") {
		t.Errorf("auth error: %v", err)
	}
	if fam, _ := parseThreatFox([]byte(`{"query_status":"ok","data":[{"ioc":"1.2.3.4:443","malware_printable":"Emotet","threat_type":"botnet_cc","confidence_level":100}]}`)); len(fam.Families) != 0 {
		t.Errorf("families already in Feodo Tracker are left out: %+v", fam.Families)
	}
	a := newTestApp(t, validConfig)
	a.threats.ok("urlhaus:nl", u, "", "")
	if body := get(a.routes("/"), "GET", "/api/threats", nil).Body.String(); !strings.Contains(body, `"urlhaus":{`) || !strings.Contains(body, `"online":120`) || !strings.Contains(body, `"threatfox":{`) || !strings.Contains(body, `"missing_key":true`) {
		t.Errorf("threats handler: %s", body[:min(len(body), 300)])
	}
}

func TestRadarNL(t *testing.T) {
	sh := radarShares(map[string]string{"SYN Flood": "62.19", "UDP Flood": "23.9", "other": "0.6", "ACK Flood": "0.7"}, 2)
	if len(sh) != 2 || sh[0].Name != "SYN Flood" || sh[1].Name != "UDP Flood" {
		t.Errorf("shares: %+v", sh)
	}
	if !radarTime("2026-10-04T10:00:35.809").Equal(time.Date(2026, 10, 4, 10, 0, 35, 809e6, time.UTC)) || radarTime("x") != (time.Time{}) {
		t.Error("radar time")
	}
	a := newTestApp(t, validConfig)
	if body := get(a.routes("/"), "GET", "/api/nlthreat", nil).Body.String(); !strings.Contains(body, `"missing_key":true`) {
		t.Errorf("no token: %s", body)
	}
	cfg := *a.config()
	cfg.Keys.CloudflareRadarToken = "test"
	a.mu.Lock()
	a.cfg = &cfg
	a.mu.Unlock()
	a.threats.ok("radar:nl", RadarNL{Trend: []float64{0.2, 1}, Vectors: sh, Hijacks: []BGPEvent{{ASN: 60781, Score: 8}}, HijacksN: 1, ASNames: map[int]string{60781: "LeaseWeb Netherlands B.V."}}, "", "")
	if body := get(a.routes("/"), "GET", "/api/nlthreat", nil).Body.String(); !strings.Contains(body, `"hijacks":[{"at":`) || !strings.Contains(body, "LeaseWeb") || strings.Contains(body, "test") {
		t.Errorf("radar handler (token never in the reply): %s", body)
	}
}

func TestGHSAAndFortinet(t *testing.T) {
	b, err := os.ReadFile("testdata/github-advisories-opnsense.json")
	if err != nil {
		t.Fatal(err)
	}
	src := AdvisorySource{ID: "opnsense", Name: "OPNsense", Format: "ghsa", Group: "edge"}
	as, err := parseGHSA(src, b)
	if err != nil || len(as) < 5 || as[0].ID != "GHSA-x3q7-cc7h-gj3v" || as[0].Severity != "medium" || !strings.HasPrefix(as[0].URL, "https://github.com/opnsense/core/security/advisories/") || as[0].Source != "opnsense" {
		t.Fatalf("ghsa: %v %+v", err, as[:min(1, len(as))])
	}
	if _, err := parseGHSA(src, []byte(`{"message":"API rate limit exceeded"}`)); err == nil {
		t.Error("an error object (rate limit) must be an error")
	}
	if l, _ := parseGHSA(src, []byte(`[{"ghsa_id":"GHSA-x","html_url":"https://github.com/x","state":"draft","published_at":null}]`)); len(l) != 0 {
		t.Errorf("drafts are skipped: %+v", l)
	}
	forti := AdvisorySource{ID: "fortinet", URL: "https://filestore.fortinet.com/fortiguard/rss/ir.xml"}
	if p := fortiProduct(forti, "Arbitrary process termination", "An Unverified Ownership Vulnerability [CWE-283] in FortiClient Windows fortimon3 driver may allow"); p != "FortiClient Windows" {
		t.Errorf("product: %q", p)
	}
	if p := fortiProduct(forti, "FortiOS - heap overflow", "in FortiOS"); p != "" {
		t.Error("a title that already names the product is left alone")
	}
	if p := fortiProduct(AdvisorySource{URL: "https://example.org/rss"}, "x", "FortiGate"); p != "" {
		t.Error("only for Fortinet feeds")
	}
	raws := []rawItem{{Title: "Improper Authentication", Link: "https://www.fortiguard.com/psirt/FG-IR-26-001", Summary: "CVSSv3 Score: 9.1 An improper authentication in FortiPAM Server may allow", Date: "Tue, 08 Sep 2026 00:00:00 +0000"}}
	if a := normalizeAdvisories(forti, raws, time.Now()); len(a) != 1 || a[0].Title != "FortiPAM: Improper Authentication" || a[0].Severity != "critical" {
		t.Errorf("fortinet advisory: %+v", a)
	}
}

func TestPageID(t *testing.T) {
	a := newTestApp(t, validConfig)
	page := get(a.routes("/"), "GET", "/", nil)
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(pageID) || !strings.Contains(page.Body.String(), `<meta name="ndb-page" content="`+pageID+`">`) || strings.Contains(page.Body.String(), "__PAGE_ID__") {
		t.Fatalf("page must carry the build id %q", pageID)
	}
	if rr := get(a.routes("/"), "GET", "/api/today", nil); rr.Header().Get("X-NDB-Page") != pageID || !strings.Contains(rr.Header().Get("Vary"), "X-NDB-Page") {
		t.Errorf("API header %q, Vary %q", rr.Header().Get("X-NDB-Page"), rr.Header().Get("Vary"))
	}
	if got := get(a.routes("/"), "GET", "/api/sea?lat=x", nil).Header().Get("X-NDB-Page"); got != pageID {
		t.Errorf("errors carry the header too: %q", got)
	}
	if buildCSP(indexHTML) != buildCSP([]byte(page.Body.String())) {
		t.Error("the stamped page must keep the same script hashes (CSP)")
	}
}
