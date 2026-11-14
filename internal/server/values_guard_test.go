package server

import (
	"html"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/multiexport"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/pingone/pingonetest"
)

var alertRE = regexp.MustCompile(`(?s)role="alert"[^>]*>(.*?)</p>`)

func alertText(body string) string {
	m := alertRE.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
}

// An environment's ID typed on the preview is never the ID the server
// configuration gives another environment (the source included): qa's
// pipeline would apply to prod.
func TestValuesRefuseAnotherEnvironmentsID(t *testing.T) {
	ids := map[string]string{"dev": firstUseSourceID, "qa": "22222222-2222-4222-8222-222222222222", "stage": "33333333-3333-4333-8333-333333333333", "prod": "44444444-4444-4444-8444-444444444444"}
	e := newCoreEnvWith(t, func(c *Config) { c.EnvironmentIDs = ids })
	dropLines(e, "qa.tfvars", "pingone_environment_id")
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("")))
	mustContain(t, p.body, `name="v:qa:pingone_environment_id"`)
	for _, other := range []string{"prod", "stage", "dev"} {
		p = b.wait(b.post(draft+"/values", url.Values{"v:qa:pingone_environment_id": {strings.ToUpper(ids[other])}}))
		got := alertText(p.body)
		if !strings.Contains(got, "Nothing was saved") || !strings.Contains(got, "PingOne environment ID of "+other) {
			t.Errorf("%s's ID for qa: alert = %q", other, got)
		}
		if strings.Contains(p.body, `name="v:qa:pingone_environment_id" value="`+ids[other]) {
			t.Errorf("%s's ID was kept for qa", other)
		}
	}
	// Its own ID is fine.
	p = b.wait(b.post(draft+"/values", url.Values{"v:qa:pingone_environment_id": {ids["qa"]}}))
	if got := alertText(p.body); got != "" {
		t.Errorf("qa's own ID: alert = %q", got)
	}
}

// Without environmentIds in the configuration, an ID that another
// environment's tfvars already has blocks the request.
func TestSameIDAsAnotherTfvarsBlocks(t *testing.T) {
	e := newCoreEnv(t)
	prod, _, _ := e.gl.File("main", "prod.tfvars")
	m := regexp.MustCompile(`(?m)^pingone_environment_id\s*=\s*"([^"]+)"`).FindStringSubmatch(prod)
	if m == nil {
		t.Fatal("the fixture's prod.tfvars sets no pingone_environment_id")
	}
	dropLines(e, "qa.tfvars", "pingone_environment_id")
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("")))
	form := url.Values{}
	for _, f := range valueFieldRE.FindAllStringSubmatch(p.body, -1) {
		form.Set(f[1], "noreply@"+f[2]+".example.com")
	}
	form.Set("v:qa:pingone_environment_id", m[1])
	p = b.wait(b.post(draft+"/values", form))
	if strings.Contains(p.body, "Submit to GitLab") && !strings.Contains(p.body, "same pingone_environment_id") {
		t.Fatalf("qa was given prod's ID and the request is ready to submit")
	}
	mustContain(t, p.body, "same pingone_environment_id as prod")
}

// A typed value that is a credential is refused whatever the value is
// for, and so is a credential in the title or the note.
func TestTypedCredentialsRefusedEverywhere(t *testing.T) {
	e := newCoreEnv(t)
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", notificationsOnly("")))
	f := valueFieldRE.FindStringSubmatch(p.body)
	if f == nil {
		t.Fatal("no value is asked for")
	}
	for _, v := range []string{
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789", "glpat-abcdefghij0123456789", "dXNlcjpwYXNzd29yZA==",
		"Basic dXNlcjpwYXNzd29yZA==", "Bearer abcdef0123456789abcdef0123456789", "AKIAIOSFODNN7EXAMPLE",
		"https://user:hunter2secret@smtp.example.com", "password=Sup3rS3cret!", "client_secret: 9f8e7d6c5b4a39281706f5e4d3c2b1a0",
		`{"headers":[{"name":"Authorization","value":"opaque-value-123"}]}`,
	} {
		p = b.wait(b.post(draft+"/values", url.Values{f[1]: {v}}))
		if got := alertText(p.body); !strings.Contains(got, "looks like a password, key or token") {
			t.Errorf("%s: alert = %q", v, got)
		}
		if strings.Contains(p.body, html.EscapeString(v)) || strings.Contains(e.logs.String(), v) {
			t.Errorf("%s was echoed or logged", v)
		}
	}
	// Ordinary values are still accepted.
	for _, v := range []string{"noreply@qa.example.com", "https://hooks.qa.example.com/in?source=promote", "user:name"} {
		p = b.wait(b.post(draft+"/values", url.Values{f[1]: {v}}))
		if got := alertText(p.body); got != "" {
			t.Errorf("%s: alert = %q", v, got)
		}
	}
	for field, v := range map[string]string{
		"title":       "token ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"description": "see\nAuthorization: Bearer abcdef0123456789abcdef0123456789",
	} {
		p = b.post(draft+"/save", mainForm("Core Tester", func(fv url.Values) {
			notificationsOnly("")(fv)
			fv.Set("step", "3")
			fv.Set(field, v)
		}))
		if got := alertText(p.body); !strings.Contains(got, "password, key or token") {
			t.Errorf("%s: alert = %q", field, got)
		}
	}
}

// A credential inside an ordinary attribute of the source (a password in
// a webhook's URL) is never shown, filled in or committed: the value
// becomes a secret.
func TestSourceURLPasswordBecomesSecret(t *testing.T) {
	const password, key = "Sup3rS3cretPw", "9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	f := crossTerraformer(t)
	tn := pingonetest.LoadTenant(t, filepath.Join("..", "coreexport", "testdata", "tenant.json"))
	subs := tn.Collections["subscriptions"]
	subs.Items[0]["httpEndpoint"].(map[string]any)["url"] = "https://svc:" + password + "@siem.dev.example.com/hook?apikey=" + key
	tn.Collections["subscriptions"] = subs
	core := pingonetest.Start(t, tn)
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
	b := e.browser()
	draft := b.newDraft()
	p := previewDraft(t, b, draft, mainForm("Core Tester", func(v url.Values) {
		v.Set("scope", "all")
		v.Set("confirm_all", "yes")
		v.Del("upstream")
	}))
	for _, s := range []string{password, key} {
		if strings.Contains(p.body, s) {
			t.Fatal("the preview shows the credential")
		}
	}
	mustContain(t, p.body, "looks like a credential")
	p, _ = fillReadiness(t, b, draft, p.body)
	mustContain(t, p.body, "Submit to GitLab")
	id := requestID(t, p.body)
	p = b.wait(b.post(draft+"/submit", url.Values{"mode": {"branch"}}))
	mustContain(t, p.body, "Submitted to a branch")
	files := e.gl.Files("pingone-promote/" + id)
	for n, c := range files {
		if strings.Contains(c, password) || strings.Contains(c, key) {
			t.Errorf("%s holds the credential", n)
		}
	}
	if !strings.Contains(files["customer_secrets.yml"], "http_endpoint_url") {
		t.Errorf("the webhook URL is not a secret in customer_secrets.yml:\n%s", files["customer_secrets.yml"])
	}
	if strings.Contains(e.logs.String(), password) {
		t.Error("the log holds the credential")
	}
}
