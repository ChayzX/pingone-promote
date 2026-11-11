package server

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/gitlab/gitlabtest"
	"pingone-promote/internal/multiexport"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/pingone/pingonetest"
)

var (
	crossOnce sync.Once
	crossBin  exportertest.Fake
)

// crossTerraformer builds the fake terraformer for the cross fixture once.
func crossTerraformer(t *testing.T) exportertest.Fake {
	t.Helper()
	crossOnce.Do(func() {
		fakeTerraformer(t) // makes fakeDir, removed by TestMain
		dir := filepath.Join(fakeDir, "cross")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		crossBin = exportertest.Build(t, dir, exportertest.FixtureDir("cross"))
	})
	if crossBin.Path == "" {
		t.Fatal("cross fake terraformer was not built")
	}
	return crossBin
}

// newCoreEnv serves DaVinci through the fake terraformer and every other
// PingOne type through the native exporter against a fake PingOne API.
func newCoreEnv(t *testing.T) *env {
	t.Helper()
	f := crossTerraformer(t)
	core := pingonetest.Start(t, pingonetest.LoadTenant(t, filepath.Join("..", "coreexport", "testdata", "tenant.json")))
	core.ClientID, core.ClientSecret = sentinelClientID, sentinelClientSecret
	exp := &multiexport.Exporter{
		DaVinci: &exporter.Runner{Config: exporter.Config{Path: f.Path, SHA256: f.SHA256, Version: f.Version}},
		NewReader: func(c exporter.Credentials) (coreexport.Reader, error) {
			cl, err := pingone.New(pingone.Credentials{ClientID: c.ClientID, ClientSecret: c.ClientSecret, WorkerEnvironmentID: c.WorkerEnvironmentID, RegionCode: c.RegionCode})
			if err != nil {
				return nil, err
			}
			cl.AuthBase, cl.APIBase = core.AuthBase(), core.APIBase()
			return cl, nil
		},
	}
	e := newEnvWith(t, exp, func(c *Config) { c.PingOne.WorkerEnvironmentID = core.WorkerEnvID })
	t.Cleanup(func() {
		if core.Writes() != 0 {
			t.Errorf("the server wrote to PingOne %d times", core.Writes())
		}
	})
	return e
}

// changedFiles lists the files a branch changed against main.
func changedFiles(gl *gitlabtest.Fake, branch string) []string {
	base, head := gl.Files("main"), gl.Files(branch)
	var out []string
	for p, c := range head {
		if base[p] != c {
			out = append(out, p)
		}
	}
	for p := range base {
		if _, ok := head[p]; !ok {
			out = append(out, p+" (deleted)")
		}
	}
	sort.Strings(out)
	return out
}

// coreDraft starts a draft, finds addr by query and adds it.
func coreDraft(t *testing.T, e *env, query, addr string) (*browser, string) {
	t.Helper()
	b := e.browser()
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Core Tester", nil))
	p := b.wait(b.post(draft+"/search", url.Values{"q": {query}}))
	mustContain(t, p.body, addr)
	if p = b.post(draft+"/pick", url.Values{"address": {addr}, "op": {"select"}}); p.status != 200 {
		t.Fatalf("pick %s: %d", addr, p.status)
	}
	return b, draft
}

func noUpstream(v url.Values) { v.Del("upstream") }

