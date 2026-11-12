package server

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The repository shapes and steps found in the first real use and its
// review: each must reach "Ready to submit" (or a clear message), never a
// dead end.

const firstUseSourceID = "11111111-1111-4111-8111-111111111111"

// dropLines removes lines starting with prefix from path on main.
func dropLines(e *env, path, prefix string) {
	src, _, _ := e.gl.File("main", path)
	var kept []string
	for _, l := range strings.Split(src, "\n") {
		if !strings.HasPrefix(l, prefix) {
			kept = append(kept, l)
		}
	}
	e.gl.Edit("main", path, strings.Join(kept, "\n"))
}

func notificationsOnly(branch string) func(url.Values) {
	return func(v url.Values) {
		v.Set("scope", "sections")
		v["family"] = []string{"notifications"}
		v.Del("upstream")
		if branch != "" {
			v.Set("branch", branch)
		}
	}
}

// fillValues fills every value field of a preview page with an example
// and previews again.
func fillValues(t *testing.T, b *browser, draft, body string, envs []string) page {
	t.Helper()
	form := url.Values{}
	for _, m := range valueFieldRE.FindAllStringSubmatch(body, -1) {
		form.Set(m[1], "noreply@"+m[2]+".example.com")
	}
	return b.wait(b.post(draft+"/values", form))
}

var valueFieldRE = regexp.MustCompile(`name="(v:([a-z]+):[A-Za-z0-9_]+)"`)

// The pipeline supplies pingone_environment_id (a CI variable): no tfvars
// sets it, the form never asks for it, and nothing writes it. Another
// environment's tfvars (a DR copy) with problems of its own does not block.
func TestEnvironmentIDFromThePipeline(t *testing.T) {
	e := newCoreEnvWith(t, func(c *Config) { c.SourceEnvironmentIDs = map[string]string{"dev": firstUseSourceID} })
	for _, f := range []string{"dev.tfvars", "qa.tfvars", "stage.tfvars", "prod.tfvars"} {
		dropLines(e, f, "pingone_environment_id")
	}
	e.gl.Edit("main", "dev_dr.tfvars", "jira_project = \"CIAM\"\nsomething_undeclared = 1\n")
	e.gl.Edit("main", "qa.tfvars", func() string { s, _, _ := e.gl.File("main", "qa.tfvars"); return s + "\nnot_a_variable = \"x\"\n" }())
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("")))
	if strings.Contains(p.body, `:pingone_environment_id"`) {
		t.Fatalf("the form asks for the pipeline's environment ID:\n%s", p.body)
	}
	p = fillValues(t, b, draft, p.body, e.cfg.Rollout)
	mustContain(t, p.body, "Submit to GitLab")
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}}))
	mustContain(t, p.body, "Submitted.")
	branch := "pingone-promote/" + requestID(t, p.body)
	for _, f := range []string{"dev.tfvars", "qa.tfvars", "stage.tfvars", "prod.tfvars"} {
		if got, _, _ := e.gl.File(branch, f); strings.Contains(got, "pingone_environment_id") {
			t.Fatalf("%s gained pingone_environment_id, which would override the pipeline's:\n%s", f, got)
		}
	}
}

// A repository without customer_secrets.yml gets one when a request needs
// secrets, instead of a blocker.
func TestMissingCustomerSecretsFileIsCreated(t *testing.T) {
	e := newCoreEnv(t)
	e.gl.Delete("main", "customer_secrets.yml")
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", func(v url.Values) {
		v.Set("scope", "sections")
		v["family"] = []string{"davinci"}
		v.Del("upstream")
	}))
	if strings.Contains(p.body, "customer_secrets.yml is missing") || strings.Contains(p.body, "cannot be updated") {
		t.Fatalf("missing customer_secrets.yml blocks the request:\n%s", p.body)
	}
	mustContain(t, p.body, "customer_secrets.yml")
}

// With a typed branch, an earlier submit of the same request is not
// "already exists", even after the default branch moved.
func TestTypedBranchResubmitAfterMainMoved(t *testing.T) {
	e := newCoreEnvWith(t, func(c *Config) { c.BranchPattern = "CIAM-[0-9]+" })
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("CIAM-55")))
	p = fillValues(t, b, draft, p.body, e.cfg.Rollout)
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}}))
	mustContain(t, p.body, "Submitted.")
	e.gl.Edit("main", "README.md", "moved\n")
	p = b.wait(b.post(draft+"/preview", url.Values{}))
	if strings.Contains(p.body, "CIAM-55 already exists") {
		t.Fatalf("the request's own branch is refused as taken:\n%s", p.body)
	}
}

// A branch pattern cannot escape its anchors.
func TestBranchPatternCannotEscapeAnchors(t *testing.T) {
	if _, err := compileBranchPattern("CIAM-1)|(.*"); err == nil {
		t.Fatal("an unbalanced pattern was accepted")
	}
	re, err := compileBranchPattern("CIAM-[0-9]+|HOTFIX-[0-9]+")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"main", "x/CIAM-1", "CIAM-1/x", "HOTFIX-1x"} {
		if re.MatchString(bad) {
			t.Errorf("%s matched", bad)
		}
	}
}

// "Whole sections" with nothing ticked stays chosen and explains itself.
func TestSectionsWithoutAnyTicked(t *testing.T) {
	e := newCoreEnv(t)
	b := e.browser()
	draft := b.newDraft()
	p := b.post(draft+"/save", mainForm("Core Tester", func(v url.Values) { v.Set("scope", "sections") }))
	mustContain(t, p.body, `value="sections" form="main" checked`, "Tick at least one section")
}

// A tfvars file that was not fmt-clean before does not block the request:
// the commit carries it formatted.
func TestUnformattedTfvarsDoNotBlock(t *testing.T) {
	e := newCoreEnv(t)
	src, _, _ := e.gl.File("main", "qa.tfvars")
	e.gl.Edit("main", "qa.tfvars", src+"\nzz_extra=\"x\"\n")
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("")))
	p = fillValues(t, b, draft, p.body, e.cfg.Rollout)
	mustContain(t, p.body, "Submit to GitLab")
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"mr"}}))
	mustContain(t, p.body, "Submitted.")
}

// A repository whose PingOne resources live in another local module is not
// given a second module: the preview says which settings to change.
func TestOtherModuleIsNotDuplicated(t *testing.T) {
	e := newCoreEnv(t)
	for p := range e.gl.Files("main") {
		if strings.HasPrefix(p, "definitions/") || p == "main.tf" {
			e.gl.Delete("main", p)
		}
	}
	e.gl.Edit("main", "main.tf", "module \"pingone\" {\n  source = \"./modules/pingone\"\n}\n")
	e.gl.Edit("main", "modules/pingone/main.tf", "resource \"pingone_population\" \"x\" {\n  environment_id = \"e\"\n  name           = \"x\"\n}\n")
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("")))
	mustContain(t, p.body, "repository.moduleName")
	if strings.Contains(p.body, "Submit to GitLab") {
		t.Fatal("a second module could be submitted")
	}
}
