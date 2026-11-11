package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/gitlab/gitlabtest"
	"pingone-promote/internal/promote"
	"pingone-promote/internal/request"
	"pingone-promote/internal/secretscan"
)

// This catches a server that accepts untrusted Host values or issues a session
// cookie without the protections required by the hosted UI.
func TestHTTPBoundaryAndSessionCookie(t *testing.T) {
	cfg := Config{AllowedHosts: []string{"promote.example.test"}}
	s, err := New(cfg, Secrets{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	t.Run("rejects an unconfigured host", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://evil.example/", nil)
		r.Host = "evil.example"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})
	t.Run("issues a protected anonymous session", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://promote.example.test/", nil)
		r.Host = "promote.example.test"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		var session *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == sessionCookieName {
				session = c
			}
		}
		if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.Value == "" {
			t.Fatalf("session cookie missing required protections: %#v", session)
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("security headers are missing")
		}
	})
}

func TestExplicitPublicOriginBehindTLSProxy(t *testing.T) {
	s, err := New(Config{AllowedHosts: []string{"promote.example.test"}, PublicOrigin: "https://promote.example.test:8443"}, Secrets{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	get := httptest.NewRequest("GET", "http://promote.example.test:8443/", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, get)
	if w.Code != 200 {
		t.Fatalf("proxy GET: %d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure {
		t.Fatal("configured HTTPS origin must issue Secure cookies")
	}
	s.mu.Lock()
	token := s.sessions[cookie.Value].CSRF
	s.mu.Unlock()
	for _, tc := range []struct {
		host, origin string
		want         int
	}{
		{"promote.example.test:8443", "https://promote.example.test:8443", 404},
		{"promote.example.test:8443", "http://promote.example.test:8443", 403},
		{"promote.example.test:8443", "https://promote.example.test", 403},
		{"promote.example.test", "https://promote.example.test", 400},
		{"localhost:8443", "https://localhost:8443", 400},
	} {
		r := httptest.NewRequest("POST", "http://"+tc.host+"/unknown", strings.NewReader(url.Values{"csrf": {token}}.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "promote.example.test:8443")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("host=%s origin=%s: %d, want %d", tc.host, tc.origin, w.Code, tc.want)
		}
	}
}

func TestForwardedHeadersDoNotEstablishHTTPS(t *testing.T) {
	s, err := New(Config{AllowedHosts: []string{"promote.example.test"}}, Secrets{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	r := httptest.NewRequest("GET", "http://promote.example.test/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Result().Cookies()[0].Secure {
		t.Fatal("untrusted forwarded header enabled HTTPS")
	}
	cookie := w.Result().Cookies()[0]
	s.mu.Lock()
	token := s.sessions[cookie.Value].CSRF
	s.mu.Unlock()
	r = httptest.NewRequest("POST", "http://promote.example.test/unknown", strings.NewReader(url.Values{"csrf": {token}}.Encode()))
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://promote.example.test")
	r.Header.Set("X-Forwarded-Proto", "https")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("forwarded proto bypassed Origin: %d", w.Code)
	}
}

type gateExporter struct {
	request.Exporter
	started chan struct{}
	release chan struct{}
}

func (e *gateExporter) ScopedExport(ctx context.Context, c exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	select {
	case e.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
		return e.Exporter.ScopedExport(ctx, c, seeds, exclude)
	}
}

type findingExporter struct{ request.Exporter }

type failMRTransport struct {
	base http.RoundTripper
	fail atomic.Bool
}

func (f *failMRTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.fail.Load() && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge_requests") {
		return nil, errors.New("simulated lost MR response")
	}
	return f.base.RoundTrip(r)
}

func TestPartialPublicationReconcilesWithoutSecondCommit(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	h := newHTTPHarness(t, fake)
	id := h.create("mr")
	h.action(id, "search", nil)
	h.wait(id)
	h.action(id, "selection", url.Values{"resource": {"pingone_davinci_flow.pingcli__Flow-0020-B"}})
	h.action(id, "preview", nil)
	if d := h.wait(id); d.State != "ready" {
		t.Fatalf("preview %s", d.State)
	}
	repo := h.s.deps.Repository.(*gitLabRepository)
	transport := &failMRTransport{base: repo.client.HTTP.Transport}
	transport.fail.Store(true)
	repo.client.HTTP.Transport = transport
	before := len(h.gl.CommitOrder)
	h.action(id, "submit", nil)
	d := h.wait(id)
	if d.State != "partial" || d.Report.CommitID == "" || !h.gl.HasBranch(h.s.cfg.BranchPrefix+id) || len(h.gl.CommitOrder) != before+1 {
		t.Fatalf("partial response lost reconciliation facts: %s commit=%s", d.State, d.Report.CommitID)
	}
	if w := h.call("POST", "/drafts/"+id+"/selection", url.Values{"revision": {strconv.FormatUint(d.Revision, 10)}}); w.Code != 409 {
		t.Fatalf("partial draft allowed editing: %d", w.Code)
	}
	transport.fail.Store(false)
	h.action(id, "submit", nil)
	d = h.wait(id)
	if d.State != "submitted" || len(h.gl.CommitOrder) != before+1 || len(h.gl.MRs) != 1 {
		t.Fatalf("reconciliation state=%s commits=%d mrs=%d", d.State, len(h.gl.CommitOrder)-before, len(h.gl.MRs))
	}
}

func (e findingExporter) ScopedExport(ctx context.Context, c exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	out, err := e.Exporter.ScopedExport(ctx, c, seeds, exclude)
	if err != nil {
		return nil, err
	}
	p := out.ModuleDir + "/pingone_davinci_flow.tf"
	out.Files[p] = append(out.Files[p], []byte("\n# -----BEGIN PRIVATE KEY----- sentinel-source-secret\n")...)
	out.Findings = secretscan.ScanFiles(out.Files)
	return out, nil
}

func TestBackgroundPreviewSurvivesHTTPDisconnectAndSerializesEdits(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	h := newHTTPHarness(t, fake)
	id := h.create("mr")
	h.action(id, "search", nil)
	h.wait(id)
	h.action(id, "selection", url.Values{"resource": {"pingone_davinci_flow.pingcli__Flow-0020-B"}})
	gate := &gateExporter{Exporter: h.s.deps.Exporter, started: make(chan struct{}, 1), release: make(chan struct{})}
	h.s.deps.Exporter = gate
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	}()
	form := url.Values{"csrf": {h.csrf}, "revision": {strconv.FormatUint(h.draft(id).Revision, 10)}}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "https://promote.example.test/drafts/"+id+"/preview", strings.NewReader(form.Encode())).WithContext(ctx)
	r.AddCookie(h.cookie)
	r.Header.Set("Origin", "https://promote.example.test")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.s.Handler().ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal(w.Body.String())
	}
	cancel()
	select {
	case <-gate.started:
	case <-time.After(10 * time.Second):
		t.Fatal("preview never started")
	}
	if got := h.call("POST", "/drafts/"+id+"/selection", form).Code; got != 409 {
		t.Fatalf("concurrent selection accepted: %d", got)
	}
	close(gate.release)
	if d := h.wait(id); d.State != "ready" {
		t.Fatalf("disconnect canceled preview: %s", d.State)
	}
	form.Set("resource", "pingone_davinci_flow.pingcli__Flow-0020-B")
	if got := h.call("POST", "/drafts/"+id+"/selection", form).Code; got != 409 {
		t.Fatalf("form from before background preview accepted after completion: %d", got)
	}
	if got := h.call("POST", "/drafts/"+id+"/submit", url.Values{"revision": {"0"}}).Code; got != 409 {
		t.Fatalf("stale revision submitted: %d", got)
	}
	h.s.mu.Lock()
	workspace := h.s.drafts[id].Snapshot.Workspace
	h.s.drafts[id].Expires = time.Now().Add(-time.Second)
	h.s.mu.Unlock()
	if w := h.call("GET", "/drafts/"+id, nil); w.Code != 404 {
		t.Fatalf("expired draft remained accessible: %d", w.Code)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("expired draft retained snapshot: %v", err)
	}
}

