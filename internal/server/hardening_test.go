package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"pingone-promote/internal/pingone"
	"strings"
	"testing"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/gitlab/gitlabtest"
	"pingone-promote/internal/request"
)

// rawGet sends a GET with the given headers and no cookie handling.
func rawGet(t *testing.T, e *env, path string, headers map[string]string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res
}

// gitlabCalls counts GitLab requests (no request is in flight when called).
func gitlabCalls(e *env) int { return len(e.gl.Requests) }

func TestCrossSiteRequestsRefused(t *testing.T) {
	e := newEnv(t, failingExporter{})
	before := gitlabCalls(e)
	n := len(e.srv.sessions.byID)
	// An image, fetch or frame on another site: refused before any work.
	for name, h := range map[string]map[string]string{
		"img":       {"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"},
		"fetch":     {"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"},
		"iframe":    {"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"},
		"same-site": {"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"},
	} {
		res := rawGet(t, e, "/requests", h, nil)
		if res.StatusCode != http.StatusForbidden || len(res.Cookies()) != 0 {
			t.Errorf("%s: %d, cookies %v", name, res.StatusCode, res.Cookies())
		}
	}
	if gitlabCalls(e) != before {
		t.Fatal("a cross-site subresource request reached GitLab")
	}
	if len(e.srv.sessions.byID) != n {
		t.Fatal("a cross-site request created a session")
	}
	// A link from another site (top-level navigation) gets a page that
	// reloads from this site, without a new session.
	res := rawGet(t, e, "/", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}, nil)
	if res.StatusCode != http.StatusOK || len(res.Cookies()) != 0 || len(e.srv.sessions.byID) != n {
		t.Fatalf("cross-site navigation: %d, cookies %v", res.StatusCode, res.Cookies())
	}
	// Same-origin and metadata-less navigations do get a session.
	res = rawGet(t, e, "/", map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "navigate"}, nil)
	if len(res.Cookies()) != 1 {
		t.Fatal("a same-origin page got no session")
	}
	if res.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatal("CORP header missing")
	}
}

func TestSessionsWithDraftsAreNeverEvicted(t *testing.T) {
	e := newEnv(t, failingExporter{})
	e.srv.sessions.mu.Lock()
	e.srv.sessions.max = 3
	e.srv.sessions.mu.Unlock()
	owners := []*browser{e.browser(), e.browser(), e.browser()}
	var drafts []string
	for _, b := range owners {
		drafts = append(drafts, b.newDraft())
	}
	// The table is full of sessions that own drafts: a new browser waits.
	res := rawGet(t, e, "/", nil, nil)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("full session table: %d", res.StatusCode)
	}
	for i, b := range owners {
		if p := b.get(drafts[i]); p.status != 200 {
			t.Fatalf("owner %d lost its draft: %d", i, p.status)
		}
	}
	// A session without drafts can be evicted for a newcomer.
	e.srv.sessions.mu.Lock()
	e.srv.sessions.max = 4
	e.srv.sessions.mu.Unlock()
	idle := e.browser()
	idle.get("/")
	if res := rawGet(t, e, "/", nil, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("an idle session was not evicted: %d", res.StatusCode)
	}
	for i, b := range owners {
		if p := b.get(drafts[i]); p.status != 200 {
			t.Fatalf("owner %d lost its draft after eviction: %d", i, p.status)
		}
	}
}

func TestExpiredSessionDropsItsDrafts(t *testing.T) {
	e := newEnv(t, failingExporter{})
	b := e.browser()
	path := b.newDraft()
	e.clock.advance(DraftIdleExpiry + 1)
	e.srv.drafts.expire(e.srv.sessions.expire())
	e.srv.drafts.mu.Lock()
	left := len(e.srv.drafts.byID)
	e.srv.drafts.mu.Unlock()
	if left != 0 {
		t.Fatal("drafts of an expired session were kept")
	}
	if p := b.get(path); p.status != http.StatusNotFound {
		t.Fatalf("expired draft still served: %d", p.status)
	}
}

