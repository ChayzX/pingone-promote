package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/promote"
	"pingone-promote/internal/request"
)

const sessionCookieName = "pingone_promote_session"
const (
	maxSessions      = 128
	maxDrafts        = 32
	maxSessionDrafts = 4
	maxBody          = 256 << 10
	maxArtifact      = 16 << 20
	sessionTTL       = 2 * time.Hour
	draftTTL         = time.Hour
)

// IdentityProvider is reserved for authenticated deployments. A typed name
// never passes through this interface and is always attributed as unverified.
type Identity struct {
	Subject, DisplayName string
	Verified             bool
}
type IdentityProvider interface {
	Authenticate(*http.Request) (Identity, error)
}
type Dependencies struct {
	Exporter     request.Exporter
	Repository   Repository
	GitLab       *gitlab.Client
	TerraformBin string
	Authenticate IdentityProvider
}
type session struct {
	ID, CSRF, Name string
	Expires        time.Time
}
type draft struct {
	ID, Owner, State, Message, Query, TypeFilter string
	Revision                                     uint64
	Expires                                      time.Time
	Request                                      request.Request
	Catalog                                      []exporter.Resource
	Report                                       *request.Result
	Snapshot                                     *Snapshot
	Publication                                  *gitlab.BranchCommit
}
type job struct {
	draftID  string
	revision uint64
	run      func(context.Context)
}

// Server owns disposable sessions and bounded background work. Close cancels
// workers and removes every private repository snapshot.
type Server struct {
	cfg      Config
	secrets  Secrets
	deps     Dependencies
	layout   promote.Layout
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	sessions map[string]*session
	drafts   map[string]*draft
	jobs     chan job
	wg       sync.WaitGroup
	closed   bool
}

func New(cfg Config, secrets Secrets, deps Dependencies) (*Server, error) {
	if cfg.PublicOrigin != "" {
		u, err := url.Parse(cfg.PublicOrigin)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.ForceQuery || u.RawQuery != "" || strings.Contains(cfg.PublicOrigin, "#") {
			return nil, errors.New("invalid public HTTPS origin")
		}
		if _, _, ok := parsedHost(u.Host); !ok {
			return nil, errors.New("invalid public HTTPS authority")
		}
		allow := &Server{cfg: cfg}
		allow.cfg.PublicOrigin = ""
		if !allow.allowedHost(u.Host) {
			return nil, errors.New("public HTTPS hostname is not allowed")
		}
	}
	if (deps.Exporter == nil && cfg.Tools.Terraformer != "") || (deps.Repository == nil && cfg.GitLab.BaseURL != "") {
		if err := cfg.check(); err != nil {
			return nil, err
		}
	}
	if cfg.BranchPrefix == "" {
		cfg.BranchPrefix = gitlab.RequestBranchPrefix
	}
	if !gitlab.ValidRequestBranchPrefix(cfg.BranchPrefix) {
		return nil, errors.New("invalid request branch prefix")
	}
	if cfg.ApplyJobPattern == "" {
		cfg.ApplyJobPattern = "apply:{env}"
	}
	if cfg.SignIn != "" && cfg.SignIn != "none" {
		return nil, errors.New("sign-in provider is unavailable")
	}
	l := promote.DefaultLayout
	if cfg.Layout == "terraformer" {
		l = promote.TerraformerLayout
	} else if cfg.Layout != "" && cfg.Layout != "default" {
		return nil, errors.New("invalid layout")
	}
	if cfg.OverlayPattern != "" {
		l.OverlayPattern = cfg.OverlayPattern
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if deps.TerraformBin == "" {
		deps.TerraformBin = cfg.Tools.Terraform
	}
	if deps.Exporter == nil && cfg.Tools.Terraformer != "" {
		deps.Exporter = &exporter.Runner{Config: exporter.Config{Path: cfg.Tools.Terraformer, Version: cfg.Tools.TerraformerVersion, SHA256: cfg.Tools.TerraformerSHA256}}
	}
	if deps.Repository == nil {
		c := deps.GitLab
		if c == nil && cfg.GitLab.BaseURL != "" {
			var err error
			c, err = gitlab.NewClient(gitlab.Config{BaseURL: cfg.GitLab.BaseURL, ProjectID: cfg.GitLab.ProjectID, TerraformBin: deps.TerraformBin}, secrets.GitLabToken)
			if err != nil {
				return nil, err
			}
		}
		if c != nil {
			deps.Repository = &gitLabRepository{client: c, cfg: cfg, terraform: deps.TerraformBin}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{cfg: cfg, secrets: secrets, deps: deps, layout: l, ctx: ctx, cancel: cancel, sessions: map[string]*session{}, drafts: map[string]*draft{}, jobs: make(chan job, 8)}
	for i := 0; i < 2; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	s.wg.Add(1)
	go s.janitor()
	return s, nil
}
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.drafts {
		s.release(d)
	}
	s.drafts = map[string]*draft{}
	s.sessions = map[string]*session{}
}
func (s *Server) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.jobs:
			ctx, cancel := context.WithTimeout(s.ctx, 15*time.Minute)
			j.run(ctx)
			cancel()
		}
	}
}
func (s *Server) janitor() {
	defer s.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-t.C:
			s.mu.Lock()
			s.expire(now)
			s.mu.Unlock()
		}
	}
}
func (s *Server) expire(now time.Time) {
	for id, d := range s.drafts {
		if now.After(d.Expires) && d.State != "queued" && d.State != "previewing" && d.State != "searching" && d.State != "submitting" {
			s.release(d)
			delete(s.drafts, id)
		}
	}
	for id, v := range s.sessions {
		if now.After(v.Expires) {
			delete(s.sessions, id)
		}
	}
}
func (s *Server) release(d *draft) {
	if d.Snapshot != nil && d.Snapshot.Close != nil {
		_ = d.Snapshot.Close()
	}
	d.Snapshot = nil
	d.Publication = nil
}
func randomID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(time.Now())
	if s.closed {
		return nil, errors.New("server closing")
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if v := s.sessions[c.Value]; v != nil {
			v.Expires = time.Now().Add(sessionTTL)
			c := *v
			return &c, nil
		}
	}
	if r.Method != http.MethodGet {
		return nil, errors.New("session required")
	}
	if len(s.sessions) >= maxSessions {
		return nil, errors.New("session capacity reached")
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	csrf, err := randomID()
	if err != nil {
		return nil, err
	}
	v := &session{ID: id, CSRF: csrf, Expires: time.Now().Add(sessionTTL)}
	s.sessions[id] = v
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: id, Path: "/", HttpOnly: true, Secure: r.TLS != nil || s.cfg.PublicOrigin != "", SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	c := *v
	return &c, nil
}

