package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Shared settings profiles: no accounts, no login. A visitor can add a name
// (Instellingen → Gebruiker) and the browser's whole preferences blob (the
// same shape normally kept only in that browser's localStorage) is mirrored
// to the server under that name, so picking the same name on another device
// loads it there too. Anyone who can reach the dashboard can add, read,
// overwrite or remove any profile; that is acceptable here since the
// profiles are a household convenience, not an access boundary.

const maxProfiles = 25

var (
	profileIDRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	profileSlugStripRe = regexp.MustCompile(`[^a-z0-9]+`)
)

func validProfileID(id string) bool { return profileIDRe.MatchString(id) }

// slugify turns a display name into an id; ids that collide get a -2, -3, ... suffix.
func slugify(name string) string {
	s := profileSlugStripRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	return s
}

type profileMeta struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type profileStore struct {
	mu      sync.Mutex
	order   []string                   // profile ids, in the order they were added
	names   map[string]string          // id -> display name
	data    map[string]json.RawMessage // id -> preferences blob
	dirty   bool
	limiter *rateLimiter
}

func newProfileStore() *profileStore {
	return &profileStore{
		names:   map[string]string{},
		data:    map[string]json.RawMessage{},
		limiter: newRateLimiter(20, 10),
	}
}

func (s *profileStore) exists(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.names[id]
	return ok
}

func (s *profileStore) list() []profileMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]profileMeta, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, profileMeta{ID: id, Name: s.names[id]})
	}
	return out
}

// ---------------------------------------------------------------------------
// Persistence (only with cache.snapshot_path): <snapshot>.profiles.json, mode 0600.

type profileFile struct {
	Order []string                   `json:"order"`
	Names map[string]string          `json:"names"`
	Data  map[string]json.RawMessage `json:"data"`
}

func (a *App) profileStatePath() string {
	if p := a.config().Cache.SnapshotPath; p != "" {
		return p + ".profiles.json"
	}
	return ""
}

func (a *App) saveProfileState() {
	path := a.profileStatePath()
	s := a.profiles
	s.mu.Lock()
	if path == "" || !s.dirty {
		s.mu.Unlock()
		return
	}
	f := profileFile{Order: append([]string{}, s.order...), Names: map[string]string{}, Data: map[string]json.RawMessage{}}
	for k, v := range s.names {
		f.Names[k] = v
	}
	for k, v := range s.data {
		f.Data[k] = v
	}
	s.dirty = false
	s.mu.Unlock()
	b, err := json.Marshal(f)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		slog.Error("profile state write failed", "path", path, "err", err)
	}
}

// loadProfileState reads the persisted registry, replacing the empty
// defaults that newProfileStore starts with.
func (a *App) loadProfileState() {
	path := a.profileStatePath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f profileFile
	if json.Unmarshal(b, &f) != nil {
		return
	}
	s := a.profiles
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order, s.names, s.data = nil, map[string]string{}, map[string]json.RawMessage{}
	for _, id := range f.Order {
		name, ok := f.Names[id]
		if !ok || !validProfileID(id) {
			continue
		}
		s.order = append(s.order, id)
		s.names[id] = name
		if d, ok := f.Data[id]; ok {
			s.data[id] = d
		}
	}
	slog.Info("profile state loaded", "profiles", len(s.order))
}

// ---------------------------------------------------------------------------
// HTTP: GET/POST /api/profiles, DELETE /api/profiles/{id}, GET/PUT /api/profile/{id}.

func (a *App) handleProfilesList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, 0, a.profiles.list())
}

func (a *App) handleProfilesCreate(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	s := a.profiles
	if !s.limiter.allow(a.clientIP(r)) {
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken")
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, "JSON verwacht")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil {
		writeError(w, r, http.StatusBadRequest, "ongeldig verzoek")
		return
	}
	name := strings.TrimSpace(req.Name)
	if n := len([]rune(name)); n == 0 || n > 40 {
		writeError(w, r, http.StatusBadRequest, "naam moet 1-40 tekens zijn")
		return
	}
	base := slugify(name)
	if base == "" {
		writeError(w, r, http.StatusBadRequest, "kies een naam met letters of cijfers")
		return
	}
	s.mu.Lock()
	if len(s.order) >= maxProfiles {
		s.mu.Unlock()
		writeError(w, r, http.StatusBadRequest, "maximumaantal profielen bereikt")
		return
	}
	id := base
	for n := 2; ; n++ {
		if _, ok := s.names[id]; !ok {
			break
		}
		id = base + "-" + strconv.Itoa(n)
	}
	s.order = append(s.order, id)
	s.names[id] = name
	s.dirty = true
	s.mu.Unlock()
	a.saveProfileState()
	writeJSON(w, r, http.StatusOK, 0, profileMeta{ID: id, Name: name})
}

func (a *App) handleProfilesDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !sameOrigin(w, r) {
		return
	}
	s := a.profiles
	if !s.limiter.allow(a.clientIP(r)) {
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken")
		return
	}
	s.mu.Lock()
	if _, ok := s.names[id]; !ok {
		s.mu.Unlock()
		writeError(w, r, http.StatusNotFound, "onbekend profiel")
		return
	}
	delete(s.names, id)
	delete(s.data, id)
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.dirty = true
	s.mu.Unlock()
	a.saveProfileState()
	writeJSON(w, r, http.StatusOK, 0, map[string]any{"ok": true})
}

func (a *App) handleProfileGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !a.profiles.exists(id) {
		writeError(w, r, http.StatusNotFound, "onbekend profiel")
		return
	}
	s := a.profiles
	s.mu.Lock()
	v, ok := s.data[id]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, r, http.StatusOK, 0, map[string]any{})
		return
	}
	writeJSON(w, r, http.StatusOK, 0, v)
}

func (a *App) handleProfileSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !a.profiles.exists(id) {
		writeError(w, r, http.StatusNotFound, "onbekend profiel")
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	s := a.profiles
	if !s.limiter.allow(a.clientIP(r)) {
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken")
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, "JSON verwacht")
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if err != nil || !json.Valid(b) || len(b) == 0 || b[0] != '{' {
		writeError(w, r, http.StatusBadRequest, "ongeldig verzoek")
		return
	}
	s.mu.Lock()
	s.data[id] = json.RawMessage(b)
	s.dirty = true
	s.mu.Unlock()
	a.saveProfileState()
	writeJSON(w, r, http.StatusOK, 0, map[string]any{"ok": true})
}
