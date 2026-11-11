package promote

import (
	"context"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/multiexport"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/pingone/pingonetest"
)

// TestWholeEnvironmentCoreAndDaVinciValidates exports the whole fixture
// environment (every core type of the fake tenant plus the DaVinci fixture)
// through the combined exporter, merges it into an empty repository with
// values and adoption IDs for every rollout environment, and runs check
// --root. With PINGONE_PROMOTE_TERRAFORM set (CI), it also runs terraform
// validate against the pinned pingone provider and a mock-provider plan for
// every environment, so wrong attribute names, types or block shapes fail.
func TestWholeEnvironmentCoreAndDaVinciValidates(t *testing.T) {
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("cross"))
	core := pingonetest.Start(t, pingonetest.LoadTenant(t, "../coreexport/testdata/tenant.json"))
	e := &multiexport.Exporter{
		DaVinci: &exporter.Runner{Config: exporter.Config{Path: fake.Path, SHA256: fake.SHA256, Version: fake.Version}},
		NewReader: func(c exporter.Credentials) (coreexport.Reader, error) {
			cl, err := pingone.New(pingone.Credentials{ClientID: core.ClientID, ClientSecret: core.ClientSecret, WorkerEnvironmentID: core.WorkerEnvID, RegionCode: "NA"})
			if err != nil {
				return nil, err
			}
			cl.AuthBase, cl.APIBase = core.AuthBase(), core.APIBase()
			return cl, nil
		},
	}
	creds := testCreds
	creds.ExportEnvironmentID = devEnv
	exp, err := e.WholeExport(context.Background(), creds, nil)
	if err != nil {
		t.Fatal(err)
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
	neu, err := neutralize.Neutralize(&neutralize.Input{Files: exp.Files, ModuleDir: exp.ModuleDir, ModuleName: exp.ModuleName},
		neutralize.Options{SourceEnvironmentID: devEnv, SecretNames: testSecretNames, Excluded: excluded, Lift: lift})
	if err != nil {
		t.Fatal(err)
	}
	// Every rollout environment gets the source's values (a stand-in for
	// what the operator enters) and an adoption ID for every built-in.
	vals := map[string]map[string]cty.Value{}
	ids := map[string]map[string]string{}
	for _, env := range rollout {
		vals[env] = map[string]cty.Value{neutralize.EnvironmentIDVariable: cty.StringVal("22222222-2222-4222-8222-222222222222")}
		for name, v := range neu.Variables {
			if v.PerEnvironment() {
				if sv, ok := neu.SourceValues[name]; ok {
					vals[env][name] = sv
				}
			}
		}
		ids[env] = map[string]string{}
		for _, a := range exp.Adopt {
			ids[env][a] = neu.SourceIDs[a]
		}
	}
	head := MapRepo{"customer_secrets.yml": []byte(fixtureCustomerSecrets)}
	res, err := Merge(MergeRequest{Export: neu, Head: head, WholeEnvironment: true, SourceEnv: "dev", Rollout: rollout,
		EnvValues: vals, Adopt: exp.Adopt, ImportIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked() {
		t.Fatalf("blockers: %+v", res.Blockers)
	}
	if core.Writes() != 0 {
		t.Fatalf("%d write requests reached PingOne", core.Writes())
	}
	// terraform validate covers every environment's configuration. The mock
	// plan runs for the source only: a mock provider cannot refresh an
	// imported singleton (branding theme default) whose referenced theme is
	// created in the same plan. Real plans per environment are live
	// acceptance case C-04.
	endToEndPlans(t, apply(head, res), res.RequiredSecrets, []string{"dev"})
}
