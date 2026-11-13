package server

import (
	"context"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/coreexport/schematenant"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/multiexport"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/pingone/pingonetest"
	"pingone-promote/internal/promote"
)

// newEnvWithReader serves DaVinci through the fake terraformer and the
// core types from r.
func newEnvWithReader(t *testing.T, r func() (coreexport.Reader, error)) *env {
	t.Helper()
	f := crossTerraformer(t)
	exp := &multiexport.Exporter{
		DaVinci:   &exporter.Runner{Config: exporter.Config{Path: f.Path, SHA256: f.SHA256, Version: f.Version}},
		NewReader: func(exporter.Credentials) (coreexport.Reader, error) { return r() },
	}
	return newEnvWith(t, exp, nil)
}

var (
	readinessFieldRE = regexp.MustCompile(`name="((v|i):([a-z]+):([^"]+))"`)
)

// fillReadiness answers every field the preview asks for with a value of
// the right shape and returns the page after saving them.
func fillReadiness(t *testing.T, b *browser, draft, body string) (page, map[string]int) {
	t.Helper()
	form := url.Values{}
	asked := map[string]int{}
	for _, m := range readinessFieldRE.FindAllStringSubmatch(body, -1) {
		name, kind, env, key := m[1], m[2], m[3], html.UnescapeString(m[4])
		asked[kind+":"+env]++
		id := schematenant.ID("readiness", 0, name)
		switch {
		case kind == "i":
			form.Set(name, id)
		case strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_ids"):
			form.Set(name, id)
		case strings.Contains(key, "email") || strings.Contains(key, "address"):
			form.Set(name, "noreply@"+env+".example.com")
		case strings.Contains(key, "port"):
			form.Set(name, "587")
		default:
			form.Set(name, env+"-value")
		}
	}
	return b.wait(b.post(draft+"/values", form)), asked
}

// wholeEnvironment previews the whole environment, fills in readiness,
// submits a merge request and validates the branch with terraform. It
// returns the preview's "Things to check".
func wholeEnvironment(t *testing.T, e *env) []string {
	t.Helper()
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", func(v url.Values) {
		v.Set("scope", "all")
		v.Set("confirm_all", "yes")
		v.Del("upstream")
	}))
	if strings.Contains(p.body, `class="problem" role="alert"`) {
		t.Fatalf("the preview reports a problem:\n%s", p.body)
	}
	var warnings []string
	if _, sec, ok := strings.Cut(p.body, "<h2>Things to check</h2>"); ok {
		sec, _, _ = strings.Cut(sec, "</ul>")
		for _, m := range regexp.MustCompile(`(?s)<li>(.*?)</li>`).FindAllStringSubmatch(sec, -1) {
			warnings = append(warnings, html.UnescapeString(m[1]))
		}
	}
	p, asked := fillReadiness(t, b, draft, p.body)
	keys := make([]string, 0, len(asked))
	for k := range asked {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("readiness asks %d fields in %s", asked[k], k)
	}
	if strings.Contains(p.body, "</span>not ready<") || !strings.Contains(p.body, "Submit to GitLab") {
		t.Fatalf("still not ready after filling every field:\n%s", p.body)
	}
	p = b.wait(b.post(draft+"/submit", map[string][]string{"mode": {"mr"}}))
	mustContain(t, p.body, "Submitted.")
	branch := "pingone-promote/" + requestID(t, p.body)
	files := e.gl.Files(branch)
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := promote.CheckRoot(context.Background(), promote.CheckOptions{Root: dir, Envs: []string{"dev", "qa", "stage", "prod"},
		CustomerSecrets: []byte(files["customer_secrets.yml"])})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("check --root on the branch: %+v", rep.Problems)
	}
	if os.Getenv("PINGONE_PROMOTE_TERRAFORM") != "" {
		schematenant.Report(t, dir, schematenant.Validate(t, dir))
	}
	return warnings
}

// TestWholeEnvironmentFromTheFixtureTenant runs the "whole environment"
// request the way a person does in the browser, with the hand-written
// fixture tenant.
func TestWholeEnvironmentFromTheFixtureTenant(t *testing.T) {
	core := pingonetest.Start(t, pingonetest.LoadTenant(t, filepath.Join("..", "coreexport", "testdata", "tenant.json")))
	e := newEnvWithReader(t, func() (coreexport.Reader, error) {
		cl, err := pingone.New(pingone.Credentials{ClientID: core.ClientID, ClientSecret: core.ClientSecret, WorkerEnvironmentID: core.WorkerEnvID, RegionCode: "NA"})
		if err != nil {
			return nil, err
		}
		cl.AuthBase, cl.APIBase = core.AuthBase(), core.APIBase()
		return cl, nil
	})
	// The fixture forbids voice phrases (listed apart, as unlicensed);
	// nothing needs checking.
	if warnings := wholeEnvironment(t, e); len(warnings) != 0 {
		t.Errorf("things to check: %q", warnings)
	}
	if core.Writes() != 0 {
		t.Fatalf("%d writes reached PingOne", core.Writes())
	}
}

// TestWholeEnvironmentFromASchemaTenant does the same with a tenant that has
// every type the catalog exports, every attribute filled.
func TestWholeEnvironmentFromASchemaTenant(t *testing.T) {
	st := schematenant.New()
	e := newEnvWithReader(t, func() (coreexport.Reader, error) { return st, nil })
	wholeEnvironment(t, e)
}