func TestRequestsPreferMergedDefaultBranchCommitOverSquashParent(t *testing.T) {
	fake := gitlabtest.New(t, testFiles(t))
	client, err := gitlab.NewClient(gitlab.Config{BaseURL: fake.BaseURL(), ProjectID: gitlabtest.ProjectID}, gitlabtest.Token)
	if err != nil {
		t.Fatal(err)
	}
	client.HTTP = fake.Client()
	merge, squash := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for i := 0; i < 100; i++ {
		fake.MRs = append(fake.MRs, &gitlabtest.MR{IID: int64(i + 1), SourceBranch: "unrelated/" + strconv.Itoa(i), State: "opened"})
	}
	fake.MRs = append(fake.MRs, &gitlabtest.MR{IID: 1, SourceBranch: gitlab.RequestBranchPrefix + "fixture-merged", State: "merged", MergeCommitSHA: merge, SquashCommitSHA: squash})
	repo := gitLabRepository{client: client, cfg: Config{BranchPrefix: gitlab.RequestBranchPrefix, Rollout: []string{"qa"}, ApplyJobPattern: "apply:{env}"}}
	records, err := repo.Requests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Commit != merge {
		t.Fatalf("merge evidence = %+v, want default branch merge SHA %s", records, merge)
	}
	fake.MRs[100].MergeCommitSHA = ""
	records, err = repo.Requests(context.Background())
	if err != nil || len(records) != 1 || records[0].Commit != squash {
		t.Fatalf("squash-only evidence = %+v, %v", records, err)
	}
	fake.Pipelines = []gitlabtest.Pipeline{{ID: 37, SHA: squash, Ref: "main"}}
	for i := 0; i < 100; i++ {
		fake.Jobs[37] = append(fake.Jobs[37], gitlabtest.Job{ID: int64(i + 1), Name: "unrelated-" + strconv.Itoa(i), Status: "failed"})
	}
	fake.Jobs[37] = append(fake.Jobs[37], gitlabtest.Job{ID: 101, Name: "apply:qa", Status: "success"})
	rollout, err := repo.Rollout(context.Background(), squash)
	if err != nil || len(rollout) != 1 || rollout[0].Status != "success" {
		t.Fatalf("paginated rollout = %+v, %v", rollout, err)
	}
}

