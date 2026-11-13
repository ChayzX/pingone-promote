package server

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var rowAddrRE = regexp.MustCompile(`<code class="small">([^<]+)</code>`)

// rows maps each listed item's address to its table row.
func rows(body string) map[string]string {
	out := map[string]string{}
	for _, r := range strings.Split(body, "<tr>")[1:] {
		r, _, _ = strings.Cut(r, "</tr>")
		if m := rowAddrRE.FindStringSubmatch(r); m != nil {
			out[html.UnescapeString(m[1])] = r
		}
	}
	return out
}

// In a whole-environment copy an item that differs from Git keeps its Git
// version by default; the preview offers update (and keep), and update
// brings in the source version.
func TestWholeEnvironmentOffersUpdateForDifferingItems(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Jane Doe", func(v url.Values) {
		v.Set("scope", "all")
		v.Set("confirm_all", "yes")
		v.Del("upstream")
	}))
	addr, row := "", ""
	for a, r := range rows(p.body) {
		if strings.Contains(r, "Git version kept") && strings.Contains(r, "whole environment") {
			addr, row = a, r
			break
		}
	}
	if addr == "" {
		t.Fatalf("no item differs from Git in the fixtures:\n%s", p.body)
	}
	if !strings.Contains(row, `name="c:`+addr+`"`) || !strings.Contains(row, `value="update"`) {
		t.Fatalf("the kept row %s offers no update:\n%s", addr, row)
	}
	if strings.Contains(row, `value="exclude"`) {
		t.Fatalf("the kept row %s (not a dependency) offers exclude:\n%s", addr, row)
	}
	p = b.wait(b.post(draft+"/choices", url.Values{"c:" + addr: {"exclude"}}))
	mustContain(t, p.body, "Only an item your selection depends on can be left out here")
	p = b.wait(b.post(draft+"/choices", url.Values{"c:" + addr: {"update"}}))
	row, ok := rows(p.body)[addr]
	if !ok {
		t.Fatalf("%s is not listed after choosing update", addr)
	}
	mustContain(t, row, "updated", `value="update" selected`)
	t.Logf("%s: updated on request", addr)
}

// A values form can set only what the request asks for (never another
// environment's ID or an undeclared variable), and what was entered stays
// editable.
func TestValuesAreLimitedAndEditable(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Jane Doe", nil))
	selectByID(t, b, draft, flowBID, flowB)
	p := previewDraft(t, b, draft, mainForm("Jane Doe", nil))
	p = b.wait(b.post(draft+"/choices", url.Values{"c:" + subflowAddr: {"exclude"}}))
	mustContain(t, p.body, `name="v:qa:`+subflowIDVar+`"`)

	for _, tampered := range []url.Values{
		{"v:qa:pingone_environment_id": {"00000000-0000-4000-8000-000000000001"}},
		{"v:qa:totally_unknown_var": {"hello"}},
		{"i:qa:pingone_population.pingcli__Nope": {"00000000-0000-4000-8000-000000000001"}},
	} {
		p = b.post(draft+"/values", tampered)
		mustContain(t, p.body, "Nothing was saved: this request has no value named")
	}

	p = b.wait(b.post(draft+"/values", url.Values{"v:qa:" + subflowIDVar: {"f2000000000000000000000000000099"}}))
	mustContain(t, p.body, "already entered", `value="f2000000000000000000000000000099"`)
	// Corrected, then cleared: asked for again.
	p = b.wait(b.post(draft+"/values", url.Values{"v:qa:" + subflowIDVar: {"f2000000000000000000000000000003"}}))
	mustContain(t, p.body, `value="f2000000000000000000000000000003"`)
	p = b.wait(b.post(draft+"/values", url.Values{"v:qa:" + subflowIDVar: {""}}))
	if strings.Contains(p.body, `value="f2000000000000000000000000000003"`) {
		t.Fatal("a cleared value is still shown")
	}
	mustContain(t, p.body, `name="v:qa:`+subflowIDVar+`"`)
}