func TestPostOriginRules(t *testing.T) {
	e := newEnv(t, failingExporter{})
	b := e.browser()
	b.get("/")
	post := func(h map[string]string) int {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/drafts", strings.NewReader(url.Values{"csrf": {b.csrf}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range h {
			req.Header.Set(k, v)
		}
		c := *b.c
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for name, tc := range map[string]struct {
		h    map[string]string
		want int
	}{
		"fetch metadata same-origin": {map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusSeeOther},
		"old Safari, real Origin":    {map[string]string{"Origin": e.ts.URL}, http.StatusSeeOther},
		"Origin null":                {map[string]string{"Origin": "null"}, http.StatusForbidden},
		"no Origin":                  {nil, http.StatusForbidden},
		"Referer only":               {map[string]string{"Referer": e.ts.URL + "/"}, http.StatusForbidden},
		"wrong scheme":               {map[string]string{"Origin": strings.Replace(e.ts.URL, "http:", "https:", 1)}, http.StatusForbidden},
		"user-typed (none)":          {map[string]string{"Sec-Fetch-Site": "none", "Origin": e.ts.URL}, http.StatusForbidden},
		"same-site":                  {map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
	} {
		if got := post(tc.h); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
}

// panicky panics where it is asked to.
type panicky struct{ failingExporter }

func (panicky) CatalogAllIDs(context.Context, exporter.Credentials) ([]exporter.Resource, error) {
	panic("exporter bug with " + sentinelClientSecret)
}

type panicImportIDs struct{}

func (panicImportIDs) SetImportIDs(*request.Request, map[string]map[string]string) error {
	panic("recorder bug")
}

func TestPanicsAreContained(t *testing.T) {
	e := newEnv(t, panicky{})
	b := e.browser()
	path := b.newDraft()
	b.post(path+"/save", mainForm("Jane", nil))
	// A panic in a job becomes a generic job error.
	p := b.wait(b.post(path+"/search", url.Values{"q": {"Flow"}}))
	mustContain(t, p.body, "Something went wrong on the server")
	// A panic in a handler (inside the drafts lock) is a plain 500 page
	// without details, and the lock is released.
	e.srv.importIDs = panicImportIDs{}
	p = b.post(path+"/save", mainForm("Jane", func(v url.Values) { v.Set("action", "preview") }))
	if p.status != http.StatusInternalServerError || strings.Contains(p.body, "recorder bug") || strings.Contains(p.body, "goroutine") {
		t.Fatalf("handler panic: %d\n%s", p.status, p.body)
	}
	if p := b.get(path); p.status != 200 {
		t.Fatalf("the drafts lock was not released: %d", p.status)
	}
	if strings.Contains(e.logs.String(), "recorder bug") {
		t.Fatal("the panic value was logged")
	}
}

func TestExportErrorsArePlain(t *testing.T) {
	p := plainErrors{log: slog.New(slog.NewTextHandler(io.Discard, nil)), redact: func(s string) string { return s }}
	lc := &exporter.LabelCollisionError{Detail: "duplicate resource labels: secret-ish detail"}
	cases := []struct {
		err  error
		want string
	}{
		{&exporter.ExecError{ExitCode: 1, Stderr: "Error: token=abc secret detail"}, msgExportTool},
		{lc, msgLabelCollision},
		{fmt.Errorf("verify: %w", exporter.ErrChecksum), msgExportPin},
		{errors.New("GET https://api.pingone.com/... returned body with secret"), msgExportOther},
		{fmt.Errorf("coreexport: pingone_population: %w", &pingone.APIError{Status: 401, Path: "/environments/e/populations"}),
			"PingOne answered HTTP 401 to GET /environments/e/populations. PingOne refused the worker's token"},
		{errors.New("pingone: the worker token request returned HTTP 401; check the worker client ID, secret, environment and region"),
			"PingOne: the worker token request returned HTTP 401"},
		{errors.New("coreexport: pingone_group: pingone: GET /environments/e/groups failed: the connection was refused"),
			"PingOne: GET /environments/e/groups failed: the connection was refused"},
	}
	for _, c := range cases {
		got := p.wrap("export", c.err)
		if !strings.HasPrefix(got.Error(), c.want) {
			t.Errorf("%v: got %q", c.err, got.Error())
		}
	}
	// The engine still recognizes a label collision as a blocker.
	var back *exporter.LabelCollisionError
	if !errors.As(p.wrap("export", lc), &back) {
		t.Fatal("LabelCollisionError is no longer detectable")
	}
	if !errors.Is(p.wrap("export", context.Canceled), context.Canceled) {
		t.Fatal("cancellation must pass through")
	}
}

// The exact loaded secret values are removed from every free-text field of
// a result, whatever the heuristic redaction let through.
func TestResultScrubbedOfExactSecrets(t *testing.T) {
	e := newEnv(t, failingExporter{})
	res := &request.Result{Diff: "+ description = \"" + sentinelClientSecret + "\"", Warnings: []string{"w " + sentinelClientID},
		Blockers: map[string][]request.BlockerOut{"qa": {{Message: "m " + gitlabtest.Token}}},
		Changes:  []request.ChangeOut{{Reason: "r " + sentinelClientSecret}},
		Selected: []request.Selected{{Query: sentinelClientSecret}},
		Error:    &request.ErrorOut{Message: "e " + sentinelClientSecret, Candidates: []request.Selected{{Query: gitlabtest.Token}}}}
	e.srv.scrub(res)
	all := fmt.Sprintf("%+v %+v %+v", res, res.Error, res.Blockers)
	for _, v := range sentinels {
		if strings.Contains(all, v) {
			t.Fatalf("a secret value survived scrubbing: %s", all)
		}
	}
}
