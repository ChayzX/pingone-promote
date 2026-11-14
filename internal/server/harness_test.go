package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/gitlab/gitlabtest"
	"pingone-promote/internal/request"
)

// Sentinel secrets: none of them may ever appear in a page or a log line.
const (
	sentinelClientID     = "SENTINEL-client-id-7c41b9"
	sentinelClientSecret = "SENTINEL-client-secret-e93a0f"
)

var sentinels = []string{sentinelClientID, sentinelClientSecret, gitlabtest.Token}

var (
	fakeOnce sync.Once
	fakeBin  exportertest.Fake
	fakeDir  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fakeDir != "" {
		os.RemoveAll(fakeDir)
	}
	os.Exit(code)
}

func terraformBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("PINGONE_PROMOTE_TERRAFORM"); p != "" {
		return p
	}
	p, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not found; the fmt gate needs it")
	}
	return p
}

// fakeTerraformer builds the fake exporter once per test binary.
func fakeTerraformer(t *testing.T) exportertest.Fake {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "server-fake-")
		if err != nil {
			t.Fatal(err)
		}
		fakeDir = dir
		fakeBin = exportertest.Build(t, dir, exportertest.FixtureDir("dev"))
	})
	if fakeBin.Path == "" {
		t.Fatal("fake terraformer was not built")
	}
	return fakeBin
}

func repoFiles(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "promote", "testdata", "repo")
	out := map[string]string{}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, _ := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return out
}

// syncBuffer is a log sink safe for concurrent writers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type env struct {
	t      *testing.T
	gl     *gitlabtest.Fake
	status *gitlabtest.Status
	srv    *Server
	ts     *httptest.Server
	logs   *syncBuffer
	cfg    Config
	clock  *clock
}

// clock is the server's time: real time plus a test-controlled offset.
type clock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.offset += d
	c.mu.Unlock()
}

func testConfig(t *testing.T, gl *gitlabtest.Fake) Config {
	var c Config
	c.Listen = "127.0.0.1:0"
	c.AllowedHosts = []string{"promote.example.com"}
	c.SignIn = "none"
	c.GitLab.BaseURL = gl.BaseURL()
	c.GitLab.ProjectID = gitlabtest.ProjectID
	c.PingOne.RegionCode = "NA"
	c.PingOne.WorkerEnvironmentID = "11111111-1111-4111-8111-111111111111"
	c.Sources = []string{"dev"}
	c.Rollout = []string{"qa", "stage", "prod"}
	c.BranchPrefix = "pingone-promote/"
	c.ApplyJobPattern = "apply:{env}"
	c.Tools.Terraform = terraformBin(t)
	return c
}

// newEnv starts a server against a fake GitLab and the fake terraformer.
// exp replaces the exporter when not nil.
func newEnv(t *testing.T, exp request.Exporter) *env {
	t.Helper()
	return newEnvWith(t, exp, nil)
}

// newEnvWith is newEnv with a configuration change.
func newEnvWith(t *testing.T, exp request.Exporter, mut func(*Config)) *env {
	t.Helper()
	gl := gitlabtest.New(t, repoFiles(t))
	st := gitlabtest.NewStatus(gl)
	cfg := testConfig(t, gl)
	if mut != nil {
		mut(&cfg)
	}
	if exp == nil {
		f := fakeTerraformer(t)
		exp = &exporter.Runner{Config: exporter.Config{Version: f.Version, SHA256: f.SHA256, Path: f.Path}}
	}
	logs := &syncBuffer{}
	clk := &clock{}
	s, err := New(Options{Config: cfg, Secrets: Secrets{WorkerClientID: sentinelClientID, WorkerClientSecret: sentinelClientSecret, GitLabToken: gitlabtest.Token},
		Exporter: exp, Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), GitLabHTTP: st.Client(), Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	e := &env{t: t, gl: gl, status: st, srv: s, ts: ts, logs: logs, cfg: cfg, clock: clk}
	t.Cleanup(func() {
		ts.Close()
		s.Close()
		for _, v := range sentinels {
			if strings.Contains(logs.String(), v) {
				t.Errorf("a secret value reached the log")
			}
		}
	})
	return e
}

// checkingTransport asserts the headers and the absence of secrets on
// every response the server sends.
type checkingTransport struct {
	t    *testing.T
	next http.RoundTripper
}

func (c checkingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := c.next.RoundTrip(r)
	if err != nil {
		return res, err
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(body))
	h := res.Header
	if h.Get("Content-Security-Policy") != ContentSecurityPolicy || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Referrer-Policy") != "same-origin" || h.Get("Cross-Origin-Resource-Policy") != "same-origin" || h.Get("Cache-Control") != "no-store" || h.Get("Cross-Origin-Opener-Policy") != "same-origin" {
		c.t.Errorf("%s %s: security headers missing: %v", r.Method, r.URL.Path, h)
	}
	for _, v := range sentinels {
		if bytes.Contains(body, []byte(v)) {
			c.t.Errorf("%s %s: a secret value is in the response", r.Method, r.URL.Path)
		}
	}
	return res, nil
}

