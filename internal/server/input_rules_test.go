package server

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Limits are counted in characters, as the page's fields count them, and a
// name or title with an invisible or direction-changing character is
// refused: it would display as something other than what it is.
func TestNameAndTitleRules(t *testing.T) {
	e := newCoreEnv(t)
	b := e.browser()
	draft := b.newDraft()
	save := func(mut func(url.Values)) string {
		return alertText(b.post(draft+"/save", mainForm("Core Tester", func(v url.Values) {
			notificationsOnly("")(v)
			mut(v)
		})).body)
	}
	if got := save(func(v url.Values) { v.Set("step", "3"); v.Set("title", strings.Repeat("é", 200)) }); got != "" {
		t.Errorf("a title of 200 accented letters: %q", got)
	}
	if got := save(func(v url.Values) { v.Set("step", "3"); v.Set("title", strings.Repeat("é", 256)) }); !strings.Contains(got, "at most 255 characters") {
		t.Errorf("a title of 256 letters: %q", got)
	}
	for name, title := range map[string]string{"direction override": "Flow B ‮gnp.exe", "zero-width space": "Flow​B"} {
		if got := save(func(v url.Values) { v.Set("step", "3"); v.Set("title", title) }); !strings.Contains(got, "invisible or text-direction character") {
			t.Errorf("title with a %s: %q", name, got)
		}
	}
	if got := save(func(v url.Values) { v.Set("step", "1"); v.Set("name", strings.Repeat("名", 100)) }); got != "" {
		t.Errorf("a name of 100 CJK characters: %q", got)
	}
	if got := save(func(v url.Values) { v.Set("step", "1"); v.Set("name", "Dana‮ Admin") }); !strings.Contains(got, "invisible or text-direction character") {
		t.Errorf("a name with a direction override: %q", got)
	}
}

// A value too long to save says so instead of vanishing.
func TestOverlongValueIsReported(t *testing.T) {
	e := newCoreEnv(t)
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("")))
	f := valueFieldRE.FindStringSubmatch(p.body)
	if f == nil {
		t.Fatal("no value is asked for")
	}
	p = b.wait(b.post(draft+"/values", url.Values{f[1]: {strings.Repeat("é", 5000)}}))
	if got := alertText(p.body); !strings.Contains(got, "Nothing was saved") || !strings.Contains(got, "too long") {
		t.Errorf("alert = %q", got)
	}
}

// A choices form that does not carry a row (a page loaded before another
// tab set that row) leaves the row's choice alone.
func TestChoicesFormKeepsRowsItDoesNotCarry(t *testing.T) {
	e := newCoreEnv(t)
	b := e.browser()
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Core Tester", nil))
	b.wait(b.post(draft+"/search", url.Values{"q": {"Web Portal"}}))
	b.post(draft+"/pick", url.Values{"address": {"pingone_application.pingcli__Web-0020-Portal"}, "op": {"select"}})
	p := previewDraft(t, b, draft, mainForm("Core Tester", nil))
	rows := regexp.MustCompile(`name="(c:[^"]+)"`).FindAllStringSubmatch(p.body, -1)
	if len(rows) < 2 {
		t.Fatalf("the fixture offers %d choices; two are needed", len(rows))
	}
	first, second := rows[0][1], rows[1][1]
	p = b.wait(b.post(draft+"/choices", url.Values{first: {"keep"}, second: {""}}))
	kept := regexp.MustCompile(`name="` + regexp.QuoteMeta(first) + `"[^>]*>(?s:.*?)<option value="keep" selected`)
	if !kept.MatchString(p.body) {
		t.Fatal("the choice was not saved")
	}
	// The older page knows only the second row.
	p = b.wait(b.post(draft+"/choices", url.Values{second: {""}}))
	if !kept.MatchString(p.body) {
		t.Error("a form without the row reset its choice")
	}
	// Carrying the row with the default clears it.
	p = b.wait(b.post(draft+"/choices", url.Values{first: {""}, second: {""}}))
	if kept.MatchString(p.body) {
		t.Error("the default did not clear the choice")
	}
}

// A GitLab failure says what it means and who can fix it, and keeps the
// technical text for whoever runs the server.
func TestGitLabFailuresArePlain(t *testing.T) {
	for in, want := range map[string]string{
		"verify GitLab project: GitLab returned HTTP 401": "refused this server's access token",
		"commit request: GitLab returned HTTP 403":        "Nothing is wrong with your request",
		"verify GitLab project: GitLab returned HTTP 404": "could not find the project",
		"list branches: GitLab returned HTTP 502":         "Wait a minute and try again",
	} {
		got := plainJobError(errors.New(in))
		if !strings.Contains(got, want) || !strings.Contains(got, "(Detail: "+in+")") {
			t.Errorf("%s -> %q", in, got)
		}
	}
	if got := plainJobError(errors.New("the export tool stopped")); got != "the export tool stopped" {
		t.Errorf("another error was reworded: %q", got)
	}
}

// Two servers on one host keep separate sessions over plain HTTP.
func TestSessionCookieIsNamedPerPort(t *testing.T) {
	a := newCoreEnvWith(t, func(c *Config) { c.Listen = "127.0.0.1:8080" })
	b := newCoreEnvWith(t, func(c *Config) { c.Listen = "127.0.0.1:8081" })
	if a.srv.cookieName() == b.srv.cookieName() || !strings.HasSuffix(a.srv.cookieName(), "-8080") {
		t.Errorf("cookie names: %q and %q", a.srv.cookieName(), b.srv.cookieName())
	}
}