func TestRenderedGitMetadataWithSuspectedSecretsIsWithheld(t *testing.T) {
	s, err := New(Config{}, Secrets{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, sentinel := range []string{"eyJabcdefghijk.eyJlmnopqrstuv.sensitive-signature", "-----BEGIN PRIVATE KEY----- sensitive-git-title"} {
		w := httptest.NewRecorder()
		s.render(w, "requests", page{Requests: []RequestRecord{{ID: "fixture-request", Title: sentinel}}})
		if strings.Contains(w.Body.String(), sentinel) || w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("suspected sensitive Git metadata rendered: status=%d", w.Code)
		}
	}
}

func TestSecretFindingsAndToolErrorsStayOutOfHTTPAndGit(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	for _, scenario := range []string{"source_finding", "old_git_diff", "export_error"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHTTPHarness(t, fake)
			id := h.create("mr")
			h.action(id, "search", nil)
			h.wait(id)
			h.action(id, "selection", url.Values{"resource": {"pingone_davinci_flow.pingcli__Flow-0020-B"}})
			sentinel := "sentinel-source-error"
			switch scenario {
			case "source_finding":
				h.s.deps.Exporter = findingExporter{h.s.deps.Exporter}
				sentinel = "sentinel-source-secret"
			case "old_git_diff":
				p := "definitions/pingone_davinci_flow.tf"
				raw, _, _ := h.gl.File("main", p)
				raw = strings.Replace(raw, "Look up the user", "-----BEGIN PRIVATE KEY----- sentinel-prior-git-value", 1)
				h.gl.Edit("main", p, raw)
				sentinel = "sentinel-prior-git-value"
			case "export_error":
				h.exporter.fail.Store(true)
			}
			before := len(h.gl.CommitOrder)
			h.action(id, "preview", nil)
			d := h.wait(id)
			if d.State == "ready" || d.State == "submitted" {
				t.Fatalf("unsafe preview became %s", d.State)
			}
			for _, p := range []string{"/drafts/" + id, "/drafts/" + id + "/status"} {
				w := h.call("GET", p, nil)
				if w.Code != 200 {
					t.Fatalf("GET %s %d", p, w.Code)
				}
				for _, v := range []string{sentinel, "fixture-worker-secret", gitlabtest.Token} {
					if strings.Contains(w.Body.String(), v) {
						t.Fatalf("HTTP disclosed %q", v)
					}
				}
			}
			if d.Report.Diff != "" {
				t.Fatal("unsafe preview retained diff")
			}
			h.s.mu.Lock()
			captured := h.s.drafts[id].Publication
			h.s.mu.Unlock()
			if captured != nil || len(h.gl.CommitOrder) != before {
				t.Fatal("secret-bearing preview became publishable")
			}
		})
	}
}

