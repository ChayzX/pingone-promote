package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"pingone-promote/internal/pipelinesecrets"
	"strings"
	"sync"
	"time"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/promote"
	"pingone-promote/internal/request"
)

// Options build a Server.
type Options struct {
	Config  Config
	Secrets Secrets
	// Exporter exports the source environment. Production passes the
	// pinned terraformer runner; any request.Exporter works.
	Exporter request.Exporter
	// Identity defaults to TypedName.
	Identity Identity
	// ImportIDs defaults to a recorder that refuses import IDs (see
	// ImportIDRecorder).
	ImportIDs ImportIDRecorder
	// Logger defaults to a text logger on stderr. Every record passes a
	// filter that removes the secret values.
	Logger *slog.Logger
	// GitLabHTTP replaces the GitLab HTTP client (tests).
	GitLabHTTP *http.Client
	// Now defaults to time.Now.
	Now func() time.Time
	// MaxJobs bounds concurrent background jobs (default 2).
	MaxJobs int
}

// Server is the hosted pingone-promote service.
type Server struct {
	cfg        Config
	secrets    []string
	exporter   request.Exporter
	identity   Identity
	importIDs  ImportIDRecorder
	log        *slog.Logger
	gl         *gitlab.Client
	creds      exporter.Credentials
	layout     promote.Layout
	secretsCfg *pipelinesecrets.Config
	// listSlots bounds concurrent reads of item lists, apart from the
	// preview and submit workers.
	listSlots chan struct{}
	hosts     map[string]struct{}
	tls       bool
	now       func() time.Time
	sessions  *sessions
	drafts    *drafts
	pages     *pages
	jobs      chan struct{}
	base      context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	reqCache  requestsCache
}

// New checks the options and builds the server.
func New(o Options) (*Server, error) {
	if o.Exporter == nil {
		return nil, errors.New("server: an exporter is required")
	}
	if o.Secrets.GitLabToken == "" || o.Secrets.WorkerClientID == "" || o.Secrets.WorkerClientSecret == "" {
		return nil, errors.New("server: the secrets are incomplete")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Identity == nil {
		o.Identity = TypedName{}
	}
	if o.ImportIDs == nil {
		o.ImportIDs = requestImportIDs{}
	}
	if o.MaxJobs <= 0 {
		o.MaxJobs = 2
	}
	s := &Server{cfg: o.Config, exporter: o.Exporter, identity: o.Identity, importIDs: o.ImportIDs, now: o.Now,
		tls: o.Config.TLSCertFile != "", jobs: make(chan struct{}, o.MaxJobs)}
	for _, v := range []string{o.Secrets.GitLabToken, o.Secrets.WorkerClientSecret, o.Secrets.WorkerClientID} {
		if len(v) >= 4 {
			s.secrets = append(s.secrets, v)
		}
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	s.log = slog.New(redactingHandler{next: logger.Handler(), redact: s.redact})
	s.exporter = plainErrors{inner: o.Exporter, log: s.log, redact: s.redact}
	for _, w := range o.Config.warnings {
		s.log.Warn("configuration", "problem", w)
	}
	gl, err := gitlab.NewClient(gitlab.Config{BaseURL: o.Config.GitLab.BaseURL, ProjectID: o.Config.GitLab.ProjectID, TerraformBin: o.Config.Tools.Terraform}, o.Secrets.GitLabToken)
	if err != nil {
		return nil, errors.New("server: GitLab configuration: " + s.redact(err.Error()))
	}
	if o.GitLabHTTP != nil {
		gl.HTTP = o.GitLabHTTP
	}
	s.gl = gl
	s.creds = exporter.Credentials{ClientID: o.Secrets.WorkerClientID, ClientSecret: o.Secrets.WorkerClientSecret,
		WorkerEnvironmentID: o.Config.PingOne.WorkerEnvironmentID, RegionCode: o.Config.PingOne.RegionCode}
	s.layout = promote.DefaultLayout
	if o.Config.Layout == "terraformer" {
		s.layout = promote.TerraformerLayout
	}
	if o.Config.OverlayPattern != "" {
		s.layout.OverlayPattern = o.Config.OverlayPattern
	}
	rc := o.Config.Repository
	for _, f := range []struct {
		dst *string
		v   string
	}{{&s.layout.ModuleName, rc.ModuleName}, {&s.layout.ModuleDir, rc.ModuleDir},
		{&s.layout.ModuleFile, rc.ModuleFile}, {&s.layout.VariablesFile, rc.VariablesFile}, {&s.layout.SecretsFile, rc.SecretsFile},
		{&s.layout.ImportsFile, rc.ImportsFile}, {&s.layout.CustomerSecretsFile, rc.CustomerSecretsFile}} {
		if f.v != "" {
			*f.dst = f.v
		}
	}
	s.layout.CIVariables = append([]string(nil), rc.CIVariables...)
	// sourceEnvironmentIds only tells the server which environment to read;
	// it does not mean the pipeline supplies pingone_environment_id. Each
	// environment's tfvars still sets it unless repository.ciVariables
	// lists it.
	if rc.VaultPathPrefix != "" || rc.VaultMount != "" {
		sc := pipelinesecrets.DefaultConfig
		if rc.VaultPathPrefix != "" {
			sc.PathPrefix = strings.TrimSuffix(rc.VaultPathPrefix, "/")
		}
		if rc.VaultMount != "" {
			sc.Mount = rc.VaultMount
		}
		s.secretsCfg = &sc
	}
	if err := s.layout.Validate(); err != nil {
		return nil, errors.New("server: layout: " + err.Error())
	}
	if s.cfg.BranchPrefix == "" {
		s.cfg.BranchPrefix = gitlab.RequestBranchPrefix
	}
	if s.cfg.ApplyJobPattern == "" {
		s.cfg.ApplyJobPattern = "apply:{env}"
	}
	s.hosts = map[string]struct{}{"localhost": {}, "127.0.0.1": {}, "::1": {}}
	for _, h := range o.Config.AllowedHosts {
		s.hosts[strings.ToLower(h)] = struct{}{}
	}
	if s.pages, err = loadPages(); err != nil {
		return nil, err
	}
	s.sessions = &sessions{byID: map[string]*session{}, max: 1000, ttl: DraftIdleExpiry, now: s.now}
	s.drafts = &drafts{byID: map[string]*Draft{}, now: s.now, catalogs: map[string]*catalogEntry{}, loads: map[string]*listLoad{}}
	s.base, s.stop = context.WithCancel(context.Background())
	s.wg.Add(1)
	go s.janitor()
	s.listSlots = make(chan struct{}, 2)
	if s.cfg.BranchPattern != "" && s.cfg.branchRE == nil {
		re, err := compileBranchPattern(s.cfg.BranchPattern)
		if err != nil {
			return nil, fmt.Errorf("server: %w", err)
		}
		s.cfg.branchRE = re
	}
	return s, nil
}

func (s *Server) janitor() {
	defer s.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.base.Done():
			return
		case <-t.C:
			s.drafts.expire(s.sessions.expire())
		}
	}
}

