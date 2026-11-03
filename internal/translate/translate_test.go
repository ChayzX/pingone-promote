package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pingone-promote/internal/davinci"
	"pingone-promote/internal/davinci/davincitest"
	"pingone-promote/internal/tfvars"
)

var update = flag.Bool("update", false, "rewrite golden files")

const sourceEnv = "00000000-0000-4000-8000-0000000000a1"

var sourceDir = filepath.Join("..", "davinci", "testdata", "source")

func inventory(t *testing.T) *davinci.Inventory {
	t.Helper()
	_, client := davincitest.New(t, sourceDir, sourceEnv)
	inv := davinci.Read(context.Background(), client, sourceEnv)
	if len(inv.Partial) != 0 {
		t.Fatalf("partial fixture inventory: %+v", inv.Partial)
	}
	return inv
}

func translateFixture(t *testing.T, opts Options) *Result {
	t.Helper()
	res, err := Translate(inventory(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs; run go test ./internal/translate -update after review", path)
	}
}

func TestTranslateGolden(t *testing.T) {
	res := translateFixture(t, Options{})
	out, err := tfvars.Write(res.Doc)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "translated.tfvars", out)
	notes, _ := json.MarshalIndent(struct {
		Secrets       []Secret
		Params        []Param
		Warnings      []Warning
		TierConflicts []TierConflict
	}{res.Secrets, res.Params, res.Warnings, res.TierConflicts}, "", "  ")
	golden(t, "translated_notes.json", append(notes, '\n'))
	if r := tfvars.Validate(res.Doc); !r.OK() {
		t.Fatalf("translated document is invalid: %+v", r.Problems)
	}
}

// No byte sequence of any fixture secret may appear in any output: the
// written tfvars, notes, warnings or secret list.
func TestNoFixtureSecretLeaks(t *testing.T) {
	canaries := map[string]bool{davinci.MaskedValue: true}
	re := regexp.MustCompile(`LEAKCANARY-[a-z-]+-[0-9]{4}`)
	entries, _ := os.ReadDir(sourceDir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(sourceDir, e.Name()))
		for _, m := range re.FindAll(b, -1) {
			canaries[string(m)] = true
		}
	}
	if len(canaries) < 7 {
		t.Fatalf("expected at least 6 canaries in the fixture, found %d", len(canaries)-1)
	}
	res := translateFixture(t, Options{})
	out, _ := tfvars.Write(res.Doc)
	all, _ := json.Marshal(res)
	blob := string(out) + string(all)
	for c := range canaries {
		if strings.Contains(blob, c) {
			t.Fatalf("secret %q leaked into translation output", c)
		}
		// Also no long fragment of a canary.
		if len(c) > 12 && strings.Contains(blob, c[len(c)-12:]) {
			t.Fatalf("fragment of %q leaked", c)
		}
	}
}

func TestEmbeddedIDRules(t *testing.T) {
	res := translateFixture(t, Options{})
	login := res.Doc.Flows["Login"]
	sso := login.Graph.Nodes["n3sso"]
	if sso.Connection == nil || *sso.Connection != "PingOne" {
		t.Fatalf("node connectionId not mapped to key: %v", sso.Connection)
	}
	props := *sso.Properties
	for _, want := range []string{`"@@connection:Http@@"`, `@@env:id@@/as`, `literal @@@@ in text`, `@@secret:PingOne_DaVinci_flow_Login_n3sso_hmacKey@@`} {
		if !strings.Contains(props, want) {
			t.Fatalf("login node properties missing %s:\n%s", want, props)
		}
	}
	sub := *login.Graph.Nodes["n3sub"].Properties
	if !strings.Contains(sub, `"@@flow:MFA-20subflow@@"`) {
		t.Fatalf("subFlowId not mapped:\n%s", sub)
	}
	mfa := res.Doc.Flows["MFA-20subflow"]
	form := *mfa.Graph.Nodes["n2form"].Properties
	if !strings.Contains(form, `@@param:form_mfa_20subflow_n2form@@`) || !strings.Contains(form, `@@param:themeid_mfa_20subflow_n2form@@`) {
		t.Fatalf("form/theme not mapped to params:\n%s", form)
	}
	if strings.Contains(form, "00000000-0000-4000-8000-00000000f00") {
		t.Fatal("form or theme ID was copied")
	}
	if !strings.Contains(*mfa.Graph.Nodes["n2sub"].Properties, `"value": -1`) {
		t.Fatal("subflow version was not set to -1")
	}
}