// After a request that referred to the subflow by ID variable is merged,
// choosing "update" for it copies it, and each environment adopts the
// object whose ID Git already holds instead of creating another.
func TestUpdateOnAReferencedItemAdoptsByKnownID(t *testing.T) {
	e := newEnv(t, nil)
	b := e.browser()
	ids := map[string]string{"qa": "f2000000000000000000000000000003", "stage": "f3000000000000000000000000000003", "prod": "f4000000000000000000000000000003"}

	// 1. Flow B with the subflow left out: an ID variable per environment.
	draft := b.newDraft()
	b.post(draft+"/save", mainForm("Jane Doe", nil))
	selectByID(t, b, draft, flowBID, flowB)
	previewDraft(t, b, draft, mainForm("Jane Doe", nil))
	b.wait(b.post(draft+"/choices", url.Values{"c:" + subflowAddr: {"exclude"}}))
	vals := url.Values{}
	for env, id := range ids {
		vals.Set("v:"+env+":"+subflowIDVar, id)
	}
	p := b.wait(b.post(draft+"/values", vals))
	id := requestID(t, p.body)
	b.wait(b.post(draft+"/submit", url.Values{"mode": {"branch"}}))
	e.gl.Merge("pingone-promote/" + id)

	// 2. Flow B again without dependencies; the subflow is only referred
	// to. Choosing update copies it.
	draft = b.newDraft()
	noDeps := func(v url.Values) { v.Del("upstream") }
	b.post(draft+"/save", mainForm("Jane Doe", noDeps))
	selectByID(t, b, draft, flowBID, flowB)
	p = previewDraft(t, b, draft, mainForm("Jane Doe", noDeps))
	row, ok := rows(p.body)[subflowAddr]
	if !ok || !strings.Contains(row, `value="update"`) {
		t.Fatalf("the subflow row offers no update:\n%s", row)
	}
	p = b.wait(b.post(draft+"/choices", url.Values{"c:" + subflowAddr: {"update"}}))
	row = rows(p.body)[subflowAddr]
	mustContain(t, row, "added")
	mustContain(t, p.body, "already exists in", "it is imported there, not created")
	id = requestID(t, p.body)
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"branch"}}))
	mustContain(t, p.body, "Submitted")
	files := e.gl.Files("pingone-promote/" + id)
	for env, want := range ids {
		re := regexp.MustCompile(`"module\.[a-z_-]+\.` + regexp.QuoteMeta(subflowAddr) + `"\s*=\s*"` + want + `"`)
		if !re.MatchString(files[env+".tfvars"]) {
			t.Errorf("%s does not import the existing subflow %s:\n%s", env, want, files[env+".tfvars"])
		}
	}
}

// With every environment's PingOne ID configured, the preview fills in
// what the environments already have: their own ID where a tfvars file
// lacks it, and values read from them (here the fixture tenant stands for
// every environment). Filled values stay editable and are marked.
func TestValuesFilledInFromTheEnvironments(t *testing.T) {
	e := newCoreEnvWith(t, func(c *Config) {
		c.EnvironmentIDs = map[string]string{"dev": firstUseSourceID, "qa": firstUseSourceID, "stage": firstUseSourceID, "prod": firstUseSourceID}
	})
	dropLines(e, "qa.tfvars", "pingone_environment_id")
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("")))
	if strings.Contains(p.body, `name="v:qa:pingone_environment_id" value=""`) {
		t.Fatal("qa's environment ID is asked although it is configured")
	}
	mustContain(t, p.body, "filled in from what the environments already have", "Filled in from what qa has now", `value="`+firstUseSourceID+`"`)
	got := map[string]bool{}
	for _, m := range regexp.MustCompile(`Filled in from what (\w+) has now; check it\.[^<]*<code class="small">([^<]+)</code>`).FindAllStringSubmatch(p.body, -1) {
		got[m[1]+" "+m[2]] = true
	}
	for _, want := range []string{"qa pingone_environment_id", "prod notification_settings_environment_from_email_address"} {
		if !got[want] {
			t.Errorf("not filled in: %s (filled: %v)", want, got)
		}
	}
	// Changing a filled value removes the mark.
	p = b.wait(b.post(draft+"/values", url.Values{"v:qa:pingone_environment_id": {"22222222-2222-4222-8222-222222222222"}}))
	if strings.Contains(p.body, "Filled in from what qa has now; check it. The PingOne environment ID") {
		t.Fatal("a value the person changed is still marked as filled in")
	}
}
