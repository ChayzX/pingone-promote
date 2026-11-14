package server

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/multiexport"
	"pingone-promote/internal/request"
)

// A request on a named branch (branchPattern, no branchPrefix match) is
// listed on Requests, as a merge request and as a branch only.
func TestNamedBranchRequestsAreListed(t *testing.T) {
	for _, mode := range []string{"mr", "branch"} {
		t.Run(mode, func(t *testing.T) {
			e := newCoreEnvWith(t, func(c *Config) {
				c.BranchPattern = "CIAM-[0-9]+(-[0-9]+)?"
				c.EnvironmentIDs = map[string]string{"dev": firstUseSourceID, "qa": firstUseSourceID, "stage": firstUseSourceID, "prod": firstUseSourceID}
			})
			e.gl.Edit("CIAM-88", "README.md", "someone's own branch\n")
			b := e.browser()
			draft := b.newDraft()
			p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("CIAM-77")))
			mustContain(t, p.body, "Submit to GitLab")
			p = b.wait(b.post(draft+"/submit", url.Values{"mode": {mode}}))
			mustContain(t, p.body, "Submitted")
			p = b.get("/requests")
			if !strings.Contains(p.body, "CIAM-77") {
				t.Fatalf("CIAM-77 is not listed:\n%s", regexp.MustCompile(`(?s)<main.*</main>`).FindString(p.body))
			}
			if strings.Contains(p.body, "CIAM-88") {
				t.Error("a branch this tool did not submit is listed")
			}
		})
	}
}

// A submit from a page that shows an older preview (another tab previewed
// again meanwhile) is refused; one from the newest page goes through.
func TestSubmitFromAnOlderTabIsRefused(t *testing.T) {
	e := newEnv(t, nil)
	dropLines(e, "qa.tfvars", "davinci_variable_companyName_value")
	b := e.browser()
	draft := b.newDraft()
	whole := mainForm("QE", func(v url.Values) { v.Set("scope", "all"); v.Set("confirm_all", "yes") })
	previewDraft(t, b, draft, whole)
	tab1 := b.wait(b.post(draft+"/values", url.Values{"v:qa:davinci_variable_companyName_value": {"reviewed in tab 1"}}))
	mustContain(t, tab1.body, "Submit to GitLab")
	old := previewTokenRE.FindStringSubmatch(tab1.body)
	if old == nil {
		t.Fatal("the submit form names no preview")
	}
	tab2 := b.wait(b.post(draft+"/values", url.Values{"v:qa:davinci_variable_companyName_value": {"changed in tab 2"}}))
	mustContain(t, tab2.body, "Submit to GitLab")
	p := b.wait(b.post(draft+"/submit", url.Values{"mode": {"branch"}, "preview": {old[1]}}))
	if strings.Contains(p.body, "Submitted") {
		t.Fatal("a submit from the older tab was accepted")
	}
	mustContain(t, p.body, "That page showed an older preview")
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"branch"}}))
	mustContain(t, p.body, "Submitted")
}

// A string value keeps its spaces; a note with browser line endings is
// accepted.
func TestStringSpacesAndNoteLineEndings(t *testing.T) {
	e := newEnv(t, nil)
	dropLines(e, "qa.tfvars", "davinci_variable_companyName_value")
	b := e.browser()
	draft := b.newDraft()
	whole := mainForm("QE", func(v url.Values) {
		v.Set("scope", "all")
		v.Set("confirm_all", "yes")
		v.Set("description", "first line\r\nsecond line")
	})
	p := previewDraft(t, b, draft, whole)
	if strings.Contains(p.body, "must be at most 64") || strings.Contains(p.body, "control character") {
		t.Fatal("a two-line note was refused")
	}
	p = b.wait(b.post(draft+"/values", url.Values{"v:qa:davinci_variable_companyName_value": {"  exact text  "}}))
	mustContain(t, p.body, `value="  exact text  "`)
}