func TestCoreNotificationTemplateAloneAsMergeRequest(t *testing.T) {
	const addr = "pingone_notification_template_content.pingcli__recovery_code_template_en_Email"
	e := newCoreEnv(t)
	b, draft := coreDraft(t, e, "recovery_code_template", addr)
	p := previewDraft(t, b, draft, mainForm("Core Tester", noUpstream))
	mustContain(t, p.body, "notification template", addr, "Submit to GitLab")
	if strings.Contains(p.body, ">blocked<") {
		t.Fatal("a notification template alone must not be blocked")
	}
	id := requestID(t, p.body)
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}}))
	mustContain(t, p.body, "Submitted. A merge request is open for review.")
	branch := "pingone-promote/" + id
	got := changedFiles(e.gl, branch)
	want := []string{"definitions/pingone_notification_template_content.tf", "dev.tfvars", "imports.tf"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("changed files %v, want %v", got, want)
	}
	files, base := e.gl.Files(branch), e.gl.Files("main")
	def := files["definitions/pingone_notification_template_content.tf"]
	if strings.Count(def, "resource \"") != 1 || !strings.Contains(def, `"pingcli__recovery_code_template_en_Email"`) {
		t.Fatalf("only the selected block may be added:\n%s", def)
	}
	added := strings.TrimPrefix(files["imports.tf"], base["imports.tf"])
	if strings.Count(added, "import {") != 1 || !strings.Contains(added, "module.davinci."+addr) {
		t.Fatalf("only its conditional import may be added:\n%s", added)
	}
	if !strings.Contains(files["dev.tfvars"], `"module.davinci.`+addr+`"`) || strings.Contains(files["qa.tfvars"], addr) {
		t.Fatal("only the source overlay's import_ids gain the ID")
	}
	if len(e.gl.MRs) != 1 {
		t.Fatalf("MRs: %d", len(e.gl.MRs))
	}
}

func TestCoreDefaultSignOnPolicyNeedsImportIDs(t *testing.T) {
	const addr = "pingone_sign_on_policy.pingcli__Single_Factor"
	e := newCoreEnv(t)
	b, draft := coreDraft(t, e, "Single_Factor", addr)
	p := previewDraft(t, b, draft, mainForm("Core Tester", noUpstream))
	for _, env := range e.cfg.Rollout {
		mustContain(t, p.body, `name="i:`+env+`:`+addr+`"`)
	}
	if strings.Count(p.body, ">blocked<") != len(e.cfg.Rollout) || strings.Contains(p.body, "Submit to GitLab") {
		t.Fatalf("every rollout environment must be blocked until adopted:\n%s", p.body)
	}
	ids := map[string]string{"qa": "51000000-0000-4000-8000-0000000000a1", "stage": "51000000-0000-4000-8000-0000000000b1", "prod": "51000000-0000-4000-8000-0000000000c1"}
	form := url.Values{}
	for env, id := range ids {
		form.Set("i:"+env+":"+addr, id)
	}
	p = b.wait(b.post(draft+"/values", form))
	mustContain(t, p.body, "Submit to GitLab")
	if strings.Contains(p.body, ">blocked<") {
		t.Fatalf("still blocked after the import IDs:\n%s", p.body)
	}
	id := requestID(t, p.body)
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"branch"}}))
	mustContain(t, p.body, "Submitted to a branch.")
	if len(e.gl.MRs) != 0 {
		t.Fatal("branch only opened a merge request")
	}
	files := e.gl.Files("pingone-promote/" + id)
	for env, want := range ids {
		overlay := files[env+".tfvars"]
		if !strings.Contains(overlay, "import_ids") || !strings.Contains(overlay, `"module.davinci.`+addr+`"`) || !strings.Contains(overlay, want) {
			t.Fatalf("%s.tfvars lacks the import ID:\n%s", env, overlay)
		}
	}
}

func TestCoreApplicationWithUpstreamListsFlowPolicyAndMissingValues(t *testing.T) {
	const addr = "pingone_application.pingcli__Web-0020-Portal"
	e := newCoreEnv(t)
	b, draft := coreDraft(t, e, "Web Portal", addr)
	p := previewDraft(t, b, draft, mainForm("Core Tester", nil))
	mustContain(t, p.body, "pingone_davinci_application_flow_policy.pingcli__Web-0020-Policy", "DaVinci flow policy")
	for _, env := range e.cfg.Rollout {
		mustContain(t, p.body, `name="v:`+env+`:application_Web_0020_Portal_oidc_options_redirect_uris"`)
	}
	if strings.Contains(p.body, "Submit to GitLab") {
		t.Fatal("missing redirect URIs must block submit")
	}
}