// Close cancels running jobs, waits for them and removes every draft's
// snapshot (gitlab.Client.Snapshot makes each one a private 0700 directory).
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.stop()
		s.drafts.closeAll()
		s.wg.Wait()
	})
}

// redact removes the secret values from text that may reach a page or a
// log (for example an error message that quotes a tool's output).
func (s *Server) redact(text string) string {
	for _, v := range s.secrets {
		text = strings.ReplaceAll(text, v, "[redacted]")
		if e := html.EscapeString(v); e != v {
			text = strings.ReplaceAll(text, e, "[redacted]")
		}
	}
	return text
}

type redactingHandler struct {
	next   slog.Handler
	redact func(string) string
}

func (h redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(slog.String(a.Key, h.redact(a.Value.String())))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h redactingHandler) WithAttrs(as []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(as))
	for i, a := range as {
		clean[i] = slog.String(a.Key, h.redact(a.Value.String()))
	}
	return redactingHandler{next: h.next.WithAttrs(clean), redact: h.redact}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(name), redact: h.redact}
}

// Handler is the whole HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS())))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("POST /drafts", s.newDraft)
	mux.HandleFunc("GET /drafts/{id}", s.draftHome)
	mux.HandleFunc("GET /drafts/{id}/scope", s.stepPage(1))
	mux.HandleFunc("GET /drafts/{id}/items", s.stepPage(2))
	mux.HandleFunc("GET /drafts/{id}/send", s.stepPage(3))
	mux.HandleFunc("POST /drafts/{id}/save", s.saveDraft)
	mux.HandleFunc("POST /drafts/{id}/search", s.search)
	mux.HandleFunc("POST /drafts/{id}/pick", s.pick)
	mux.HandleFunc("GET /drafts/{id}/job", s.jobPage)
	mux.HandleFunc("POST /drafts/{id}/cancel", s.cancelJob)
	mux.HandleFunc("POST /drafts/{id}/preview", s.startPreview)
	mux.HandleFunc("GET /drafts/{id}/preview", s.previewPage)
	mux.HandleFunc("POST /drafts/{id}/choices", s.saveChoices)
	mux.HandleFunc("POST /drafts/{id}/values", s.saveValues)
	mux.HandleFunc("POST /drafts/{id}/submit", s.startSubmit)
	mux.HandleFunc("GET /drafts/{id}/result", s.resultPage)
	mux.HandleFunc("GET /requests", s.requestsPage)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.renderStatus(w, r, http.StatusNotFound, "Not found", "There is no page here.")
	})
	return s.protect(mux)
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context, ready func(addr string)) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return errors.New("cannot listen on " + s.cfg.Listen)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10,
		ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelWarn), BaseContext: func(net.Listener) context.Context { return s.base }}
	if s.tls {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err != nil {
			ln.Close()
			return errors.New("cannot load the TLS certificate or key")
		}
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		ln = tls.NewListener(ln, srv.TLSConfig)
	}
	if ready != nil {
		ready(ln.Addr().String())
	}
	s.log.Info("listening", "addr", ln.Addr().String(), "tls", s.tls)
	s.warm()
	if s.cfg.Exposure != "" {
		s.log.Warn("the server is exposed beyond this machine and there is no sign-in: anyone who can reach it can file requests under any name; merge request review is the control",
			"exposure", s.cfg.Exposure)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		s.Close()
		return err
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = srv.Shutdown(shut)
	s.Close()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// discard is a logger for tests that do not capture logs.
var discard = slog.New(slog.NewTextHandler(io.Discard, nil))
