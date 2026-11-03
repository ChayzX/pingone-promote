package promotelegacy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/davinci"
	"pingone-promote/internal/davinci/davincitest"
	"pingone-promote/internal/tfvars"
	"pingone-promote/internal/translate"
)

const (
	sourceEnv = "00000000-0000-4000-8000-0000000000a1"
	targetEnv = "00000000-0000-4000-8000-0000000000b2"
)

const (
	loginKey  = "Login"
	mfaKey    = "MFA-20subflow"
	forgotKey = "PingOne-20SSO-20Forgot-20Password-20subflow"
	appKey    = "Web-20Login"
)

func sourceInventory(t *testing.T) *davinci.Inventory {
	t.Helper()
	_, client := davincitest.New(t, filepath.Join("..", "davinci", "testdata", "source"), sourceEnv)
	return davinci.Read(context.Background(), client, sourceEnv)
}

// targetDocument is an environment that already manages two connections
// (one with an environment-specific clientId), the brand variable with its
// own value, and the application with its own redirect URIs.
func targetDocument(t *testing.T) *tfvars.Document {
	t.Helper()
	src, err := translate.Translate(sourceInventory(t), translate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d := NewTarget(targetEnv)
	d.Parameters = map[string]string{
		"form_mfa_20subflow_n2form":    "00000000-0000-4000-8000-00000000e001",
		"themeid_mfa_20subflow_n2form": "00000000-0000-4000-8000-00000000e002",
		"target_only":                  "kept",
	}
	d.Connections["Http"] = src.Doc.Connections["Http"]
	d.Connections["Flow-20Conductor"] = src.Doc.Connections["Flow-20Conductor"]
	sso := src.Doc.Connections["PingOne"]
	props := strings.Replace(*sso.Properties, "fake-client-id", "fake-target-client-id", 1)
	// The target already maps its client secret to an existing pipeline name.
	props = strings.Replace(props, "@@secret:PingOne_DaVinci_connection_PingOne_clientSecret@@", "@@secret:PingOne_DaVinci_connection_client@@", 1)
	sso.Properties = &props
	d.Connections["PingOne"] = sso
	brand := src.Doc.Variables["brand_company"]
	v := "target-brand"
	brand.Value = &tfvars.VariableValue{String: &v}
	d.Variables["brand_company"] = brand
	app := src.Doc.Applications[appKey]
	oauth := *app.OAuth
	oauth.RedirectURIs = []string{"https://app.test.example.com/cb"}
	oauth.LogoutURIs = nil
	app.OAuth = &oauth
	app.FlowPolicies = map[string]tfvars.FlowPolicy{}
	d.Applications[appKey] = app
	d.ImportIDs.Connections["PingOne"] = "fake-target-conn-0003"
	return d
}

func plan(t *testing.T, target *tfvars.Document, sel Selection) *Result {
	t.Helper()
	src, err := TranslateFor(sourceInventory(t), target, translate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Plan(src, target, sel)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func depMap(r *Result) map[string]Dependency {
	out := map[string]Dependency{}
	for _, d := range r.Dependencies {
		out[d.Kind+":"+d.Key] = d
	}
	return out
}

func TestClosureAndClassification(t *testing.T) {
	res := plan(t, targetDocument(t), Selection{Flows: []string{loginKey}, Applications: []string{appKey}})
	if res.Blocked() {
		t.Fatalf("blocked: %+v", res.Blocks)
	}
	deps := depMap(res)
	want := map[string]struct {
		role   Role
		status Status
	}{
		"flow:" + loginKey:             {RoleSelected, StatusNew},
		"flow:" + mfaKey:               {RoleRequired, StatusNew},
		"flow:" + forgotKey:            {RoleRequired, StatusNew},
		"application:" + appKey:        {RoleSelected, StatusDiffers},
		"connection:PingOne":           {RoleReusable, StatusDiffers},
		"connection:Http":              {RoleReusable, StatusInSync},
		"connection:Flow-20Conductor":  {RoleReusable, StatusInSync},
		"variable:attempts_flow_Login": {RoleRequired, StatusNew},
		"variable:brand_company":       {RoleReusable, StatusDiffers},
	}
	for k, w := range want {
		d, ok := deps[k]
		if !ok || d.Role != w.role || d.Status != w.status {
			t.Errorf("%s: got %+v, want role %s status %s", k, d, w.role, w.status)
		}
	}
	if len(deps) != len(want) {
		t.Errorf("closure %v", deps)
	}
}

func TestMergePreservesTargetEnvironment(t *testing.T) {
	target := targetDocument(t)
	res := plan(t, target, Selection{Flows: []string{loginKey}, Applications: []string{appKey}})
	d := res.Doc
	if d.EnvironmentID != targetEnv || d.Parameters["target_only"] != "kept" || d.ImportIDs.Connections["PingOne"] != "fake-target-conn-0003" {
		t.Fatal("target environment, parameters or import_ids were not preserved")
	}
	if len(d.ImportIDs.Flows) != 0 {
		t.Fatal("source import IDs were copied into the target")
	}
	if !strings.Contains(*d.Connections["PingOne"].Properties, "fake-target-client-id") {
		t.Fatal("reusable connection that differs was overwritten")
	}
	if *d.Variables["brand_company"].Value.String != "target-brand" {
		t.Fatal("reusable variable that differs was overwritten")
	}
	if got := d.Applications[appKey].OAuth.RedirectURIs; len(got) != 1 || got[0] != "https://app.test.example.com/cb" {
		t.Fatalf("redirect URIs not preserved: %v", got)
	}
	if _, ok := d.Applications[appKey].FlowPolicies["Login-20policy"]; !ok {
		t.Fatal("policy not merged")
	}
	if d.Flows[loginKey].Tier != 3 || d.Flows[mfaKey].Tier != 2 {
		t.Fatal("tiers not carried")
	}
	if len(res.MissingParameters) != 0 {
		t.Fatalf("missing params %v", res.MissingParameters)
	}
	wantSecrets := []string{"PingOne_DaVinci_connection_client", "PingOne_DaVinci_flow_Login_n3sso_hmacKey"}
	if strings.Join(res.RequiredSecrets, ",") != strings.Join(wantSecrets, ",") {
		t.Fatalf("required secrets %v", res.RequiredSecrets)
	}
	if len(res.Problems) != 0 {
		t.Fatalf("merged document has problems: %+v", res.Problems)
	}
	// UpdateReusable overwrites differing reusable entries.
	res = plan(t, target, Selection{Flows: []string{loginKey}, UpdateReusable: true})
	if *res.Doc.Variables["brand_company"].Value.String != "acme" {
		t.Fatal("UpdateReusable did not copy the source variable")
	}
}

func TestNewApplicationDropsEnvLocalURIs(t *testing.T) {
	res := plan(t, NewTarget(targetEnv), Selection{Applications: []string{appKey}})
	if res.Blocked() {
		t.Fatalf("blocked: %+v", res.Blocks)
	}
	if o := res.Doc.Applications[appKey].OAuth; o == nil || o.RedirectURIs != nil || o.LogoutURIs != nil {
		t.Fatal("source redirect/logout URIs were copied into a new environment")
	}
	found := false
	for _, w := range res.Warnings {
		found = found || w.Code == "env_local_review"
	}
	if !found {
		t.Fatal("no env-local review warning")
	}
	if len(res.MissingParameters) != 2 {
		t.Fatalf("form/theme parameters should be missing in a new environment: %v", res.MissingParameters)
	}
}

func TestExclusionOverridesUpstream(t *testing.T) {
	res := plan(t, targetDocument(t), Selection{Flows: []string{loginKey}, Exclude: map[string]bool{"flow:" + mfaKey: true}})
	if res.Blocked() {
		t.Fatalf("blocked: %+v", res.Blocks)
	}
	deps := depMap(res)
	if deps["flow:"+mfaKey].Status != StatusExcluded {
		t.Fatal("excluded subflow not marked")
	}
	if _, ok := deps["flow:"+forgotKey]; ok {
		t.Fatal("dependencies of an excluded flow were followed")
	}
	if _, ok := res.Doc.Flows[mfaKey]; ok {
		t.Fatal("excluded flow was copied")
	}
	props := *res.Doc.Flows[loginKey].Graph.Nodes["n3sub"].Properties
	param := ExclusionParam("flow", mfaKey)
	if !strings.Contains(props, "@@param:"+param+"@@") || param != "flow_mfa_20subflow_id" {
		t.Fatalf("excluded reference not rewritten to a param:\n%s", props)
	}
	if strings.Join(res.MissingParameters, ",") != param {
		t.Fatalf("missing params %v", res.MissingParameters)
	}

	// Excluding a connection the target does not manage is blocked for a
	// structured node reference.
	empty := NewTarget(targetEnv)
	res = plan(t, empty, Selection{Flows: []string{loginKey}, Exclude: map[string]bool{"connection:PingOne": true}})
	if !res.Blocked() || res.Blocks[0].Code != "excluded_reference" || res.Doc != nil {
		t.Fatalf("blocks %+v", res.Blocks)
	}
}

func TestTierChangeIsBlockedWithMovedSnippet(t *testing.T) {
	target := targetDocument(t)
	src, err := translate.Translate(sourceInventory(t), translate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	mfa := src.Doc.Flows[mfaKey]
	mfa.Tier = 1
	target.Flows[mfaKey] = mfa
	// Without sticky tiers the source puts the MFA subflow in tier 2.
	res, err := Plan(src, target, Selection{Flows: []string{loginKey}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked() || res.Blocks[0].Code != "tier_change" || !strings.Contains(res.Blocks[0].Moved, `pingone_davinci_flow.t1["MFA-20subflow"]`) || !strings.Contains(res.Blocks[0].Moved, `pingone_davinci_flow.t2["MFA-20subflow"]`) {
		t.Fatalf("blocks %+v", res.Blocks)
	}
	// With sticky tiers (TranslateFor) the target's tier is kept and the
	// child still sits below its parent, so nothing is blocked.
	res = plan(t, target, Selection{Flows: []string{loginKey}})
	if res.Blocked() || res.Doc.Flows[mfaKey].Tier != 1 {
		t.Fatalf("sticky tier not kept: %+v", res.Blocks)
	}
	// A sticky tier that breaks ordering produces a raise suggestion.
	target.Flows[mfaKey] = func() tfvars.Flow { f := mfa; f.Tier = 3; return f }()
	res = plan(t, target, Selection{Flows: []string{loginKey}})
	if !res.Blocked() || !strings.Contains(res.Blocks[0].Message, "lower") {
		t.Fatalf("blocks %+v", res.Blocks)
	}
}

func TestRekeyIsBlocked(t *testing.T) {
	target := targetDocument(t)
	// The target manages the same company variable under a legacy key.
	target.Variables["brand"] = target.Variables["brand_company"]
	delete(target.Variables, "brand_company")
	res := plan(t, target, Selection{Flows: []string{loginKey}})
	if !res.Blocked() {
		t.Fatal("re-key not blocked")
	}
	b := res.Blocks[0]
	want := "moved {\n  from = module.davinci.pingone_davinci_variable.global[\"brand\"]\n  to   = module.davinci.pingone_davinci_variable.global[\"brand_company\"]\n}\n"
	if b.Code != "rekey" || b.Moved != want {
		t.Fatalf("block %+v", b)
	}
}

func TestUnmanagedReferenceBecomesParam(t *testing.T) {
	inv := sourceInventory(t)
	// Drop the MFA subflow from the inventory, as if its read failed.
	var flows []davinci.Flow
	for _, f := range inv.Flows {
		if f.Name != "MFA subflow" {
			flows = append(flows, f)
		}
	}
	inv.Flows = flows
	inv.Partial = append(inv.Partial, davinci.Failure{Kind: davinci.KindFlows, Reason: davinci.ReasonForbidden, Status: 403, Count: 1})
	src, err := TranslateFor(inv, NewTarget(targetEnv), translate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Plan(src, NewTarget(targetEnv), Selection{Flows: []string{loginKey}})
	if err != nil {
		t.Fatal(err)
	}
	unmanaged := 0
	for _, d := range res.Dependencies {
		if d.Status == StatusUnmanaged {
			unmanaged++
		}
	}
	if unmanaged != 1 || res.Doc == nil || !strings.Contains(*res.Doc.Flows[loginKey].Graph.Nodes["n3sub"].Properties, "@@param:flow_unresolved") {
		t.Fatalf("unmanaged=%d blocks=%+v", unmanaged, res.Blocks)
	}
}