// This catches POST handlers that rely only on cookie SameSite behavior and
// accept a forged cross-origin form without the session-bound CSRF token.
func TestPOSTRequiresSessionCSRFToken(t *testing.T) {
	s, err := New(Config{AllowedHosts: []string{"promote.example.test"}}, Secrets{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	get := httptest.NewRequest(http.MethodGet, "http://promote.example.test/", nil)
	get.Host = "promote.example.test"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, get)
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("GET did not establish a session")
	}
	post := httptest.NewRequest(http.MethodPost, "http://promote.example.test/session", nil)
	post.Host = "promote.example.test"
	post.AddCookie(cookie)
	post.Header.Set("Origin", "https://evil.example")
	pw := httptest.NewRecorder()
	s.Handler().ServeHTTP(pw, post)
	if pw.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %d, want %d", pw.Code, http.StatusForbidden)
	}
}

type countingExporter struct {
	request.Exporter
	calls atomic.Int32
	fail  atomic.Bool
}

func (e *countingExporter) ScopedExport(ctx context.Context, c exporter.Credentials, seeds, exclude []string) (*exporter.Export, error) {
	e.calls.Add(1)
	if e.fail.Load() {
		return nil, errors.New("sentinel-source-error")
	}
	return e.Exporter.ScopedExport(ctx, c, seeds, exclude)
}
func (e *countingExporter) WholeExport(ctx context.Context, c exporter.Credentials, exclude []string) (*exporter.Export, error) {
	e.calls.Add(1)
	return e.Exporter.WholeExport(ctx, c, exclude)
}

type httpHarness struct {
	t        *testing.T
	s        *Server
	cookie   *http.Cookie
	csrf     string
	gl       *gitlabtest.Fake
	exporter *countingExporter
}

