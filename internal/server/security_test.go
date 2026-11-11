package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHostAllowList(t *testing.T) {
	e := newEnv(t, failingExporter{})
	cases := map[string]int{
		"127.0.0.1:8080":           200,
		"localhost":                200,
		"LOCALHOST:1":              200,
		"[::1]:8080":               200,
		"promote.example.com:443":  200,
		"promote.example.com":      200,
		"evil.example":             http.StatusMisdirectedRequest,
		"promote.example.com.evil": http.StatusMisdirectedRequest,
		"127.0.0.2":                http.StatusMisdirectedRequest,
		"":                         http.StatusMisdirectedRequest,
	}
	h := e.srv.Handler()
	for host, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: got %d, want %d", host, w.Code, want)
		}
		if w.Header().Get("Content-Security-Policy") != ContentSecurityPolicy {
			t.Errorf("Host %q: no CSP on the response", host)
		}
	}
	// Forwarded host headers are not trusted.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "evil.example"
	r.Header.Set("X-Forwarded-Host", "localhost")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("X-Forwarded-Host was trusted: %d", w.Code)
	}
}

func TestCSRFAndSameOrigin(t *testing.T) {
	e := newEnv(t, failingExporter{})
	b := e.browser()
	b.get("/")
	good := b.csrf
	if good == "" {
		t.Fatal("no CSRF token on the page")
	}
	post := func(form url.Values, headers map[string]string, withCookie bool) int {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/drafts", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		c := b.c
		if !withCookie {
			c = e.browser().c
		}
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		defer func() { c.CheckRedirect = nil }()
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "null"}
	cases := []struct {
		name    string
		form    url.Values
		headers map[string]string
		cookie  bool
		want    int
	}{
		{"valid", url.Values{"csrf": {good}}, same, true, http.StatusSeeOther},
		{"missing token", url.Values{}, same, true, http.StatusForbidden},
		{"wrong token", url.Values{"csrf": {strings.Repeat("A", len(good))}}, same, true, http.StatusForbidden},
		{"short token", url.Values{"csrf": {good[:10]}}, same, true, http.StatusForbidden},
		{"no session cookie", url.Values{"csrf": {good}}, same, false, http.StatusForbidden},
		{"cross-site fetch", url.Values{"csrf": {good}}, map[string]string{"Sec-Fetch-Site": "cross-site"}, true, http.StatusForbidden},
		{"same-site fetch", url.Values{"csrf": {good}}, map[string]string{"Sec-Fetch-Site": "same-site"}, true, http.StatusForbidden},
		{"foreign origin", url.Values{"csrf": {good}}, map[string]string{"Origin": "https://evil.example"}, true, http.StatusForbidden},
		{"foreign referer", url.Values{"csrf": {good}}, map[string]string{"Referer": "https://evil.example/x"}, true, http.StatusForbidden},
		{"no origin evidence", url.Values{"csrf": {good}}, map[string]string{"Origin": "null"}, true, http.StatusForbidden},
		{"same origin header", url.Values{"csrf": {good}}, map[string]string{"Origin": e.ts.URL}, true, http.StatusSeeOther},
	}
	for _, tc := range cases {
		if got := post(tc.form, tc.headers, tc.cookie); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
	// Oversized bodies are refused.
	big := url.Values{"csrf": {good}, "x": {strings.Repeat("a", MaxFormBytes)}}
	if got := post(big, same, true); got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized form: %d", got)
	}
}

func TestCookieAndHeadersOnEveryPage(t *testing.T) {
	e := newEnv(t, failingExporter{})
	b := e.browser()
	res, err := b.c.Get(e.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var c *http.Cookie
	for _, k := range res.Cookies() {
		c = k
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("session cookie: %+v", c)
	}
	// checkingTransport asserts the headers on each of these.
	for _, p := range []string{"/", "/requests", "/static/style.css", "/healthz", "/nope", "/drafts/unknown"} {
		b.get(p)
	}
	if p := b.get("/healthz"); p.status != 200 || strings.TrimSpace(p.body) != "ok" {
		t.Fatalf("healthz: %d %q", p.status, p.body)
	}
	if p := b.get("/static/style.css"); p.status != 200 {
		t.Fatalf("stylesheet: %d", p.status)
	}
	req, _ := http.NewRequest(http.MethodPut, e.ts.URL+"/", nil)
	if p := b.do(req); p.status != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: %d", p.status)
	}
	if strings.Contains(b.get("/").body, "<script") {
		t.Fatal("pages must not contain scripts")
	}
}

func TestIdentityRecordsUnverified(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("name=+Jane+Doe+"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p, err := TypedName{}.Who(r)
	if err != nil || p.Verified || p.Name != "Jane Doe" || p.Recorded() != "Jane Doe (unverified)" {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{"", "a\x00b", "line\nbreak", strings.Repeat("x", MaxNameLength+1), "Mallory (unverified)"} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(url.Values{"name": {bad}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if _, err := (TypedName{}).Who(r); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if (Person{Name: "Jane", Verified: true}).Recorded() != "Jane" {
		t.Fatal("a verified person is recorded by name")
	}
}

func TestDraftsAreIsolatedBetweenSessions(t *testing.T) {
	e := newEnv(t, failingExporter{})
	alice, mallory := e.browser(), e.browser()
	path := alice.newDraft()
	alice.post(path+"/save", mainForm("Alice", nil))
	if p := mallory.get(path); p.status != http.StatusNotFound || strings.Contains(p.body, "Alice") {
		t.Fatalf("another session read the draft: %d", p.status)
	}
	mallory.get("/")
	for _, action := range []string{"/save", "/preview", "/submit", "/pick", "/values", "/choices", "/cancel", "/search"} {
		if p := mallory.post(path+action, mainForm("Mallory", nil)); p.status != http.StatusNotFound {
			t.Errorf("%s by another session: %d", action, p.status)
		}
	}
	if p := mallory.get("/"); strings.Contains(p.body, path) {
		t.Fatal("another session lists the draft")
	}
	if p := alice.get(path); p.status != 200 || !strings.Contains(p.body, `value="Alice"`) {
		t.Fatalf("owner lost the draft: %d", p.status)
	}
}

func TestSecretsNeverInPagesOrLogs(t *testing.T) {
	e := newEnv(t, failingExporter{})
	b := e.browser()
	path := b.newDraft()
	b.post(path+"/save", mainForm("Jane", nil))
	p := b.wait(b.post(path+"/search", url.Values{"q": {"Flow"}}))
	// A fixed plain-language message is shown, never the tool's text.
	mustContain(t, p.body, msgExportOther)
	if strings.Contains(p.body, "tool failed") || strings.Contains(p.body, "client_secret") {
		t.Fatal("the exporter's own error text reached the page")
	}
	// The detail is logged at debug level only, redacted.
	if !strings.Contains(e.logs.String(), "level=DEBUG msg=\"export failed\"") {
		t.Fatalf("no debug detail logged:\n%s", e.logs.String())
	}
	// checkingTransport and the env cleanup assert absence everywhere.
}
