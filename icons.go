package main

import (
	"bytes"
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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Source icons: a small icon per news site instead of a coloured dot. The
// server finds the sharpest icon the site offers (apple-touch-icon, a declared
// PNG icon, then /favicon.ico), turns it into a 32×32 PNG with transparency and
// serves it itself, so visitors never contact the news sites. SVG icons are
// skipped (they can contain scripts). Fetched in the background after start and
// refreshed weekly; a site without a usable icon keeps the coloured dot.
// Some sites show automated visitors only a cookie wall or block them (NU.nl, RTL,
// De Telegraaf, De Tijd); then, if features.icon_services is on, the server asks
// DuckDuckGo's and then Google's favicon service, sending only the site's name.
// A service that does not know the site answers 404 (with a placeholder), which is
// treated as "no icon".
// Fetches go through the image proxy's client, which refuses private addresses.

const (
	iconSize    = 32
	iconRefresh = 7 * 24 * time.Hour
	iconRetry   = time.Hour // after a failure (often a slow site right after start)
)

type iconEntry struct {
	png []byte // nil: no usable icon
	at  time.Time
}

type iconCache struct {
	mu    sync.RWMutex
	m     map[string]iconEntry // host -> icon
	dirty bool                 // changed since the last write to disk
}

func newIconCache() *iconCache { return &iconCache{m: map[string]iconEntry{}} }

func (c *iconCache) get(host string) (iconEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[host]
	return e, ok
}

func (c *iconCache) set(host string, png []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[host] = iconEntry{png: png, at: time.Now()}
	c.dirty = true
}

// ---------------------------------------------------------------------------
// Persistence, so the icons are there right after a restart: cache.icon_cache_path,
// or <snapshot_path>.icons.json when only the news snapshot is on. Only good icons
// are kept, with the time they were fetched, so the weekly refresh simply
// continues; the file (~85 KB) is written only when the icons changed. The icon
// cache alone never writes the news to disk.

type iconSnapshot struct {
	Version int                    `json:"version"`
	Icons   map[string]iconSnapEnt `json:"icons"`
}

type iconSnapEnt struct {
	PNG []byte    `json:"png"` // base64 in the JSON
	At  time.Time `json:"at"`
}

func iconsPathFor(cfg *Config) string {
	switch {
	case cfg.Cache.IconCachePath != "":
		return cfg.Cache.IconCachePath
	case cfg.Cache.SnapshotPath != "":
		return cfg.Cache.SnapshotPath + ".icons.json"
	}
	return ""
}

func (a *App) iconsPath() string { return iconsPathFor(a.config()) }

// checkWritable tells at startup when a folder for the snapshot or the icon cache
// cannot be written, instead of failing quietly on the first write later. In
// Docker the app runs as uid 65532 (distroless "nonroot").
func checkWritable(cfg *Config) {
	from := func(env string) string { // where a path setting comes from: an environment variable wins over config.yaml
		if os.Getenv(env) != "" {
			return env
		}
		return "config.yaml"
	}
	switch {
	case cfg.Cache.SnapshotPath != "":
		slog.Info("disk writes: news snapshot every 30 min and on stop, push subscriptions and icons",
			"snapshot", cfg.Cache.SnapshotPath, "snapshot_from", from("NDB_SNAPSHOT_PATH"), "icons", iconsPathFor(cfg))
	case cfg.Cache.IconCachePath != "":
		slog.Info("disk writes: only the site icons (no news snapshot)", "icons", cfg.Cache.IconCachePath, "icons_from", from("NDB_ICON_CACHE_PATH"))
	}
	for _, p := range []string{cfg.Cache.SnapshotPath, iconsPathFor(cfg)} {
		if p == "" {
			continue
		}
		if err := dirWritable(filepath.Dir(p)); err != nil {
			slog.Error("folder not writable: nothing will be stored there", "path", p, "uid", os.Getuid(), "err", err,
				"fix", fmt.Sprintf("make the folder writable for uid %d, e.g. on the host: sudo chown %d:%d <folder>", os.Getuid(), os.Getuid(), os.Getgid()))
		}
	}
}

func dirWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// saveIcons writes the icons when they changed (atomically: temporary file, then rename).
func (a *App) saveIcons() error {
	path := a.iconsPath()
	c := a.icons
	c.mu.Lock()
	if path == "" || !c.dirty {
		c.mu.Unlock()
		return nil
	}
	snap := iconSnapshot{Version: 1, Icons: map[string]iconSnapEnt{}}
	for host, e := range c.m {
		if e.png != nil {
			snap.Icons[host] = iconSnapEnt{PNG: e.png, At: e.at.UTC()}
		}
	}
	c.dirty = false
	c.mu.Unlock()
	b, err := json.Marshal(snap)
	if err == nil {
		err = writeFileAtomic(path, b, 0o644)
	}
	if err != nil {
		c.mu.Lock()
		c.dirty = true
		c.mu.Unlock()
	}
	return err
}

// loadIcons restores the icons from disk (path: <snapshot>.icons.json); anything
// that is not a PNG or claims a future fetch time is skipped.
func (a *App) loadIcons(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var snap iconSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return 0, err
	}
	n := 0
	c := a.icons
	c.mu.Lock()
	defer c.mu.Unlock()
	for host, e := range snap.Icons {
		if !bytes.HasPrefix(e.PNG, []byte("\x89PNG\r\n\x1a\n")) || len(e.PNG) > 64<<10 || e.At.After(time.Now().Add(time.Hour)) || host == "" {
			continue
		}
		c.m[host] = iconEntry{png: e.PNG, at: e.At}
		n++
	}
	return n, nil
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// iconSite: the site of a source, from its homepage (else its feed address).
func iconSite(s Source) (*url.URL, bool) {
	for _, raw := range []string{s.Homepage, s.URL} {
		if u, err := url.Parse(raw); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}, true
		}
	}
	return nil, false
}