func (h *httpHarness) call(method, path string, form url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	var body *strings.Reader
	if form == nil {
		body = strings.NewReader("")
	} else {
		form.Set("csrf", h.csrf)
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, "https://promote.example.test"+path, body)
	r.Host = "promote.example.test"
	if h.cookie != nil {
		r.AddCookie(h.cookie)
	}
	if method == http.MethodPost {
		r.Header.Set("Origin", "https://promote.example.test")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	h.s.Handler().ServeHTTP(w, r)
	return w
}
func (h *httpHarness) post(path string, form url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	w := h.call(http.MethodPost, path, form)
	if w.Code != 303 {
		h.t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
	}
	return w
}
func (h *httpHarness) draft(id string) *draft {
	h.t.Helper()
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if h.s.drafts[id] == nil {
		h.t.Fatal("missing draft")
	}
	return cloneDraft(h.s.drafts[id])
}
func (h *httpHarness) wait(id string) *draft {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		d := h.draft(id)
		if !busy(d) {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("background job did not finish")
	return nil
}
func (h *httpHarness) action(id, action string, form url.Values) {
	h.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("revision", strconv.FormatUint(h.draft(id).Revision, 10))
	h.post("/drafts/"+id+"/"+action, form)
}
func (h *httpHarness) create(mode string) string {
	h.t.Helper()
	w := h.post("/drafts", url.Values{"source": {"dev"}, "name": {"Fixture User"}, "scope": {"selected"}, "upstream": {"yes"}, "mode": {mode}})
	return strings.TrimPrefix(w.Header().Get("Location"), "/drafts/")
}
func (h *httpHarness) selection(id string, fill bool) url.Values {
	h.t.Helper()
	d := h.draft(id)
	v := url.Values{"resource": {"pingone_davinci_flow.pingcli__Flow-0020-B"}, "exclude": {"pingone_davinci_connector_instance.pingcli__PingOne"}}
	if fill && d.Report != nil {
		for env, bs := range d.Report.Blockers {
			for _, b := range bs {
				if b.Code == promote.BlockMissingValue && b.Var != "" && env != "_request" {
					v.Set("value."+env+"."+b.Var, `"fixture-`+env+`-id"`)
				}
			}
		}
	}
	return v
}
func testFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	root := filepath.Join("..", "promote", "testdata", "repo")
	err := filepath.Walk(root, func(p string, fi os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if fi.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
func newHTTPHarness(t *testing.T, fake exportertest.Fake) *httpHarness {
	t.Helper()
	tf := os.Getenv("PINGONE_PROMOTE_TERRAFORM")
	if tf == "" {
		var err error
		tf, err = exec.LookPath("terraform")
		if err != nil {
			t.Fatal("real Terraform required for hosted end-to-end tests")
		}
	}
	gl := gitlabtest.New(t, testFiles(t))
	client, err := gitlab.NewClient(gitlab.Config{BaseURL: gl.BaseURL(), ProjectID: gitlabtest.ProjectID}, gitlabtest.Token)
	if err != nil {
		t.Fatal(err)
	}
	client.HTTP = gl.Client()
	e := &countingExporter{Exporter: &exporter.Runner{Config: exporter.Config{Path: fake.Path, Version: fake.Version, SHA256: fake.SHA256}}}
	cfg := Config{AllowedHosts: []string{"promote.example.test"}, Sources: []string{"dev"}, Rollout: []string{"qa", "stage", "prod"}}
	cfg.PingOne.RegionCode = "NA"
	cfg.PingOne.WorkerEnvironmentID = "11111111-1111-4111-8111-111111111111"
	s, err := New(cfg, Secrets{WorkerClientID: "fixture-worker-id", WorkerClientSecret: "fixture-worker-secret", GitLabToken: gitlabtest.Token}, Dependencies{Exporter: e, GitLab: client, TerraformBin: tf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := &httpHarness{t: t, s: s, gl: gl, exporter: e}
	w := h.call(http.MethodGet, "/", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			h.cookie = c
		}
	}
	m := regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`).FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatal("missing CSRF form token")
	}
	h.csrf = m[1]
	return h
}

func TestHostedHTTPWorkflow(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	for _, mode := range []string{"mr", "branch"} {
		t.Run(mode, func(t *testing.T) {
			h := newHTTPHarness(t, fake)
			connectorFile := "definitions/pingone_davinci_connector_instance.tf"
			connectorText, _, _ := h.gl.File("main", connectorFile)
			connectorText, _, _ = strings.Cut(connectorText, `resource "pingone_davinci_connector_instance" "pingcli__PingOne"`)
			h.gl.Edit("main", connectorFile, connectorText)
			sourceOverlay, _, _ := h.gl.File("main", "dev.tfvars")
			lines := strings.Split(sourceOverlay, "\n")
			kept := lines[:0]
			for _, line := range lines {
				if !strings.Contains(line, "module.davinci.pingone_davinci_connector_instance.pingcli__PingOne") {
					kept = append(kept, line)
				}
			}
			h.gl.Edit("main", "dev.tfvars", strings.Join(kept, "\n"))
			id := h.create(mode)
			h.action(id, "search", url.Values{"query": {"Flow B"}})
			d := h.wait(id)
			if len(d.Catalog) == 0 || d.State != "editing" {
				t.Fatalf("search: %+v", d)
			}
			html := h.call(http.MethodGet, "/drafts/"+id, nil).Body.String()
			if !strings.Contains(html, `value="`+h.csrf+`"`) || !strings.Contains(html, "Flow B") {
				t.Fatal("source catalog form not rendered")
			}
			h.action(id, "selection", h.selection(id, false))
			h.action(id, "preview", nil)
			d = h.wait(id)
			if d.State != "needs_values" || d.Report == nil || len(d.Report.Blockers) == 0 {
				t.Fatalf("expected missing per-environment IDs: %s, blockers=%v", d.State, d.Report.Blockers)
			}
			h.action(id, "selection", h.selection(id, true))
			h.action(id, "preview", nil)
			d = h.wait(id)
			if d.State != "ready" {
				t.Fatalf("preview state %s, blockers=%v, error=%v", d.State, d.Report.Blockers, d.Report.Error)
			}
			before := h.exporter.calls.Load()
			h.exporter.fail.Store(true)
			h.action(id, "submit", nil)
			d = h.wait(id)
			if d.State != "submitted" {
				t.Fatalf("submit %s %s", d.State, d.Message)
			}
			if h.exporter.calls.Load() != before {
				t.Fatal("submit re-exported live source")
			}
			if got := len(h.gl.MRs); (mode == "mr" && got != 1) || (mode == "branch" && got != 0) {
				t.Fatalf("MR count %d", got)
			}
			commit := h.gl.Commits[d.Report.CommitID]
			if commit == nil || !strings.Contains(commit.Message, "Requested-by: Fixture User (unverified)") {
				t.Fatal("unverified attribution missing")
			}
			for _, a := range commit.Actions {
				if a["file_path"] == gitlab.RequestFile {
					t.Fatal("hosted commit touched CI request transport")
				}
				if strings.Contains(a["content"], "fixture-worker-secret") {
					t.Fatal("worker credential in Git payload")
				}
			}
			if w := h.call(http.MethodGet, "/requests", nil); w.Code != 200 || !strings.Contains(w.Body.String(), id) {
				t.Fatalf("requests %d %s", w.Code, w.Body.String())
			}
			h.gl.Merge(h.s.cfg.BranchPrefix + id)
			if mode == "mr" {
				h.gl.Pipelines = []gitlabtest.Pipeline{{ID: 99, SHA: strings.Repeat("a", 40), Ref: "main"}, {ID: 98, SHA: d.Report.CommitID, Ref: h.s.cfg.BranchPrefix + id}, {ID: 97, SHA: d.Report.CommitID, Ref: "main"}}
				h.gl.Jobs[99] = []gitlabtest.Job{{ID: 990, Name: "apply:qa", Status: "failed", WebURL: h.gl.BaseURL() + "/group/infra/-/jobs/990"}}
				h.gl.Jobs[98] = []gitlabtest.Job{{ID: 980, Name: "apply:qa", Status: "failed", WebURL: h.gl.BaseURL() + "/group/infra/-/jobs/980"}}
				h.gl.Jobs[97] = []gitlabtest.Job{{ID: 970, Name: "apply:qa", Status: "success", WebURL: h.gl.BaseURL() + "/group/infra/-/jobs/970"}, {ID: 971, Name: "apply:prod", Status: "pending", WebURL: "https://evil.example/steal"}}
			}
			if w := h.call(http.MethodGet, "/requests/"+id, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "unknown") {
				t.Fatalf("rollout %d %s", w.Code, w.Body.String())
			} else if mode == "mr" && (!strings.Contains(w.Body.String(), ">success<") || !strings.Contains(w.Body.String(), "pending") || strings.Contains(w.Body.String(), "evil.example") || strings.Contains(w.Body.String(), ">failed<")) {
				t.Fatalf("rollout did not follow exact merge SHA or allowed an external URL: %s", w.Body.String())
			}
			// A new preview of the now-matching default branch publishes nothing.
			h.exporter.fail.Store(false)
			again := h.create("branch")
			h.action(again, "search", nil)
			h.wait(again)
			h.action(again, "selection", h.selection(again, false))
			h.action(again, "preview", nil)
			d = h.wait(again)
			if d.State != "no_changes" {
				t.Fatalf("matching repository state %s %+v", d.State, d.Report)
			}
		})
	}
}
func TestHostedHeadMoveRequiresNewPreview(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("dev"))
	h := newHTTPHarness(t, fake)
	id := h.create("mr")
	h.action(id, "search", nil)
	h.wait(id)
	h.action(id, "selection", url.Values{"resource": {"pingone_davinci_flow.pingcli__Flow-0020-B"}})
	h.action(id, "preview", nil)
	if d := h.wait(id); d.State != "ready" {
		t.Fatalf("preview %s %+v", d.State, d.Report)
	}
	h.gl.Edit("main", "versions.tf", "# concurrent change\n")
	before := len(h.gl.CommitOrder)
	h.action(id, "submit", nil)
	d := h.wait(id)
	if d.State != "conflict" || h.gl.HasBranch(h.s.cfg.BranchPrefix+id) || len(h.gl.CommitOrder) != before {
		t.Fatalf("moved default wrote artifacts: state %s", d.State)
	}
}

func TestDraftOwnershipRevisionAndSecurity(t *testing.T) {
	s, err := New(Config{AllowedHosts: []string{"promote.example.test"}}, Secrets{WorkerClientSecret: "html<&secret"}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := &httpHarness{t: t, s: s}
	w := h.call(http.MethodGet, "/", nil)
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			h.cookie = c
		}
	}
	s.mu.Lock()
	sess := s.sessions[h.cookie.Value]
	h.csrf = sess.CSRF
	s.drafts["fixture-draft-id"] = &draft{ID: "fixture-draft-id", Owner: "another-session", State: "editing", Revision: 1, Expires: time.Now().Add(time.Hour)}
	s.mu.Unlock()
	for _, p := range []string{"/drafts/fixture-draft-id", "/drafts/fixture-draft-id/status"} {
		if got := h.call(http.MethodGet, p, nil).Code; got != 404 {
			t.Fatalf("cross-session GET %s=%d", p, got)
		}
	}
	for _, host := range []string{"promote.example.test@evil.test", "promote.example.test.evil.test", "promote.example.test/path", "promote.example.test:99999", "promote.example.test.", "promote.example.test,evil.test"} {
		r := httptest.NewRequest("GET", "http://promote.example.test/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("Host %q allowed", host)
		}
	}
	r := httptest.NewRequest("GET", "https://promote.example.test/", nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if !w.Result().Cookies()[0].Secure {
		t.Fatal("TLS session cookie not Secure")
	}
	for _, path := range []string{"/drafts", "/drafts/fixture-draft-id/search", "/drafts/fixture-draft-id/selection", "/drafts/fixture-draft-id/preview", "/drafts/fixture-draft-id/submit"} {
		r := httptest.NewRequest("POST", "https://promote.example.test"+path, strings.NewReader("csrf=wrong"))
		r.AddCookie(h.cookie)
		r.Header.Set("Origin", "https://promote.example.test")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("POST %s bypassed CSRF: %d", path, w.Code)
		}
	}
	r = httptest.NewRequest("POST", "https://promote.example.test/drafts", strings.NewReader("csrf="+h.csrf+"&payload="+strings.Repeat("x", maxBody)))
	r.AddCookie(h.cookie)
	r.Header.Set("Origin", "https://promote.example.test")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("oversized body %d", w.Code)
	}
	var buf strings.Builder
	_ = json.NewEncoder(&buf).Encode(map[string]string{"secret": "html<&secret"})
	if strings.Contains(string(s.redact([]byte("html&lt;&amp;secret"))), "html&lt;&amp;secret") {
		t.Fatal("HTML-escaped credential not redacted")
	}
}