// A session cookie is Secure behind a TLS-terminating proxy.
func TestSessionCookieSecureBehindProxy(t *testing.T) {
	e := newEnvWith(t, nil, func(c *Config) { c.Exposure = ExposureBehindProxy })
	res := rawGet(t, e, "/", nil, nil)
	defer res.Body.Close()
	if len(res.Cookies()) == 0 {
		t.Fatal("no session cookie")
	}
	for _, c := range res.Cookies() {
		if !c.Secure || !strings.HasPrefix(c.Name, "__Host-") {
			t.Errorf("cookie %s: Secure=%v", c.Name, c.Secure)
		}
	}
}

// A free name is suggested however many numbered names are taken.
func TestFreeBranchAfterManyTaken(t *testing.T) {
	e := newCoreEnvWith(t, func(c *Config) { c.BranchPattern = "CIAM-[0-9]+(-[0-9]+)?" })
	e.gl.Edit("CIAM-77", "README.md", "taken\n")
	for i := 1; i <= 25; i++ {
		e.gl.Edit("CIAM-77-"+itoa(i), "README.md", "taken\n")
	}
	b := e.browser()
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Core Tester", notificationsOnly("CIAM-77")))
	p := b.wait(b.post(draft+"/save", mainForm("Core Tester", func(v url.Values) { notificationsOnly("CIAM-77")(v); v.Set("action", "preview") })))
	mustContain(t, p.body, "CIAM-77-26 is free")
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

// arrayTextExporter makes the source's companyName hold JSON-array text.
type arrayTextExporter struct{ request.Exporter }

func (q arrayTextExporter) WholeExport(ctx context.Context, c exporter.Credentials, exclude []string) (*exporter.Export, error) {
	x, err := q.Exporter.WholeExport(ctx, c, exclude)
	if err != nil {
		return x, err
	}
	for path, raw := range x.Files {
		x.Files[path] = []byte(strings.ReplaceAll(string(raw), `Example Corp (dev)`, `[ \"a\", \"b\" ]`))
	}
	return x, nil
}

// A string whose text is a JSON list is a plain field (kept exactly); a
// real list is one per line.
func TestJSONListTextIsNotAList(t *testing.T) {
	const addr = "pingone_davinci_variable.pingcli__companyName_company"
	f := fakeTerraformer(t)
	e := factsServer(t, &fakeFacts{Exporter: arrayTextExporter{&exporter.Runner{Config: exporter.Config{Version: f.Version, SHA256: f.SHA256, Path: f.Path}}},
		f: map[string]*multiexport.Facts{fillQAID: {Values: map[string]string{addr: `[ "a", "b" ]`}, Raw: map[string]map[string]any{addr: {}}}}})
	dropLines(e, "qa.tfvars", "davinci_variable_companyName_value")
	dropLines(e, "qa.tfvars", "davinci_application_Web_oauth_redirect_uris")
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("QE", func(v url.Values) { v.Set("scope", "all"); v.Set("confirm_all", "yes") }))
	if strings.Contains(p.body, `<textarea id="v:qa:davinci_variable_companyName_value"`) {
		t.Error("a string holding JSON-list text is shown one per line")
	}
	mustContain(t, p.body, `name="v:qa:davinci_variable_companyName_value" value="[ &#34;a&#34;, &#34;b&#34; ]"`, `<textarea id="v:qa:davinci_application_Web_oauth_redirect_uris"`)
	b.wait(b.post(draft+"/values", url.Values{"v:qa:davinci_variable_companyName_value": {`[ "a", "b" ]`}}))
	e.srv.drafts.mu.Lock()
	defer e.srv.drafts.mu.Unlock()
	for _, d := range e.srv.drafts.byID {
		if got := d.Values["qa"]["davinci_variable_companyName_value"]; got != `[ "a", "b" ]` {
			t.Errorf("saved as %q", got)
		}
	}
}