var (
	iconLinkRe = regexp.MustCompile(`(?i)<link\b[^>]*>`)
	iconAttrRe = regexp.MustCompile(`(?i)\b(rel|href|type|sizes)\s*=\s*("([^"]*)"|'([^']*)')`)
)

// baseDomain: the last two labels of a host name ("www.nu.nl" -> "nu.nl"); good
// enough to tell a site's own pages from a consent page elsewhere.
func baseDomain(host string) string {
	parts := strings.Split(strings.TrimSuffix(strings.ToLower(host), "."), ".")
	if len(parts) <= 2 {
		return strings.Join(parts, ".")
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// iconCandidates lists the icon URLs from a homepage, best first: apple-touch-icon,
// then declared PNG icons (largest first), then other raster icons; SVG is skipped.
func iconCandidates(page *url.URL, html string) []string {
	type cand struct {
		u    string
		rank int
	}
	var list []cand
	for _, tag := range iconLinkRe.FindAllString(html, 200) {
		at := map[string]string{}
		for _, m := range iconAttrRe.FindAllStringSubmatch(tag, -1) {
			at[strings.ToLower(m[1])] = m[3] + m[4]
		}
		rel := strings.ToLower(at["rel"])
		if !strings.Contains(rel, "icon") || at["href"] == "" || strings.Contains(rel, "mask") {
			continue
		}
		ref, err := url.Parse(strings.TrimSpace(at["href"]))
		if err != nil {
			continue
		}
		u := page.ResolveReference(ref)
		if u.Scheme != "https" && u.Scheme != "http" {
			continue
		}
		ext := strings.ToLower(u.Path[strings.LastIndex(u.Path, ".")+1:])
		typ := strings.ToLower(at["type"])
		if ext == "svg" || strings.Contains(typ, "svg") {
			continue
		}
		size := 0
		fmt.Sscanf(strings.ToLower(at["sizes"]), "%dx", &size)
		rank := 100 + min(size, 256) // other raster icons
		switch {
		case strings.Contains(rel, "apple-touch-icon"):
			rank = 1000
		case ext == "png" || strings.Contains(typ, "png"):
			rank = 500 + min(size, 256)
		}
		list = append(list, cand{u.String(), rank})
	}
	for i := 1; i < len(list); i++ { // stable insertion sort, best first
		for j := i; j > 0 && list[j].rank > list[j-1].rank; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, c := range list {
		if !seen[c.u] && len(out) < 4 {
			seen[c.u] = true
			out = append(out, c.u)
		}
	}
	return append(out, page.ResolveReference(&url.URL{Path: "/favicon.ico"}).String())
}

// decodeIcon reads PNG, JPEG, GIF or ICO (with PNG or BMP entries).
func decodeIcon(b []byte) (image.Image, error) {
	if len(b) >= 6 && binary.LittleEndian.Uint16(b[0:]) == 0 && binary.LittleEndian.Uint16(b[2:]) == 1 {
		return decodeICO(b)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > 4096*4096 {
		return nil, errors.New("icon dimensions out of range")
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}

// decodeICO picks the largest entry (up to 256 px) and decodes it: embedded PNG, or
// a BMP bitmap of 32, 24, 8, 4 or 1 bits per pixel with its transparency mask.
func decodeICO(b []byte) (image.Image, error) {
	n := int(binary.LittleEndian.Uint16(b[4:]))
	if n == 0 || n > 64 || len(b) < 6+16*n {
		return nil, errors.New("ico: bad directory")
	}
	best, bestSize := -1, -1
	for i := 0; i < n; i++ {
		e := b[6+16*i:]
		w := int(e[0])
		if w == 0 {
			w = 256
		}
		if w > bestSize {
			best, bestSize = i, w
		}
	}
	e := b[6+16*best:]
	size, off := int(binary.LittleEndian.Uint32(e[8:])), int(binary.LittleEndian.Uint32(e[12:]))
	if off < 0 || size <= 0 || off+size > len(b) || off+size < off {
		return nil, errors.New("ico: entry out of range")
	}
	data := b[off : off+size]
	if bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return png.Decode(bytes.NewReader(data))
	}
	return decodeDIB(data)
}

func decodeDIB(d []byte) (image.Image, error) {
	if len(d) < 40 {
		return nil, errors.New("ico: short bitmap")
	}
	hdr := int(binary.LittleEndian.Uint32(d[0:]))
	w, h2 := int(int32(binary.LittleEndian.Uint32(d[4:]))), int(int32(binary.LittleEndian.Uint32(d[8:])))
	bpp := int(binary.LittleEndian.Uint16(d[14:]))
	colors := int(binary.LittleEndian.Uint32(d[32:]))
	h := h2 / 2 // the height covers the colour bitmap and the AND mask
	if hdr < 40 || w <= 0 || w > 256 || h <= 0 || h > 256 || binary.LittleEndian.Uint32(d[16:]) != 0 {
		return nil, errors.New("ico: unsupported bitmap")
	}
	var palette []color.NRGBA
	if bpp <= 8 {
		if colors == 0 {
			colors = 1 << bpp
		}
		p := hdr
		if colors > 256 || p+4*colors > len(d) {
			return nil, errors.New("ico: bad palette")
		}
		for i := 0; i < colors; i++ {
			q := d[p+4*i:]
			palette = append(palette, color.NRGBA{q[2], q[1], q[0], 255})
		}
	} else if bpp != 24 && bpp != 32 {
		return nil, fmt.Errorf("ico: %d bits per pixel", bpp)
	}
	stride := ((w*bpp + 31) / 32) * 4
	maskStride := ((w + 31) / 32) * 4
	pix := hdr + 4*len(palette)
	mask := pix + stride*h
	if mask > len(d) {
		return nil, errors.New("ico: short pixel data")
	}
	hasMask := mask+maskStride*h <= len(d)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	for y := 0; y < h; y++ {
		row := d[pix+(h-1-y)*stride:]
		for x := 0; x < w; x++ {
			var c color.NRGBA
			switch bpp {
			case 32:
				c = color.NRGBA{row[4*x+2], row[4*x+1], row[4*x], row[4*x+3]}
				if c.A != 0 {
					anyAlpha = true
				}
			case 24:
				c = color.NRGBA{row[3*x+2], row[3*x+1], row[3*x], 255}
			default:
				bit := x * bpp
				v := int(row[bit/8]>>(8-bpp-bit%8)) & (1<<bpp - 1)
				if v < len(palette) {
					c = palette[v]
				}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	for y := 0; y < h && hasMask; y++ { // AND mask: 1 = transparent (32-bit icons with alpha use that instead)
		row := d[mask+(h-1-y)*maskStride:]
		for x := 0; x < w; x++ {
			if row[x/8]>>(7-x%8)&1 == 1 && (bpp != 32 || !anyAlpha) {
				img.SetNRGBA(x, y, color.NRGBA{})
			}
		}
	}
	if bpp == 32 && !anyAlpha { // old 32-bit icons leave alpha at 0 and rely on the mask
		for i := 3; i < len(img.Pix); i += 4 {
			if img.Pix[i-3]|img.Pix[i-2]|img.Pix[i-1] != 0 || !hasMask {
				img.Pix[i] = 255
			}
		}
	}
	return img, nil
}

// iconPNG scales an icon to 32×32 (area average with alpha, centred if not square).
func iconPNG(src image.Image) ([]byte, error) {
	b := src.Bounds()
	if b.Dx() < 12 || b.Dy() < 12 {
		return nil, errors.New("icon too small")
	}
	side := max(b.Dx(), b.Dy())
	dst := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	f := float64(side) / iconSize
	ox, oy := float64(side-b.Dx())/2, float64(side-b.Dy())/2
	visible := 0
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			var r, g, bl, a, n float64
			x0, y0 := float64(x)*f-ox, float64(y)*f-oy
			steps := max(1, min(8, int(f)))
			for j := 0; j < steps; j++ {
				for i := 0; i < steps; i++ {
					px := b.Min.X + int(x0+(float64(i)+0.5)*f/float64(steps))
					py := b.Min.Y + int(y0+(float64(j)+0.5)*f/float64(steps))
					n++
					if px < b.Min.X || py < b.Min.Y || px >= b.Max.X || py >= b.Max.Y {
						continue
					}
					cr, cg, cb, ca := src.At(px, py).RGBA() // premultiplied
					r, g, bl, a = r+float64(cr), g+float64(cg), bl+float64(cb), a+float64(ca)
				}
			}
			if a == 0 {
				continue
			}
			visible++
			dst.SetNRGBA(x, y, color.NRGBA{uint8(r / a * 255), uint8(g / a * 255), uint8(bl / a * 255), uint8(a / n / 257)})
		}
	}
	if visible < iconSize*iconSize/20 {
		return nil, errors.New("icon (almost) empty")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// iconServices: the favicon services asked when a site blocks the server; only the
// site's name is sent. Google redirects to one of several image servers
// (t0-t3.gstatic.com) at random; if a DNS filter blocks the one it picked, a next
// request usually lands on another, so Google is asked up to four times.
func iconServices(host string) []string {
	h := url.QueryEscape(host)
	g := "https://www.google.com/s2/favicons?sz=64&domain=" + h
	return []string{"https://icons.duckduckgo.com/ip3/" + h + ".ico", g, g + "&retry=1", g + "&retry=2", g + "&retry=3"}
}

// fetchIcon tries the candidates of one site until one decodes.
func (a *App) fetchIcon(ctx context.Context, site *url.URL) ([]byte, error) {
	get := func(u string, limit int64, accept string) ([]byte, *url.URL, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("User-Agent", a.images.ua())
		req.Header.Set("Accept", accept)
		resp, err := a.images.client.Do(req)
		if err != nil {
			return nil, nil, shortErr(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		return body, resp.Request.URL, err
	}
	// The site's own standard files; a homepage on another domain is usually a cookie
	// wall (e.g. DPG Media's consent page), whose icon is not the site's.
	cands := []string{site.ResolveReference(&url.URL{Path: "/apple-touch-icon.png"}).String(), site.ResolveReference(&url.URL{Path: "/favicon.ico"}).String()}
	if page, final, err := get(site.String(), 512<<10, "text/html"); err == nil && baseDomain(final.Hostname()) == baseDomain(site.Hostname()) {
		cands = iconCandidates(final, string(page))
	}
	if a.config().Features.IconServices {
		cands = append(cands, iconServices(site.Hostname())...)
	}
	var last error = errors.New("no icon found")
	for _, u := range cands {
		body, _, err := get(u, 1<<20, "image/png,image/x-icon,image/*;q=0.8")
		if err != nil {
			last = err
			continue
		}
		img, err := decodeIcon(body)
		if err != nil {
			last = err
			continue
		}
		out, err := iconPNG(img)
		if err != nil {
			last = err
			continue
		}
		return out, nil
	}
	return nil, last
}

// iconJob fetches the icons that are missing or due, a few sites at a time.
func (a *App) iconJob(ctx context.Context) error {
	if wait := time.Minute - time.Since(a.started); wait > 0 { // the feeds go first after a start
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	cfg := a.config()
	sites := map[string]*url.URL{}
	for _, s := range cfg.Sources {
		if u, ok := iconSite(s); ok && s.IsEnabled() {
			sites[u.Host] = u
		}
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for host, site := range sites {
		if e, ok := a.icons.get(host); ok && ((e.png != nil && time.Since(e.at) < iconRefresh) || (e.png == nil && time.Since(e.at) < iconRetry)) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(host string, site *url.URL) {
			defer func() { <-sem; wg.Done() }()
			fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			out, err := a.fetchIcon(fctx, site)
			if err != nil {
				slog.Debug("no icon", "site", host, "err", err)
			}
			if old, ok := a.icons.get(host); err != nil && ok && old.png != nil {
				return // keep the last good icon
			}
			a.icons.set(host, out)
		}(host, site)
	}
	wg.Wait()
	if err := a.saveIcons(); err != nil {
		slog.Error("icon cache write failed", "path", a.iconsPath(), "err", err)
	}
	return nil
}

// iconURL: the page's link to a source's icon ("" when there is none yet); the
// time of the fetch makes the link change when the icon does.
func (a *App) iconURL(s Source) string {
	site, ok := iconSite(s)
	if !ok {
		return ""
	}
	if e, has := a.icons.get(site.Host); has && e.png != nil {
		return fmt.Sprintf("api/icon?s=%s&v=%d", url.QueryEscape(s.ID), e.at.Unix())
	}
	return ""
}

// handleIcon serves the icon of a configured source: GET /api/icon?s=<source id>.
func (a *App) handleIcon(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	s, ok := cfg.sourceByID(r.URL.Query().Get("s"))
	if !cfg.Features.SourceIcons || !ok {
		http.NotFound(w, r)
		return
	}
	site, ok := iconSite(s)
	if !ok {
		http.NotFound(w, r)
		return
	}
	e, has := a.icons.get(site.Host)
	if !has || e.png == nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	w.Write(e.png)
}