// browser is one browser session (its own cookie jar).
type browser struct {
	e    *env
	c    *http.Client
	csrf string
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{e: e, c: &http.Client{Jar: jar, Transport: checkingTransport{t: e.t, next: http.DefaultTransport}, Timeout: 2 * time.Minute}}
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

type page struct {
	status int
	body   string
	path   string // final path after redirects
	raw    string // final URL
}

func (b *browser) do(req *http.Request) page {
	b.e.t.Helper()
	res, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if m := csrfRE.FindSubmatch(body); m != nil {
		b.csrf = string(m[1])
	}
	return page{status: res.StatusCode, body: string(body), path: res.Request.URL.Path, raw: res.Request.URL.String()}
}

func (b *browser) get(path string) page {
	b.e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, b.e.ts.URL+path, nil)
	return b.settle(b.do(req))
}

// post submits a form as the browser would from this site's page.
func (b *browser) post(path string, form url.Values) page {
	b.e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if _, ok := form["csrf"]; !ok {
		if b.csrf == "" {
			b.get("/")
		}
		form.Set("csrf", b.csrf)
	}
	// A submit is sent from the preview page, which names its preview.
	if strings.HasSuffix(path, "/submit") {
		if _, ok := form["preview"]; !ok {
			if m := previewTokenRE.FindStringSubmatch(b.get(strings.TrimSuffix(path, "/submit") + "/preview").body); m != nil {
				form.Set("preview", m[1])
			} else {
				// No submit form on the page (blocked, stale, nothing to
				// change): the post is made by hand, naming the preview.
				id := strings.TrimSuffix(strings.TrimPrefix(path, "/drafts/"), "/submit")
				b.e.srv.drafts.mu.Lock()
				if d := b.e.srv.drafts.byID[id]; d != nil && d.preview != nil {
					form.Set("preview", d.preview.token())
				}
				b.e.srv.drafts.mu.Unlock()
			}
		}
	}
	req, _ := http.NewRequest(http.MethodPost, b.e.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "null")
	return b.settle(b.do(req))
}

// settle follows the items page's own refresh while it shows "Reading the
// items", as a browser does.
func (b *browser) settle(p page) page {
	b.e.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for strings.HasSuffix(p.path, "/items") && strings.Contains(p.body, `http-equiv="refresh"`) {
		if time.Now().After(deadline) {
			b.e.t.Fatal("the item list was not read")
		}
		time.Sleep(20 * time.Millisecond)
		p = b.do(mustRequest(http.MethodGet, b.e.ts.URL+p.path))
	}
	return p
}

func mustRequest(method, u string) *http.Request {
	req, _ := http.NewRequest(method, u, nil)
	return req
}

// wait follows a job page until the job finishes.
func (b *browser) wait(p page) page {
	b.e.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for strings.HasSuffix(p.path, "/job") && strings.Contains(p.body, `http-equiv="refresh"`) {
		if time.Now().After(deadline) {
			b.e.t.Fatal("job did not finish")
		}
		time.Sleep(50 * time.Millisecond)
		p = b.get(p.path)
	}
	return p
}

// newDraft starts a draft and returns its path.
func (b *browser) newDraft() string {
	b.e.t.Helper()
	p := b.post("/drafts", nil)
	if p.status != 200 || !strings.HasPrefix(p.path, "/drafts/") {
		b.e.t.Fatalf("new draft: %d %s", p.status, p.path)
	}
	// The draft's address, without its step page.
	return strings.TrimSuffix(p.path, "/scope")
}

func mainForm(name string, mut func(url.Values)) url.Values {
	v := url.Values{"name": {name}, "source": {"dev"}, "scope": {"selected"}, "upstream": {"yes"}, "mode": {"mr"}, "title": {"Flow B from dev"}, "action": {"save"}}
	if mut != nil {
		mut(v)
	}
	return v
}

func mustContain(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("page lacks %q:\n%s", w, body)
		}
	}
}

// failingExporter returns an error quoting a secret, as a broken tool
// might.
type failingExporter struct{}

func (failingExporter) Catalog(context.Context, exporter.Credentials, bool) ([]exporter.Resource, error) {
	return nil, errFailing
}
func (failingExporter) CatalogAllIDs(context.Context, exporter.Credentials) ([]exporter.Resource, error) {
	return nil, errFailing
}
func (failingExporter) ScopedExport(context.Context, exporter.Credentials, []string, []string) (*exporter.Export, error) {
	return nil, errFailing
}
func (failingExporter) WholeExport(context.Context, exporter.Credentials, []string) (*exporter.Export, error) {
	return nil, errFailing
}

type failErr struct{}

func (failErr) Error() string {
	return "tool failed: client_secret=" + sentinelClientSecret + " token " + gitlabtest.Token
}

var errFailing = failErr{}

// slowList answers the item list only once release is closed.
type slowList struct {
	failingExporter
	release chan struct{}
}

func (s slowList) CatalogAllIDs(ctx context.Context, _ exporter.Credentials) ([]exporter.Resource, error) {
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []exporter.Resource{{Address: exporter.Address{Type: "pingone_population", Label: "pingcli__Customers"}, Name: "Customers", ID: "p1"}}, nil
}

var previewTokenRE = regexp.MustCompile(`name="preview" value="([^"]+)"`)