func parsedHost(raw string) (string, string, bool) {
	if raw == "" || strings.HasSuffix(raw, ":") || strings.ContainsAny(raw, "/@?#\\ \t\r\n") {
		return "", "", false
	}
	u, err := url.Parse("http://" + raw)
	if err != nil || u.Host != raw || u.User != nil || u.Path != "" {
		return "", "", false
	}
	host := u.Hostname()
	if host == "" || strings.HasSuffix(host, ".") {
		return "", "", false
	}
	port := u.Port()
	if port != "" {
		p, e := strconv.Atoi(port)
		if e != nil || p < 1 || p > 65535 {
			return "", "", false
		}
	}
	if strings.Contains(host, ":") {
		if net.ParseIP(host) == nil || !strings.HasPrefix(raw, "[") {
			return "", "", false
		}
	} else if !hostRE.MatchString(host) {
		return "", "", false
	} else {
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return "", "", false
			}
		}
	}
	return strings.ToLower(host), port, true
}
func (s *Server) allowedHost(raw string) bool {
	host, _, ok := parsedHost(raw)
	if !ok {
		return false
	}
	if s.cfg.PublicOrigin != "" {
		u, err := url.Parse(s.cfg.PublicOrigin)
		return err == nil && sameAuthority(raw, u.Host, "https")
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	for _, h := range s.cfg.AllowedHosts {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}
func (s *Server) sameOrigin(r *http.Request) bool {
	if len(r.Header.Values("Origin")) != 1 {
		return false
	}
	raw := r.Header.Get("Origin")
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Path != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil || s.cfg.PublicOrigin != "" {
		scheme = "https"
	}
	if u.Scheme != scheme {
		return false
	}
	return sameAuthority(r.Host, u.Host, scheme)
}
func sameAuthority(aRaw, bRaw, scheme string) bool {
	a, ap, ok := parsedHost(aRaw)
	b, bp, bok := parsedHost(bRaw)
	if !ok || !bok {
		return false
	}
	if ap == "" {
		if scheme == "https" {
			ap = "443"
		} else {
			ap = "80"
		}
	}
	if bp == "" {
		if scheme == "https" {
			bp = "443"
		} else {
			bp = "80"
		}
	}
	return a == b && ap == bp
}
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
	if !s.allowedHost(r.Host) {
		http.Error(w, "Untrusted Host", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", 405)
		return
	}
	sess, err := s.getSession(w, r)
	if err != nil {
		status := http.StatusServiceUnavailable
		if r.Method == http.MethodPost {
			status = http.StatusForbidden
		}
		http.Error(w, "Session unavailable", status)
		return
	}
	if r.Method == http.MethodPost {
		if !s.sameOrigin(r) {
			http.Error(w, "Same-origin form required", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid or oversized form", 413)
			return
		}
		token := r.PostForm.Get("csrf")
		if len(token) != len(sess.CSRF) || subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRF)) != 1 {
			http.Error(w, "Invalid CSRF token", 403)
			return
		}
	}
	s.route(w, r, sess)
}
func (s *Server) redact(b []byte) []byte {
	for _, v := range []string{s.secrets.WorkerClientID, s.secrets.WorkerClientSecret, s.secrets.GitLabToken} {
		if v != "" {
			b = bytes.ReplaceAll(b, []byte(v), []byte("[redacted]"))
			b = bytes.ReplaceAll(b, []byte(html.EscapeString(v)), []byte("[redacted]"))
			encoded, _ := json.Marshal(v)
			if len(encoded) > 2 {
				b = bytes.ReplaceAll(b, encoded[1:len(encoded)-1], []byte("[redacted]"))
			}
		}
	}
	return b
}
