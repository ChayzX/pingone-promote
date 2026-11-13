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
