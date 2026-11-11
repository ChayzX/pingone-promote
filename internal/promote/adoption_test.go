package promote

import (
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/overlay"
)

func adoptionExport(typ string) *neutralize.Result {
	addr := typ + ".existing"
	return &neutralize.Result{
		Blocks:       []neutralize.Block{{Address: addr, Type: typ, Label: "existing", File: typ + ".tf", Text: []byte("resource \"" + typ + "\" \"existing\" {\n environment_id = var.pingone_environment_id\n}\n"), Vars: []string{neutralize.EnvironmentIDVariable}}},
		Variables:    map[string]*neutralize.Variable{neutralize.EnvironmentIDVariable: {Name: neutralize.EnvironmentIDVariable, Type: "string", Kind: neutralize.KindEnvironmentID}},
		SourceValues: map[string]cty.Value{neutralize.EnvironmentIDVariable: cty.StringVal(devEnv)},
		SourceIDs:    map[string]string{addr: "source-object"}, AdoptOnly: map[string]bool{addr: true},
	}
}
func TestBuiltInRequiresEveryRolloutImport(t *testing.T) {
	exp := adoptionExport("pingone_population")
	req := MergeRequest{Head: MapRepo{}, Export: exp, SourceEnv: "dev", Rollout: []string{"qa"}, EnvValues: map[string]map[string]cty.Value{"qa": {neutralize.EnvironmentIDVariable: cty.StringVal(qaEnv)}}}
	res, err := Merge(req)
	if err != nil {
		t.Fatal(err)
	}
	full := "module.pingone.pingone_population.existing"
	// Layout defaults are authoritative rather than assuming the module name.
	full = DefaultLayout.ModuleAddress("pingone_population.existing")
	found := false
	for _, b := range res.Blockers {
		if b.Code == BlockMissingImport && b.Env == "qa" && b.Address == full {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing adoption blocker: %+v", res.Blockers)
	}
	req.ImportIDs = map[string]map[string]string{"qa": {full: "existing-qa-object"}}
	res, err = Merge(req)
	if err != nil || res.Blocked() {
		t.Fatalf("filled adoption: %v %+v", err, res)
	}
	o, err := overlay.ParseOverlay(res.Files["qa.tfvars"], "qa.tfvars")
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Values["import_ids"].GetAttr(full).AsString(); got != "existing-qa-object" {
		t.Fatal("adoption ID lost")
	}
}
func TestSingletonAdoptionUsesEachEnvironmentID(t *testing.T) {
	exp := adoptionExport("pingone_environment")
	exp.SourceIDs["pingone_environment.existing"] = "environment"
	req := MergeRequest{Head: MapRepo{}, Export: exp, SourceEnv: "dev", Rollout: []string{"qa"}, EnvValues: map[string]map[string]cty.Value{"qa": {neutralize.EnvironmentIDVariable: cty.StringVal(qaEnv)}}}
	res, err := Merge(req)
	if err != nil || res.Blocked() {
		t.Fatalf("singleton adoption: %v %+v", err, res)
	}
	text := string(res.Files["imports.tf"])
	if strings.Contains(text, "${each.value}") || !strings.Contains(text, "id       = var.pingone_environment_id") {
		t.Fatalf("wrong singleton import: %s", text)
	}
	if !strings.Contains(string(res.Files["qa.tfvars"]), "environment") {
		t.Fatal("singleton lacks conditional adoption key")
	}
}
func TestTypedSecretRootVariable(t *testing.T) {
	exp := adoptionExport("pingone_webhook")
	exp.AdoptOnly = nil
	exp.Variables["headers"] = &neutralize.Variable{Name: "headers", Type: "map(string)", Kind: neutralize.KindSecret}
	exp.Blocks[0].Vars = append(exp.Blocks[0].Vars, "headers")
	req := MergeRequest{Head: MapRepo{}, Export: exp, SourceEnv: "dev", Rollout: []string{"qa"}}
	res, err := Merge(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Files[DefaultLayout.SecretsFile]), "map(string)") {
		t.Fatalf("root secret type lost: %s", res.Files[DefaultLayout.SecretsFile])
	}
}

func TestHostedSelectedDiffersRequiresUpdate(t *testing.T) {
	head := readTree(t, "testdata/repo")
	exp := neutralized(t, exportertest.FixtureDir("dev"), flowB)
	req := MergeRequest{Head: head, Export: exp, Selected: selectedFor(flowB), SourceEnv: "dev", Rollout: []string{"qa", "stage", "prod"}, ExplicitUpdatesOnly: true}
	res, err := Merge(req)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range res.Changes {
		if c.Address == flowB {
			found = true
			if c.Action != ActionKeptDiffers {
				t.Fatalf("changed Git implicitly: %s", c.Action)
			}
		}
	}
	if !found {
		t.Fatal("selected block missing")
	}
	req.Choices = map[string]Choice{flowB: ChoiceUpdate}
	res, err = Merge(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Changes {
		if c.Address == flowB && c.Action != ActionUpdated {
			t.Fatal("explicit update ignored")
		}
	}
}
