package promote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/coreexport/schematenant"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/multiexport"
	"pingone-promote/internal/neutralize"
)

func schemaTenantInventory(t *testing.T) (*schematenant.Tenant, *coreexport.Inventory) {
	t.Helper()
	st := schematenant.New()
	inv, err := coreexport.Read(context.Background(), st, devEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Skipped) > 0 {
		t.Errorf("skipped types: %v", inv.Skipped)
	}
	if len(st.Missing) > 0 {
		t.Errorf("paths no definition answers: %v", st.Missing)
	}
	return st, inv
}

// TestSchemaTenantEveryTypeIsRead: every API-exported type yields objects,
// so the tests below cover all of them.
func TestSchemaTenantEveryTypeIsRead(t *testing.T) {
	_, inv := schemaTenantInventory(t)
	got := map[string]int{}
	for _, o := range inv.Objects {
		got[o.Type]++
	}
	for _, d := range catalog.All() {
		if d.Exporter == catalog.ExporterAPI && got[d.Type] == 0 {
			t.Errorf("%s: no objects generated", d.Type)
		}
	}
	t.Logf("%d objects of %d types", len(inv.Objects), len(got))
}

// TestSchemaTenantRendersAndValidates renders every generated object,
// built-in ones included (as a person selecting them would), and runs
// terraform validate against the pinned provider.
func TestSchemaTenantRendersAndValidates(t *testing.T) {
	_, inv := schemaTenantInventory(t)
	ids := map[string]string{}
	exported := map[string]bool{}
	var all []string
	for _, o := range inv.Objects {
		ids[o.ID] = o.Address()
		exported[o.Address()] = true
		all = append(all, o.Address())
	}
	// Built-ins referenced from outside become ID variables: leave them out
	// of Exported so both paths render.
	for _, o := range inv.Objects {
		if o.Builtin {
			delete(exported, o.Address())
		}
	}
	r, err := coreexport.Render(coreexport.RenderInput{Inventory: inv, Addresses: all, Exported: exported, IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string][]byte{}
	for p, b := range r.Files {
		if p == coreexport.ImportsFile || p == coreexport.ValuesFile {
			continue // the module alone is validated
		}
		files[p] = b
	}
	md := neutralize.DefaultExportModuleDir
	vars := string(files[md+"/"+coreexport.VariablesFile])
	vars += fmt.Sprintf("\nvariable %q {\n  type = string\n}\n", neutralize.EnvironmentIDVariable)
	files[md+"/"+coreexport.VariablesFile] = []byte(vars)
	files[md+"/versions.tf"] = []byte("terraform {\n  required_providers {\n    pingone = { source = \"pingidentity/pingone\", version = \"" + catalog.SchemaVersion() + "\" }\n  }\n}\n")
	writeTree(t, dir, files)
	if os.Getenv("PINGONE_PROMOTE_TERRAFORM") == "" {
		t.Skip("set PINGONE_PROMOTE_TERRAFORM to run terraform validate")
	}
	schematenant.Report(t, filepath.Join(dir, md), schematenant.Validate(t, filepath.Join(dir, md)))
}

// TestSchemaTenantWholeEnvironment runs the generated tenant through the
// whole pipeline (combined export, neutralizer, merge, check --root) and
// validates the resulting repository.
func TestSchemaTenantWholeEnvironment(t *testing.T) {
	st := schematenant.New()
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("cross"))
	e := &multiexport.Exporter{
		DaVinci:   &exporter.Runner{Config: exporter.Config{Path: fake.Path, SHA256: fake.SHA256, Version: fake.Version}},
		NewReader: func(exporter.Credentials) (coreexport.Reader, error) { return st, nil },
	}
	creds := testCreds
	creds.ExportEnvironmentID = devEnv
	exp, err := e.WholeExport(context.Background(), creds, nil)
	if err != nil {
		t.Fatal(err)
	}
	for a := range exp.Referenced {
		t.Logf("referenced, not exported: %s", a)
	}
	if len(exp.Adopt) != 0 {
		t.Errorf("a whole export adopts built-ins: %v", exp.Adopt)
	}
	lift := append([]neutralize.LiftRule(nil), neutralize.DefaultLiftRules...)
	for _, d := range catalog.All() {
		for _, a := range d.EnvLocal {
			lift = append(lift, neutralize.LiftRule{ResourceType: d.Type, Attribute: a})
		}
	}
	var excluded []neutralize.Excluded
	for a, id := range exp.Referenced {
		excluded = append(excluded, neutralize.Excluded{Address: a, ID: id})
	}
	neu, err := neutralize.Neutralize(exp.NeutralizeInput(),
		neutralize.Options{SourceEnvironmentID: devEnv, SecretNames: testSecretNames, Excluded: excluded, Lift: lift})
	if err != nil {
		t.Fatal(err)
	}
	vals := map[string]map[string]cty.Value{}
	for _, env := range rollout {
		vals[env] = map[string]cty.Value{neutralize.EnvironmentIDVariable: cty.StringVal("22222222-2222-4222-8222-222222222222")}
		for name, v := range neu.Variables {
			if v.PerEnvironment() {
				if sv, ok := neu.SourceValues[name]; ok {
					vals[env][name] = sv
				}
			}
		}
	}
	head := MapRepo{"customer_secrets.yml": []byte(fixtureCustomerSecrets)}
	res, err := Merge(MergeRequest{Export: neu, Head: head, WholeEnvironment: true, SourceEnv: "dev", Rollout: rollout, EnvValues: vals})
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked() {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	repo := apply(head, res)
	dir := t.TempDir()
	writeTree(t, dir, repo)
	rep, err := CheckRoot(context.Background(), CheckOptions{Root: dir, Envs: []string{"dev", "qa", "stage", "prod"}, CustomerSecrets: repo["customer_secrets.yml"]})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("check: %+v", rep.Problems)
	}
	if os.Getenv("PINGONE_PROMOTE_TERRAFORM") == "" {
		t.Skip("set PINGONE_PROMOTE_TERRAFORM to run terraform validate")
	}
	schematenant.Report(t, dir, schematenant.Validate(t, dir))
}
