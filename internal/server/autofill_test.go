package server

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/multiexport"
	"pingone-promote/internal/request"
)

// fakeFacts serves what each environment (by ID) has.
type fakeFacts struct {
	request.Exporter
	f   map[string]*multiexport.Facts
	err error
}

func (q *fakeFacts) EnvironmentFacts(ctx context.Context, c exporter.Credentials) (*multiexport.Facts, error) {
	return q.f[c.ExportEnvironmentID], q.err
}

const (
	fillQAID   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	fillProdID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func factsServer(t *testing.T, q *fakeFacts) *env {
	e := newEnvWith(t, nil, func(c *Config) {
		c.EnvironmentIDs = map[string]string{"qa": fillQAID, "prod": fillProdID}
	})
	e.srv.exporter = plainErrors{inner: q, log: e.srv.log, redact: e.srv.redact}
	return e
}

// A DaVinci variable whose value carries a token inside JSON is never
// filled in (it would be shown, then refused by request validation).
func TestAutofillSkipsSecretsInsideJSON(t *testing.T) {
	const dv = "davinci_variable_cfg_value"
	const addr = "pingone_davinci_variable.pingcli__cfg_company"
	for _, val := range []string{
		`{"auth":"Bearer abcdefghijklmnopqrstuvwxyz0123"}`,
		`{"k":"ghp_abcdefghijklmnopqrstuvwxyz0123456789"}`,
		`{"basic":"dXNlcjpwYXNzd29yZDEyMw=="}`,
		`["Bearer abcdefghijklmnopqrstuvwxyz0123"]`,
	} {
		e := factsServer(t, &fakeFacts{f: map[string]*multiexport.Facts{fillQAID: {Values: map[string]string{addr: val}, Raw: map[string]map[string]any{addr: {}}}}})
		d := &Draft{ID: "x", RequestID: "20261113-000000-abcdef", RequestedBy: "QE", Source: "dev", Whole: true, ConfirmAll: true}
		res := &request.Result{
			Blockers:  map[string][]request.BlockerOut{"qa": {{Code: "missing_value", Var: dv}}},
			Variables: map[string]request.VariableOut{dv: {Kind: "env_local", Resource: addr, Attribute: "value"}},
		}
		e.srv.autofill(context.Background(), d, res, func(string) {})
		if d.Values["qa"][dv] != "" {
			t.Errorf("a secret-looking value was filled in: %.14s…", val)
		}
		if _, err := e.srv.buildRequest(d, true); err != nil {
			t.Errorf("the draft is refused after filling in: %v", err)
		}
	}
	// An ordinary object is filled in.
	val := `{"CSRauth":true,"region":"us"}`
	e := factsServer(t, &fakeFacts{f: map[string]*multiexport.Facts{fillQAID: {Values: map[string]string{addr: val}, Raw: map[string]map[string]any{addr: {}}}}})
	d := &Draft{ID: "x", RequestID: "20261113-000000-abcdef", RequestedBy: "QE", Source: "dev", Whole: true, ConfirmAll: true}
	res := &request.Result{
		Blockers:  map[string][]request.BlockerOut{"qa": {{Code: "missing_value", Var: dv}}},
		Variables: map[string]request.VariableOut{dv: {Kind: "env_local", Resource: addr, Attribute: "value"}},
	}
	e.srv.autofill(context.Background(), d, res, func(string) {})
	if d.Values["qa"][dv] != val {
		t.Errorf("an ordinary object value was not filled in: %q", d.Values["qa"][dv])
	}
}

// A read that fails says why and does not claim anything was filled in.
func TestAutofillReadErrorReport(t *testing.T) {
	const v = "notification_settings_environment_from_email_address"
	e := factsServer(t, &fakeFacts{err: context.DeadlineExceeded})
	d := &Draft{ID: "x", RequestID: "20261113-000000-abcdef", RequestedBy: "QE", Source: "dev", Whole: true, ConfirmAll: true}
	res := &request.Result{
		Blockers:  map[string][]request.BlockerOut{"qa": {{Code: "missing_value", Var: v}}},
		Variables: map[string]request.VariableOut{v: {Kind: "env_local", Resource: "pingone_notification_settings.pingcli__notification_settings", Attribute: "from.address"}},
	}
	e.srv.autofill(context.Background(), d, res, func(string) {})
	r := strings.Join(d.FillReport, " | ")
	if !strings.Contains(r, "qa: could not be read (the read took too long)") || strings.Contains(r, "was filled in") || strings.Contains(r, "deadline") {
		t.Errorf("report: %s", r)
	}
}

// A filled-in value the person clears is not filled in again.
func TestClearedFilledValueStaysClear(t *testing.T) {
	const key = "notification_settings_environment_from_email_address"
	e := newCoreEnvWith(t, func(c *Config) {
		c.EnvironmentIDs = map[string]string{"dev": firstUseSourceID, "qa": firstUseSourceID, "stage": firstUseSourceID, "prod": firstUseSourceID}
	})
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("")))
	field := regexp.MustCompile(`name="v:prod:` + key + `" value="([^"]*)"`)
	if m := field.FindStringSubmatch(p.body); m == nil || m[1] == "" {
		t.Fatalf("prod's value was not filled in first")
	}
	p = b.wait(b.post(draft+"/values", url.Values{"v:prod:" + key: {""}}))
	if m := field.FindStringSubmatch(p.body); m != nil && m[1] != "" {
		t.Fatalf("the cleared value was filled in again: %q", m[1])
	}
	mustContain(t, p.body, `name="v:prod:`+key+`"`)
	// Typed by the person: kept, not marked.
	p = b.wait(b.post(draft+"/values", url.Values{"v:prod:" + key: {"ops@prod.example.com"}}))
	mustContain(t, p.body, `value="ops@prod.example.com"`)
}

