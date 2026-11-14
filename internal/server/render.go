package server

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed web/templates/*.html web/static/*
var webFS embed.FS

func staticFS() fs.FS {
	sub, err := fs.Sub(webFS, "web/static")
	if err != nil {
		panic(err)
	}
	return sub
}

type pages struct {
	t map[string]*template.Template
}

var pageNames = []string{"home", "draft", "job", "preview", "result", "requests", "status", "continue"}

func loadPages() (*pages, error) {
	funcs := template.FuncMap{
		"when": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format("2006-01-02 15:04 UTC")
		},
		"short": func(s string) string {
			if len(s) > 12 {
				return s[:12]
			}
			return s
		},
		"add": func(a, b int) int { return a + b },
	}
	p := &pages{t: map[string]*template.Template{}}
	for _, name := range pageNames {
		t, err := template.New(name).Funcs(funcs).ParseFS(webFS, "web/templates/base.html", "web/templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		p.t[name] = t
	}
	return p, nil
}

// view is what every page template receives.
type view struct {
	Title   string
	CSRF    string
	Refresh bool
	// RefreshNow reloads at once (the continue page).
	RefreshNow bool
	Data       any
}

// render executes a page into a buffer, removes any secret value and
// writes it.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name, title string, data any, refresh bool) {
	v := view{Title: title, Refresh: refresh && name != "continue", RefreshNow: refresh && name == "continue", Data: data}
	if sess := sessionFrom(r); sess != nil {
		v.CSRF = sess.csrf
	}
	var buf bytes.Buffer
	if err := s.pages.t[name].ExecuteTemplate(&buf, "base", v); err != nil {
		s.log.Error("render failed", "page", name, "error", err.Error())
		http.Error(w, "The page could not be shown.", http.StatusInternalServerError)
		return
	}
	body := s.redact(buf.String())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write([]byte(body))
	}
}

type statusView struct {
	Message string
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, title, msg string) {
	s.render(w, r, status, "status", title, statusView{Message: msg}, false)
}

// bufferedWriter collects a response to send later.
type bufferedWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedWriter() *bufferedWriter { return &bufferedWriter{header: http.Header{}} }

func (b *bufferedWriter) Header() http.Header { return b.header }

func (b *bufferedWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

func (b *bufferedWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *bufferedWriter) flushTo(w http.ResponseWriter) {
	for k, v := range b.header {
		if strings.EqualFold(k, "Content-Security-Policy") {
			continue // already set by protect
		}
		w.Header()[k] = v
	}
	if b.status == 0 {
		b.status = http.StatusOK
	}
	w.WriteHeader(b.status)
	w.Write(b.body.Bytes())
}
