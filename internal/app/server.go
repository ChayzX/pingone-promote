package app

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"

	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/logging"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/store"
)

//go:embed web/*
var assets embed.FS

type inventorySession struct {
	ID        string `json:"id"`
	ProfileID string `json:"profileId"`
	pingone.Inventory
}
type planSession struct {
	Record         store.Record
	Source, Target pingone.Inventory
}
type clientFactory func(pingone.Profile, string) (*pingone.Client, error)
type Server struct {
	store                                  *store.Store
	token, csrf                            string
	mu                                     sync.Mutex
	inventories                            map[string]*inventorySession
	plans                                  map[string]*planSession
	clients                                map[string]*pingone.Client
	clientFactory                          clientFactory
	files                                  http.Handler
	logger                                 *logging.Controller
	localTargets                           map[string]bool
	gitlabFactory                          func(gitlab.Config, string) (*gitlab.Client, error)
	gitlabSessionToken, gitlabTokenBinding string
	requests                               map[string]gitops.Request
	submissionAttempts                     map[string]bool

	profileGen uint64
}

func New(st *store.Store, token string) *Server {
	root, _ := fs.Sub(assets, "web")
	return &Server{store: st, token: token, csrf: RandomToken(), inventories: map[string]*inventorySession{}, plans: map[string]*planSession{}, clients: map[string]*pingone.Client{}, clientFactory: pingone.NewClient, gitlabFactory: gitlab.NewClient, requests: map[string]gitops.Request{}, submissionAttempts: map[string]bool{}, localTargets: map[string]bool{}, files: http.FileServer(http.FS(root))}
}
func RandomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func equal(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func localHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	ip := net.ParseIP(h)
	return h == "localhost" || ip != nil && ip.IsLoopback()
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if !localHost(r.Host) {
		replyError(w, 403, errors.New("this application only accepts localhost requests"))
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		replyError(w, 403, errors.New("cross-origin requests are not allowed"))
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		replyError(w, 403, errors.New("cross-site requests are not allowed"))
		return
	}
	if r.URL.Path == "/" && r.Method == "GET" && r.URL.Query().Has("token") {
		if !equal(r.URL.Query().Get("token"), s.token) {
			replyError(w, 401, errors.New("invalid startup link; use the URL printed by the application"))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "pingone_local", Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	cookie, err := r.Cookie("pingone_local")
	if err != nil || !equal(cookie.Value, s.token) {
		replyError(w, 401, errors.New("open the startup URL printed in your terminal to unlock this local app"))
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" || r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/style.css" {
			http.NotFound(w, r)
			return
		}
		s.files.ServeHTTP(w, r)
		return
	}
	if r.Method != "GET" && !equal(r.Header.Get("X-CSRF-Token"), s.csrf) {
		replyError(w, 403, errors.New("invalid request token; reload the app"))
		return
	}
	// Inventory loads spend tens of seconds waiting on PingOne. Running them
	// under the global lock serialized the source and destination loads the
	// browser sends together, so they manage the lock themselves.
	if r.URL.Path == "/api/inventory" && r.Method == "POST" {
		if err := s.loadInventory(w, r); err != nil {
			s.logFailure(r, err)
			replyRouteError(w, err)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.route(w, r); err != nil {
		s.logFailure(r, err)
		replyRouteError(w, err)
	}
}

func (s *Server) logFailure(r *http.Request, err error) {
	if s.logger == nil {
		return
	}
	status := http.StatusBadRequest
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		status = statusErr.status
	} else {
		var persistenceErr *store.PersistenceError
		if errors.As(err, &persistenceErr) {
			status = http.StatusInternalServerError
		}
	}
	level, stage, class := slog.LevelWarn, "application", "request_rejected"
	if status >= 500 {
		level, stage, class = slog.LevelError, "operation", "request_failed"
	}
	s.logger.Event(r.Context(), level, "app.failure", stage, "", "", logging.RequestID(r.Context()), class, status, 0)
}

func replyRouteError(w http.ResponseWriter, err error) {
	var e *statusError
	if errors.As(err, &e) {
		replyError(w, e.status, e.err)
	} else if errors.Is(err, store.ErrCredentialStateInconsistent) {
		replyError(w, http.StatusInternalServerError, store.ErrCredentialStateInconsistent)
	} else {
		var persistenceErr *store.PersistenceError
		if errors.As(err, &persistenceErr) {
			replyError(w, http.StatusInternalServerError, errors.New("Could not access local app storage; check storage permissions and available disk space, then restart the app."))
		} else {
			replyError(w, http.StatusBadRequest, err)
		}
	}
}

type statusError struct {
	status int
	err    error
}

type profileCredentialStorageCapability struct {
	Mode         string `json:"mode"`
	Provider     string `json:"provider"`
	Supported    bool   `json:"supported"`
	Availability string `json:"availability"`
	Requirement  string `json:"requirement,omitempty"`
}

func credentialStorageCapabilityFor(goos string) profileCredentialStorageCapability {
	capability := profileCredentialStorageCapability{Mode: "keychain", Availability: "unsupported"}
	switch goos {
	case "darwin":
		capability.Provider = "macOS Keychain"
		capability.Supported = true
		capability.Availability = "not_probed"
	case "linux":
		capability.Provider = "Linux Secret Service"
		capability.Supported = true
		capability.Availability = "not_probed"
		capability.Requirement = "Requires a running, unlocked Secret Service provider, such as GNOME Keyring or KWallet. Availability is checked when you save or use a credential."
	case "windows":
		capability.Provider = "OS credential vault (unsupported on Windows)"
		capability.Requirement = "Persistent OS credential storage is supported only on macOS and Linux. Use Session only or Environment variable on Windows."
	default:
		capability.Provider = "OS credential vault (unsupported)"
		capability.Requirement = "Persistent OS credential storage is supported only on macOS and Linux. Use Session only or Environment variable on this platform."
	}
	return capability
}

func (e *statusError) Error() string      { return e.err.Error() }
func problem(status int, err error) error { return &statusError{status, err} }
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func replyError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
func decodeBody(w http.ResponseWriter, r *http.Request, out any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errors.New("request must use application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("invalid request JSON: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("request must contain one JSON object")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(out); err != nil {
		return fmt.Errorf("invalid request JSON: %w", err)
	}
	return nil
}
func (s *Server) route(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Path
	if path == "/api/gitlab" || path == "/api/gitlab/submit" || path == "/api/requests/preview" {
		return s.gitlabRoute(w, r)
	}
	if path == "/api/logging" {
		return s.loggingRoute(w, r)
	}
	switch {
	case path == "/api/session" && r.Method == "GET":
		writeJSON(w, map[string]any{"csrf": s.csrf, "regions": pingone.RegionDomains, "localTargets": s.localTargetIDs(), "credentialStorage": credentialStorageCapabilityFor(runtime.GOOS)})
		return nil
	case path == "/api/profiles" && r.Method == "GET":
		p, err := s.store.ListProfiles()
		if err != nil {
			return problem(http.StatusInternalServerError, errors.New("Could not read saved profiles; check local app storage and restart the app."))
		}
		writeJSON(w, p)
		return nil
	case path == "/api/profiles" && r.Method == "POST":
		var input struct {
			pingone.Profile
			Secret string `json:"secret"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return err
		}
		p, err := s.store.SaveProfile(input.Profile, input.Secret)
		if err != nil {
			return err
		}
		s.clients = map[string]*pingone.Client{}
		s.profileGen++
		writeJSON(w, p)
		return nil
	case strings.HasPrefix(path, "/api/profiles/") && r.Method == "DELETE":
		if err := s.store.DeleteProfile(strings.TrimPrefix(path, "/api/profiles/")); err != nil {
			return err
		}
		s.clients = map[string]*pingone.Client{}
		s.profileGen++
		writeJSON(w, map[string]bool{"deleted": true})
		return nil
	case path == "/api/history" && r.Method == "GET":
		records, err := s.store.ListRecords()
		if err != nil {
			return problem(http.StatusInternalServerError, errors.New("Could not read saved history; check local app storage and restart the app."))
		}
		// The store appends in creation order; show the newest operation first so
		// the latest promotion (the only one PingOne can roll back) is on top.
		slices.Reverse(records)
		// The history list renders only description/kind/status/source/target.
		// Each record also embeds every promoted resource ref (both inventories),
		// which the list never reads but which bloats the response (tens of MB at
		// a few dozen records). Drop it from the list; the stored record keeps it
		// for restart scope reconstruction, and createPlan reloads full records.
		for i := range records {
			records[i].Resources = nil
		}
		writeJSON(w, records)
		return nil
	case path == "/api/mappings" && r.Method == "POST":
		var input inventoryPair
		if err := decodeBody(w, r, &input); err != nil {
			return err
		}
		source, target, err := s.pair(input)
		if err != nil {
			return err
		}
		p, err := s.profile(source.ProfileID)
		if err != nil {
			return err
		}
		c, err := s.client(p)
		if err != nil {
			return err
		}
		var native struct {
			Mappings map[string]string `json:"resourceMappings"`
		}
		err = c.Do(r.Context(), "GET", pingone.EnvPath(source.EnvironmentID)+"/promotionMappings/"+target.EnvironmentID, "", nil, &native)
		warnings := []string{}
		if err != nil {
			warnings = append(warnings, "Saved PingOne mappings could not be read. Review suggestions and the final native plan: "+err.Error())
		}
		mappings := suggestMappings(source.Inventory, target.Inventory, native.Mappings)
		writeJSON(w, map[string]any{"mappings": mappings, "warnings": warnings})
		return nil
	case path == "/api/plans" && r.Method == "POST":
		return s.createPlan(w, r)
	case strings.HasPrefix(path, "/api/plans/"):
		parts := strings.Split(strings.TrimPrefix(path, "/api/plans/"), "/")
		if parts[0] == "" {
			return errors.New("plan ID required")
		}
		ps, err := s.loadPlan(parts[0])
		if err != nil {
			return err
		}
		if len(parts) == 1 && r.Method == "GET" {
			v, err := s.refresh(r, ps)
			if err != nil {
				return err
			}
			writeJSON(w, v)
			return nil
		}
		if len(parts) == 2 && parts[1] == "deploy" && r.Method == "POST" {
			return s.deploy(w, r, ps)
		}
		if len(parts) == 2 && parts[1] == "rollback" && r.Method == "POST" {
			return s.rollback(w, r, ps)
		}
	}
	return problem(404, errors.New("unknown application endpoint"))
}

func (s *Server) loggingRoute(w http.ResponseWriter, r *http.Request) error {
	if s.logger == nil {
		return problem(http.StatusServiceUnavailable, errors.New("logging settings are unavailable in this server"))
	}
	if r.Method == http.MethodGet {
		writeJSON(w, s.logger.Settings())
		return nil
	}
	if r.Method != http.MethodPut {
		return problem(http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
	var input struct {
		Level       string `json:"level"`
		Debug       bool   `json:"debug"`
		Format      string `json:"format"`
		Destination string `json:"destination"`
		File        string `json:"file"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	settings := logging.RuntimeSettings{Level: input.Level, Debug: input.Debug, Format: input.Format, Destination: input.Destination, File: input.File}
	if err := s.logger.Configure(settings); err != nil {
		return problem(http.StatusBadRequest, err)
	}
	writeJSON(w, s.logger.Settings())
	return nil
}
func (s *Server) profile(id string) (pingone.Profile, error) {
	profiles, err := s.store.ListProfiles()
	if err != nil {
		return pingone.Profile{}, problem(http.StatusInternalServerError, errors.New("Could not read saved profiles; check local app storage and restart the app."))
	}
	for _, p := range profiles {
		if p.ID == id {
			return p, nil
		}
	}
	return pingone.Profile{}, errors.New("environment profile not found; restore it or create a new promotion")
}
func (s *Server) client(p pingone.Profile) (*pingone.Client, error) {
	current, err := s.profile(p.ID)
	if err != nil {
		return nil, err
	}
	if current.EnvironmentID != p.EnvironmentID || current.Region != p.Region {
		return nil, errors.New("profile environment identity changed; load resources and build a new plan")
	}
	if c := s.clients[p.ID]; c != nil {
		return c, nil
	}
	secret, err := s.store.Secret(current)
	if err != nil {
		return nil, err
	}
	c, err := s.clientFactory(current, secret)
	if err == nil {
		s.clients[p.ID] = c
	}
	return c, err
}

// loadInventory reads one environment's inventory. Profile and client lookup,
// and recording the result, happen under s.mu; the PingOne calls in between do
// not, so source and destination loads run concurrently. If a profile was saved
// or deleted meanwhile, the result is discarded rather than stored.
func (s *Server) loadInventory(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		ProfileID string `json:"profileId"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		return err
	}
	s.mu.Lock()
	gen := s.profileGen
	p, err := s.profile(input.ProfileID)
	var c *pingone.Client
	if err == nil {
		c, err = s.client(p)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	inv, err := c.Inventory(r.Context(), p.EnvironmentID)
	if err != nil {
		return problem(502, err)
	}
	session := &inventorySession{ID: RandomToken(), ProfileID: p.ID, Inventory: inv}
	s.mu.Lock()
	if s.profileGen != gen {
		s.mu.Unlock()
		return problem(409, errors.New("environment profiles changed while resources were loading; load resources again"))
	}
	s.inventories[session.ID] = session
	s.mu.Unlock()
	writeJSON(w, browserInventory(session))
	return nil
}

// browserInventory is the view of an inventory sent to the UI. The UI needs
// each resource's identity, name, type and dependencies; the full
// configuration stays in the server session, where plan inspection uses it.
// Leaving it out cut the payload ~90% in benchmarks and keeps even redacted
// configuration out of the browser.
func browserInventory(session *inventorySession) inventorySession {
	safe := *session
	safe.Resources = make([]pingone.Resource, len(session.Resources))
	for i, res := range session.Resources {
		res.Configuration = nil
		safe.Resources[i] = res
	}
	return safe
}