func TestSecretClassification(t *testing.T) {
	res := translateFixture(t, Options{})
	got := map[string]string{}
	for _, s := range res.Secrets {
		got[s.Site] = s.Reason + " " + s.Key
	}
	want := map[string]string{
		"connection:PingOne/clientSecret":    "masked PingOne_DaVinci_connection_PingOne_clientSecret",
		"connection:Custom-20API/apiKey":     "heuristic PingOne_DaVinci_connection_Custom_API_apiKey",
		"connection:Custom-20API/loginHint":  "secure PingOne_DaVinci_connection_Custom_API_loginHint",
		"variable:PingFedAuthNCreds_company": "data_type davinci_variable_PingFedAuthNCreds_value",
		"node:Login/n3sso/hmacKey":           "heuristic PingOne_DaVinci_flow_Login_n3sso_hmacKey",
	}
	for site, w := range want {
		if got[site] != w {
			t.Errorf("site %s: got %q want %q", site, got[site], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("secrets %v", got)
	}
	acks := 0
	for _, w := range res.Warnings {
		if w.Code == "secret_heuristic" && w.NeedsAck {
			acks++
		}
	}
	if acks != 2 {
		t.Fatalf("heuristic secrets should need acknowledgement: %d", acks)
	}
	if v := res.Doc.Variables["PingFedAuthNCreds_company"]; v.Secret == nil || v.Value != nil {
		t.Fatal("secret variable kept a value")
	}

	// Operator decisions: map to an existing pipeline secret, acknowledge one
	// heuristic, mark the other as not secret.
	res = translateFixture(t, Options{
		SecretNames:  map[string]string{"connection:PingOne/clientSecret": "PingOne_DaVinci_connection_client"},
		Acknowledged: map[string]bool{"node:Login/n3sso/hmacKey": true},
		NotSecret:    map[string]bool{"connection:Custom-20API/apiKey": true},
	})
	if !strings.Contains(*res.Doc.Connections["PingOne"].Properties, "@@secret:PingOne_DaVinci_connection_client@@") {
		t.Fatal("explicit secret name not used")
	}
	for _, w := range res.Warnings {
		if w.NeedsAck && w.Code == "secret_heuristic" {
			t.Fatalf("acknowledged warning still needs ack: %+v", w)
		}
	}
	if !strings.Contains(*res.Doc.Connections["Custom-20API"].Properties, "LEAKCANARY-plain-apikey-0001") {
		t.Fatal("NotSecret value was not kept literally")
	}
}

func TestSecretNameCollisionNeedsDecision(t *testing.T) {
	_, err := Translate(inventory(t), Options{SecretNames: map[string]string{
		"connection:PingOne/clientSecret":   "shared_name",
		"connection:Custom-20API/loginHint": "shared_name",
	}})
	var te *Error
	if !errors.As(err, &te) || !strings.Contains(te.Error(), "shared_name") {
		t.Fatalf("want collision error, got %v", err)
	}
	_, err = Translate(inventory(t), Options{SecretNames: map[string]string{"variable:PingFedAuthNCreds_company": "1bad"}})
	if !errors.As(err, &te) {
		t.Fatal("invalid explicit secret name accepted")
	}
}

func TestNormalization(t *testing.T) {
	res := translateFixture(t, Options{})
	login := res.Doc.Flows["Login"]
	if login.InputSchema != nil {
		t.Fatal("AUTHENTICATION flow kept input_schema")
	}
	if login.Settings == nil || *login.Settings.LogLevel != DefaultLogLevel {
		t.Fatal("log_level default not applied")
	}
	if *res.Doc.Flows["PingOne-20SSO-20Forgot-20Password-20subflow"].Settings.LogLevel != 2 {
		t.Fatal("explicit log level lost")
	}
	pol := res.Doc.Applications["Web-20Login"].FlowPolicies["Login-20policy"]
	if pol.Distributions[0].Version != -1 || pol.Distributions[0].Flow != "Login" {
		t.Fatalf("distribution %+v", pol.Distributions[0])
	}
	out, _ := tfvars.Write(res.Doc)
	for _, computed := range []string{"computed-name", "computed-n", "currentVersion", "publishedVersion", "allLinterErrors"} {
		if strings.Contains(string(out), computed) {
			t.Fatalf("computed field %q leaked into tfvars", computed)
		}
	}
}

func TestVariableKeysKeepContextsApart(t *testing.T) {
	res := translateFixture(t, Options{})
	for _, k := range []string{"attempts_company", "attempts_flow_Login", "greeting_flowInstance", "brand_company"} {
		if _, ok := res.Doc.Variables[k]; !ok {
			t.Fatalf("missing variable %s; have %v", k, keysOf(res.Doc.Variables))
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestTiersAreTopDownAndSticky(t *testing.T) {
	res := translateFixture(t, Options{})
	tiers := map[string]int{}
	for k, f := range res.Doc.Flows {
		tiers[k] = f.Tier
	}
	if tiers["Login"] != 3 || tiers["MFA-20subflow"] != 2 || tiers["PingOne-20SSO-20Forgot-20Password-20subflow"] != 1 {
		t.Fatalf("tiers %v", tiers)
	}
	// Sticky: an existing tier is kept even if a fresh assignment differs.
	res = translateFixture(t, Options{ExistingTiers: map[string]int{"PingOne-20SSO-20Forgot-20Password-20subflow": 0}})
	if res.Doc.Flows["PingOne-20SSO-20Forgot-20Password-20subflow"].Tier != 0 || len(res.TierConflicts) != 0 {
		t.Fatal("sticky lower tier not kept")
	}
	// A sticky tier that breaks ordering is reported, not moved.
	res = translateFixture(t, Options{ExistingTiers: map[string]int{"MFA-20subflow": 3}})
	if len(res.TierConflicts) != 1 || res.TierConflicts[0].Parent != "Login" || res.TierConflicts[0].Child != "MFA-20subflow" {
		t.Fatalf("conflicts %+v", res.TierConflicts)
	}
}

func TestKeyCollisionNeedsOverride(t *testing.T) {
	inv := inventory(t)
	inv.Flows[1].Name = inv.Flows[0].Name
	_, err := Translate(inv, Options{})
	var te *Error
	if !errors.As(err, &te) {
		t.Fatal("duplicate flow names accepted")
	}
	_, err = Translate(inv, Options{KeyOverrides: map[string]string{inv.Flows[1].ID: "renamed_flow"}})
	if err != nil {
		t.Fatalf("override did not resolve the collision: %v", err)
	}
}

func TestNamingRules(t *testing.T) {
	if got := VariableSecretName("PingFed AuthN-Creds"); got != "davinci_variable_PingFed_AuthN_Creds_value" {
		t.Fatal(got)
	}
	if got := ConnectionSecretName("PingOne SSO", "client.secret"); got != "PingOne_DaVinci_connection_PingOne_SSO_client_secret" {
		t.Fatal(got)
	}
	if !tfvars.IsSecretKey(NodeSecretName("Login", "n1", "hmacKey")) {
		t.Fatal("node secret name is not a valid key")
	}
}
