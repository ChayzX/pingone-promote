package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ContentSecurityPolicy is sent on every response. The pages use no
// script at all, so script-src stays at default-src 'none'.
const ContentSecurityPolicy = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// MaxFormBytes bounds a POST body.
const MaxFormBytes = 256 << 10

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
}

// hostOnly lowercases a Host header value and drops the port.
func hostOnly(hostport string) string {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	} else if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

func (s *Server) hostAllowed(hostport string) bool {
	if hostport == "" {
		return false
	}
	_, ok := s.hosts[hostOnly(hostport)]
	return ok
}

// sameOrigin requires positive evidence that a POST comes from one of this
// server's own pages. Sec-Fetch-Site is preferred (browsers send it even
// under Referrer-Policy no-referrer, which makes a form's Origin "null");
// otherwise Origin, otherwise Referer must name this host.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}
	matches := func(raw string) bool {
		u, err := url.Parse(raw)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
	}
	origin := r.Header.Get("Origin")
	if origin != "" && origin != "null" && !matches(origin) {
		return false
	}
	ref := r.Header.Get("Referer")
	if ref != "" && !matches(ref) {
		return false
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin" || origin != "" && origin != "null" || ref != ""
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// session is one browser. It owns its drafts and its CSRF token.
type session struct {
	id       string
	csrf     string
	lastSeen time.Time
}

type sessions struct {
	mu   sync.Mutex
	byID map[string]*session
	max  int
	ttl  time.Duration
	now  func() time.Time
}

func (ss *sessions) get(id string) *session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s := ss.byID[id]
	if s == nil || ss.now().Sub(s.lastSeen) > ss.ttl {
		return nil
	}
	s.lastSeen = ss.now()
	return s
}

func (ss *sessions) create() *session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if len(ss.byID) >= ss.max {
		var oldest *session
		for _, s := range ss.byID {
			if oldest == nil || s.lastSeen.Before(oldest.lastSeen) {
				oldest = s
			}
		}
		delete(ss.byID, oldest.id)
	}
	s := &session{id: randomToken(32), csrf: randomToken(32), lastSeen: ss.now()}
	ss.byID[s.id] = s
	return s
}

func (ss *sessions) expire() []string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	var gone []string
	for id, s := range ss.byID {
		if ss.now().Sub(s.lastSeen) > ss.ttl {
			delete(ss.byID, id)
			gone = append(gone, id)
		}
	}
	return gone
}

type ctxKey int

const sessionKey ctxKey = 0

func sessionFrom(r *http.Request) *session {
	s, _ := r.Context().Value(sessionKey).(*session)
	return s
}

func (s *Server) cookieName() string {
	if s.tls {
		return "__Host-pingone-promote"
	}
	return "pingone-promote"
}

// statusRecorder captures the status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// protect wraps every handler: headers, Host allow-list, session cookie,
// body limit and, for POST, same-origin and CSRF checks.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := s.now()
		w := &statusRecorder{ResponseWriter: rw}
		setSecurityHeaders(w.Header())
		defer func() {
			// Paths carry only random draft IDs; queries and forms are never logged.
			s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", w.status, "ms", s.now().Sub(start).Milliseconds())
		}()
		if !s.hostAllowed(r.Host) {
			http.Error(w, "This server does not answer for that host name.", http.StatusMisdirectedRequest)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodPost:
		default:
			w.Header().Set("Allow", "GET, HEAD, POST")
			http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
			return
		}
		var sess *session
		if c, err := r.Cookie(s.cookieName()); err == nil {
			sess = s.sessions.get(c.Value)
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, MaxFormBytes)
			if !sameOrigin(r) {
				s.forbidden(w, r, "This form did not come from this site's own page. Reload the page and try again.")
				return
			}
			if sess == nil {
				s.forbidden(w, r, "Your browser session expired or cookies are blocked. Reload the page and try again.")
				return
			}
			if err := r.ParseForm(); err != nil {
				http.Error(w, "The form is too large or malformed.", http.StatusRequestEntityTooLarge)
				return
			}
			got := r.PostForm.Get("csrf")
			if len(got) != len(sess.csrf) || subtle.ConstantTimeCompare([]byte(got), []byte(sess.csrf)) != 1 {
				s.forbidden(w, r, "The form is out of date. Reload the page and try again.")
				return
			}
		} else if sess == nil && !strings.HasPrefix(r.URL.Path, "/static/") && r.URL.Path != "/healthz" {
			sess = s.sessions.create()
			http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: sess.id, Path: "/", HttpOnly: true, Secure: s.tls,
				SameSite: http.SameSiteStrictMode})
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request, msg string) {
	s.renderStatus(w, r, http.StatusForbidden, "Not allowed", msg)
}
