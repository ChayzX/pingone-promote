package app

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"

	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
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
	localTargets                           map[string]bool
	gitlabFactory                          func(gitlab.Config, string) (*gitlab.Client, error)
	gitlabSessionToken, gitlabTokenBinding string
	requests                               map[string]gitops.Request
	submissionAttempts                     map[string]bool
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.route(w, r); err != nil {
		var e *statusError
		if errors.As(err, &e) {
			replyError(w, e.status, e.err)
		} else {
			replyError(w, 400, err)
		}
	}
}

type statusError struct {
	status int
	err    error
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
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("request must use application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid request JSON: %w", err)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	return nil
}
func (s *Server) route(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Path
	if path == "/api/gitlab" || path == "/api/gitlab/submit" || path == "/api/requests/preview" {
		return s.gitlabRoute(w, r)
	}
	switch {
	case path == "/api/session" && r.Method == "GET":
		writeJSON(w, map[string]any{"csrf": s.csrf, "regions": pingone.RegionDomains, "localTargets": s.localTargetIDs()})
		return nil
	case path == "/api/profiles" && r.Method == "GET":
		p, err := s.store.ListProfiles()
		if err != nil {
			return err
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
		writeJSON(w, p)
		return nil
	case strings.HasPrefix(path, "/api/profiles/") && r.Method == "DELETE":
		if err := s.store.DeleteProfile(strings.TrimPrefix(path, "/api/profiles/")); err != nil {
			return err
		}
		s.clients = map[string]*pingone.Client{}
		writeJSON(w, map[string]bool{"deleted": true})
		return nil
	case path == "/api/history" && r.Method == "GET":
		records, err := s.store.ListRecords()
		if err != nil {
			return err
		}
		writeJSON(w, records)
		return nil
	case path == "/api/inventory" && r.Method == "POST":
		var input struct {
			ProfileID string `json:"profileId"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return err
		}
		p, err := s.profile(input.ProfileID)
		if err != nil {
			return err
		}
		c, err := s.client(p)
		if err != nil {
			return err
		}
		inv, err := c.Inventory(r.Context(), p.EnvironmentID)
		if err != nil {
			return problem(502, err)
		}
		session := &inventorySession{ID: RandomToken(), ProfileID: p.ID, Inventory: inv}
		s.inventories[session.ID] = session
		safe := *session
		safe.Resources = make([]pingone.Resource, len(inv.Resources))
		for i, res := range inv.Resources {
			safe.Resources[i] = res
			if res.Configuration != nil {
				safe.Resources[i].Configuration = promotion.Redact(res.Configuration).(map[string]any)
			}
		}
		writeJSON(w, safe)
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
func (s *Server) profile(id string) (pingone.Profile, error) {
	profiles, err := s.store.ListProfiles()
	if err != nil {
		return pingone.Profile{}, err
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
