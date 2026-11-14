package server

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPageSnapshots walks the operator flow against the fake tenant and
// writes each page as a standalone HTML file (with the stylesheet next to
// it) for screenshots and UX review. It runs only when
// PINGONE_PROMOTE_SNAPSHOTS names an output directory.
func TestPageSnapshots(t *testing.T) {
	dir := os.Getenv("PINGONE_PROMOTE_SNAPSHOTS")
	if dir == "" {
		t.Skip("set PINGONE_PROMOTE_SNAPSHOTS to write page snapshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	css, err := webFS.ReadFile("web/static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "style.css"), css, 0o644); err != nil {
		t.Fatal(err)
	}
	save := func(name, body string) {
		body = strings.ReplaceAll(body, `href="/static/style.css"`, `href="style.css"`)
		if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := newCoreEnv(t)
	b := e.browser()
	save("01-home-empty", b.get("/").body)
	draft := b.newDraft()
	save("02-step1-what-to-copy", b.get(draft+"/scope").body)
	b.post(draft+"/save", mainForm("Dana Admin", nil))
	p := b.wait(b.post(draft+"/search", url.Values{"q": {""}}))
	b.post(draft+"/pick", url.Values{"address": {"pingone_application.pingcli__Web-0020-Portal"}, "op": {"select"}})
	save("03-step2-choose-items", b.get(draft+"/items").body)
	save("04-step3-send", b.get(draft+"/send").body)
	p = previewDraft(t, b, draft, mainForm("Dana Admin", func(v url.Values) { v.Set("title", "Web Portal from dev") }))
	save("05-preview-almost-ready", p.body)
	p = b.wait(b.post(draft+"/values", url.Values{"v:qa:no_such_value": {"x"}}))
	save("06-preview-problem", p.body)
	p, _ = fillReadiness(t, b, draft, b.get(draft+"/preview").body)
	mustContain(t, p.body, "Ready to submit")
	save("07-preview-ready", p.body)
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}, "preview": {regexp.MustCompile(`name="preview" value="([^"]+)"`).FindStringSubmatch(p.body)[1]}}))
	mustContain(t, p.body, "Submitted.")
	save("08-submitted", p.body)
	save("09-requests", b.get("/requests").body)
	save("10-home-drafts", b.get("/").body)
	save("11-not-found", b.get("/drafts/nosuchdraft").body)

	// The whole environment: the long tables.
	whole := b.newDraft()
	p = previewDraft(t, b, whole, mainForm("Dana Admin", func(v url.Values) {
		v.Set("scope", "all")
		v.Set("confirm_all", "yes")
		v.Del("upstream")
		v.Set("title", "Whole environment from dev")
	}))
	save("12-preview-whole-environment", p.body)
}
