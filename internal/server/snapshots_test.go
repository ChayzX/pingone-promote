package server

import (
	"net/url"
	"os"
	"path/filepath"
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
	save("1-home", b.get("/").body)
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Dana Admin", nil))
	p := b.wait(b.post(draft+"/search", url.Values{"q": {"Web Portal"}}))
	b.post(draft+"/pick", url.Values{"address": {"pingone_application.pingcli__Web-0020-Portal"}, "op": {"select"}})
	save("2-new-request", b.get(draft).body)
	p = previewDraft(t, b, draft, mainForm("Dana Admin", nil))
	save("3-preview-almost-ready", p.body)
	form := url.Values{}
	for _, env := range e.cfg.Rollout {
		form.Set("v:"+env+":application_Web_0020_Portal_oidc_options_redirect_uris", "https://portal."+env+".example.com/callback")
		form.Set("v:"+env+":application_Web_0020_Portal_oidc_options_post_logout_redirect_uris", "https://portal."+env+".example.com/")
		form.Set("v:"+env+":resource_Orders_0020_API_audience", "https://api."+env+".example.com/orders")
		form.Set("i:"+env+":pingone_resource.pingcli__openid", "27000000-0000-4000-8000-0000000000"+env[:1]+"1")
		form.Set("i:"+env+":pingone_resource_scope_openid.pingcli__openid_email", "27100000-0000-4000-8000-0000000000"+env[:1]+"1")
	}
	p = b.wait(b.post(draft+"/values", form))
	mustContain(t, p.body, "Ready to submit")
	save("4-preview-ready", p.body)
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}}))
	save("5-submitted", p.body)
	save("6-requests", b.get("/requests").body)
}
