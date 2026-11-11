package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
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
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
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

// origin is this server's own origin as a browser names it: https when it
// terminates TLS or runs behind a TLS proxy, http otherwise.
func (s *Server) origin(r *http.Request) string {
	scheme := "http"
	if s.tls || s.cfg.Exposure == ExposureBehindProxy {
		scheme = "https"
	}
	return scheme + "://" + strings.ToLower(r.Host)
}

// sameOrigin requires positive evidence that a POST comes from one of this
// server's own pages: Sec-Fetch-Site same-origin when the browser sends
// Fetch Metadata, otherwise an Origin header that is exactly this server's
// origin (older browsers). Origin "null" and a missing Origin are refused.
func (s *Server) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	o := r.Header.Get("Origin")
	return o != "" && o != "null" && strings.EqualFold(o, s.origin(r))
}

// crossSite reports a request another site caused (a link, form, image
// or fetch elsewhere). Browsers without Fetch Metadata report nothing.
func crossSite(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "cross-site" || site == "same-site"
}

// topNavigation is a top-level navigation by a GET (or HEAD): the only
// cross-site request the server answers.
func topNavigation(r *http.Request) bool {
	dest := r.Header.Get("Sec-Fetch-Dest")
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Header.Get("Sec-Fetch-Mode") == "navigate" &&
		dest != "iframe" && dest != "frame" && dest != "embed" && dest != "object"
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

var errSessionsFull = errors.New("the server has too many browser sessions")

// create makes a session. When the table is full it evicts an expired
// session, or else the least recently seen session that owns no drafts; a
// session with drafts is never evicted, so a flood of new sessions cannot
// take someone's work. owns is called with ss.mu held (lock order:
// sessions, then drafts).
func (ss *sessions) create(owns func(id string) bool) (*session, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if len(ss.byID) >= ss.max {
		var oldest *session
		for _, s := range ss.byID {
			if ss.now().Sub(s.lastSeen) > ss.ttl {
				oldest = s
				break
			}
			if owns(s.id) {
				continue
			}
			if oldest == nil || s.lastSeen.Before(oldest.lastSeen) {
				oldest = s
			}
		}
		if oldest == nil {
			return nil, errSessionsFull
		}
		delete(ss.byID, oldest.id)
	}
	s := &session{id: randomToken(32), csrf: randomToken(32), lastSeen: ss.now()}
	ss.byID[s.id] = s
	return s, nil
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

// protect wraps every handler: headers, Host allow-list, cross-site
// refusal, session cookie, body limit, panic recovery and, for POST,
// same-origin and CSRF checks.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := s.now()
		w := &statusRecorder{ResponseWriter: rw}
		setSecurityHeaders(w.Header())
		defer func() {
			// Paths carry only random draft IDs; queries and forms are never logged.
			s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", w.status, "ms", s.now().Sub(start).Milliseconds())
		}()
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			// No stack trace and no panic value: either could quote data.
			s.log.Error("handler panic", "method", r.Method, "path", r.URL.Path)
			if w.status == 0 {
				http.Error(w, "Something went wrong on the server. Go back and try again.", http.StatusInternalServerError)
			}
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
		// Another site may link here (a top-level navigation) and nothing
		// else: no embedding, no subresource, no cross-site form or fetch.
		crossNav := crossSite(r) && topNavigation(r)
		if crossSite(r) && !crossNav {
			http.Error(w, "This server does not answer requests from other sites.", http.StatusForbidden)
			return
		}
		var sess *session
		if c, err := r.Cookie(s.cookieName()); err == nil {
			sess = s.sessions.get(c.Value)
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, MaxFormBytes)
			if !s.sameOrigin(r) {
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
			if crossNav {
				// The SameSite=Strict cookie is not sent on a navigation
				// from another site. Reload from this site instead of
				// making a session (which would replace the browser's).
				s.continuePage(w, r)
				return
			}
			var err error
			if sess, err = s.sessions.create(s.drafts.owns); err != nil {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "The server is busy. Try again in a minute.", http.StatusServiceUnavailable)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: sess.id, Path: "/", HttpOnly: true, Secure: s.tls,
				SameSite: http.SameSiteStrictMode})
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

// continuePage reloads the same address from this site.
func (s *Server) continuePage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "continue", "Continue", continueView{URL: r.URL.RequestURI()}, true)
}

type continueView struct{ URL string }

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request, msg string) {
	s.renderStatus(w, r, http.StatusForbidden, "Not allowed", msg)
}