// A pattern that requires the number suffix still gets a suggestion; one
// without a suffix gets none that changes the ticket number.
func TestBranchSuggestionKeepsTheTicket(t *testing.T) {
	for _, c := range []struct{ pattern, typed, taken, want, notWant string }{
		{"CIAM-[0-9]+-[0-9]+", "CIAM-77-1", "CIAM-77-1", "CIAM-77-2 is free", ""},
		{"CIAM-[0-9]+", "CIAM-77", "CIAM-77", "already exists", "is free"},
		{"[A-Z][A-Z0-9]+-[0-9]+", "AB1-5", "AB1-5", "already exists", "is free"},
	} {
		t.Run(c.pattern, func(t *testing.T) {
			e := newCoreEnvWith(t, func(cf *Config) { cf.BranchPattern = c.pattern; cf.BranchExample = "" })
			e.gl.Edit(c.taken, "README.md", "someone else's\n")
			b := e.browser()
			draft := b.newDraft()
			b.post(draft+"/save", mainForm("Core Tester", notificationsOnly(c.typed)))
			p := b.wait(b.post(draft+"/save", mainForm("Core Tester", func(v url.Values) { notificationsOnly(c.typed)(v); v.Set("action", "preview") })))
			mustContain(t, p.body, c.want)
			if c.notWant != "" && strings.Contains(p.body, c.notWant) {
				t.Errorf("suggested a name: %s", regexp.MustCompile(`The Git branch [^<]*`).FindString(p.body))
			}
		})
	}
}

// A string variable keeps its text exactly (key order, spacing, &), so a
// filled-in DaVinci object is written as the environment has it.
func TestStringVariablesKeepTheirText(t *testing.T) {
	const dv = "davinci_variable_cfg_value"
	text := `{ "z": "a&b",  "a": 1 }`
	d := &Draft{preview: &preview{result: &request.Result{Variables: map[string]request.VariableOut{dv: {Type: "string"}, "urls": {Type: "list(string)"}}}}}
	var got string
	if err := json.Unmarshal(encodeFor(d, dv, text), &got); err != nil || got != text {
		t.Fatalf("string variable: %q (%v)", got, err)
	}
	if got := string(encodeFor(d, "urls", `["https://a.example.com"]`)); got != `["https://a.example.com"]` {
		t.Fatalf("list variable: %s", got)
	}
}

// Before the first preview is stored, the types noted from its result are
// used, so the second pass encodes values as the page later does (the
// preview is not out of date on arrival).
func TestTypesKnownBeforeThePreviewIsStored(t *testing.T) {
	const dv = "davinci_variable_cfg_value"
	d := &Draft{}
	if !d.noteTypes(&request.Result{Variables: map[string]request.VariableOut{dv: {Type: "string"}}}) {
		t.Fatal("a new type is not reported")
	}
	var got string
	if err := json.Unmarshal(encodeFor(d, dv, `{"a": 1}`), &got); err != nil || got != `{"a": 1}` {
		t.Fatalf("encoded %q (%v)", got, err)
	}
	if d.noteTypes(&request.Result{Variables: map[string]request.VariableOut{dv: {Type: "string"}}}) {
		t.Fatal("a known type is reported as new")
	}
}
