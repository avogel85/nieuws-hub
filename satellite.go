package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Satellietbeeld: the latest Meteosat image of the Benelux and surroundings from
// the EUMETSAT view service (GeoServer WMS, open, no key). Default layer: MTG
// GeoColour, real colour by day and clouds with city lights at night; a new
// image every 10 minutes, published ~25-30 minutes after the scan. The server
// fetches it and serves it itself, so visitors never contact EUMETSAT.
// Coastlines and borders come as a separate transparent PNG: the WMS drops
// untimed overlay layers from a request with a time parameter.
// EUMETSAT renders a new image on the first request, which can take a minute
// (later requests are served from its cache in seconds), hence satTimeout.

const (
	satKey     = "eumetsat:satellite"
	satTimeout = 60 * time.Second
)

// Web Mercator (EPSG:3857) box around the Benelux: lon -4..14, lat 48.5..56.5.
const (
	satBBox          = "-445277,6190443,1558472,7658602"
	satW, satH       = 800, 587
	satOverlayLayers = "backgrounds:ne_10m_coastline,osmgray:ne_10m_admin_0_boundary_lines_land"
)

var (
	satLayerRe = regexp.MustCompile(`^[A-Za-z0-9_]+:[A-Za-z0-9_]+$`)
	satTimeRe  = regexp.MustCompile(`<Dimension[^>]*name="time"[^>]*default="([^"]+)"`)
)

type SatImage struct {
	Time      time.Time // scan time of the image
	Img       []byte    // JPEG
	Overlay   []byte    // PNG, coastlines and borders; nil when unavailable
	OverlayAt time.Time
}

func isJPEG(b []byte) bool { return len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF }
func isPNG(b []byte) bool  { return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) }

// parseSatTime reads the newest image time from a per-layer WMS GetCapabilities.
func parseSatTime(body []byte) (time.Time, error) {
	m := satTimeRe.FindSubmatch(body)
	if m == nil {
		return time.Time{}, errors.New("no time dimension in capabilities")
	}
	t, err := time.Parse(time.RFC3339, string(m[1]))
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q: %w", m[1], err)
	}
	return t.UTC(), nil
}

func satMapURL(base, layers, format, tm string) string {
	q := url.Values{"service": {"WMS"}, "version": {"1.3.0"}, "request": {"GetMap"}, "layers": {layers},
		"styles": {strings.Repeat(",", strings.Count(layers, ","))}, "crs": {"EPSG:3857"}, "bbox": {satBBox},
		"width": {strconv.Itoa(satW)}, "height": {strconv.Itoa(satH)}, "format": {format}}
	if tm != "" {
		q.Set("time", tm)
	}
	if format == "image/png" {
		q.Set("transparent", "true")
	}
	return base + "/wms?" + q.Encode()
}

func (a *App) satelliteJob(ctx context.Context) error {
	err := a.fetchSatellite(ctx)
	if err != nil {
		a.threats.fail(satKey, err)
		slog.Warn("fetch failed", "source", satKey, "err", err)
	}
	return err
}

func (a *App) fetchSatellite(ctx context.Context) error {
	cfg := a.config().Satellite
	base := strings.TrimRight(cfg.URL, "/")
	ws, name, _ := strings.Cut(cfg.Layer, ":")
	prev, _ := a.threats.get(satKey).Data.(SatImage)

	resp, err := a.fetcher.Do(ctx, FetchReq{URL: fmt.Sprintf("%s/%s/%s/ows?service=WMS&version=1.3.0&request=GetCapabilities",
		base, url.PathEscape(ws), url.PathEscape(name)), Accept: "application/xml, text/xml", Timeout: satTimeout})
	if err != nil {
		return fmt.Errorf("capabilities: %w", err)
	}
	latest, err := parseSatTime(resp.Body)
	if err != nil {
		return err
	}
	cur := prev
	if !latest.Equal(prev.Time) || prev.Img == nil {
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: satMapURL(base, cfg.Layer, "image/jpeg", latest.Format(time.RFC3339)), Accept: "image/jpeg", Timeout: satTimeout})
		if err != nil {
			return fmt.Errorf("image: %w", err)
		}
		if !isJPEG(resp.Body) { // a WMS error comes back as XML with status 200
			return errors.New("image: no JPEG in response")
		}
		cur.Time, cur.Img = latest, resp.Body
	}
	if cur.Overlay == nil || time.Since(cur.OverlayAt) > 7*24*time.Hour {
		// Optional: without it the image shows without coastlines.
		if resp, err := a.fetcher.Do(ctx, FetchReq{URL: satMapURL(base, satOverlayLayers, "image/png", ""), Accept: "image/png", Timeout: satTimeout}); err == nil && isPNG(resp.Body) {
			cur.Overlay, cur.OverlayAt = resp.Body, time.Now()
		} else if err != nil {
			slog.Debug("satellite overlay failed", "err", err)
		}
	}
	a.threats.ok(satKey, cur, "", "")
	return nil
}

func (a *App) handleSatellite(w http.ResponseWriter, r *http.Request) {
	if !a.config().Satellite.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry(satKey)
	e["enabled"] = true
	if v, ok := a.threats.get(satKey).Data.(SatImage); ok && v.Img != nil {
		e["time"] = v.Time
		e["image"] = "api/satellite/image?t=" + strconv.FormatInt(v.Time.Unix(), 10)
		if v.Overlay != nil {
			e["overlay"] = "api/satellite/overlay?t=" + strconv.FormatInt(v.OverlayAt.Unix(), 10)
		}
	}
	writeJSON(w, r, http.StatusOK, 60, e)
}

func (a *App) handleSatelliteFile(overlay bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := a.threats.get(satKey).Data.(SatImage)
		body, ctype := v.Img, "image/jpeg"
		if overlay {
			body, ctype = v.Overlay, "image/png"
		}
		if !a.config().Satellite.Enabled || !ok || body == nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", ctype)
		h.Set("Cache-Control", "public, max-age=600") // the URL changes with every new image
		h.Set("Content-Security-Policy", "default-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		w.Write(body)
	}
}
