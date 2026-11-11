package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

type coreInventoryFixture []SourceResource

func (f coreInventoryFixture) List(context.Context, Credentials) ([]SourceResource, error) {
	return f, nil
}

type coreReaderFixture struct{ reads map[string]int }

func (f *coreReaderFixture) Read(_ context.Context, c Credentials, rs []SourceResource) (*Export, error) {
	e := &Export{EnvironmentID: c.ExportEnvironment(), ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{}}
	for _, r := range rs {
		f.reads[r.String()]++
		file := hclwrite.NewEmptyFile()
		b := file.Body().AppendNewBlock("resource", []string{r.Type, r.Label})
		for _, k := range sortedKeys(r.Values) {
			if v, ok := r.Values[k].(string); ok {
				b.Body().SetAttributeValue(k, cty.StringVal(v))
			}
		}
		p := ModuleDir + "/" + r.Type + ".tf"
		e.Files[p] = append(e.Files[p], file.Bytes()...)
	}
	return e, nil
}
func coreObject(typ, label, id string, values map[string]any) SourceResource {
	return SourceResource{Resource: Resource{Address: Address{Type: typ, Label: label}, Name: label, ID: id}, ImportID: "environment/" + id, Values: values}
}
func assertCoreAddresses(t *testing.T, e *Export, want ...string) {
	t.Helper()
	got, err := Addresses(e)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
}
func TestCoreDependencyClosureAndExclusions(t *testing.T) {
	inv := coreInventoryFixture{
		coreObject("pingone_application", "app", "app-id", map[string]any{"sign_on_policy_id": "policy-id", "name": "app-id"}),
		coreObject("pingone_sign_on_policy", "policy", "policy-id", map[string]any{"mfa_device_policy_id": "mfa-id"}),
		coreObject("pingone_mfa_device_policy", "mfa", "mfa-id", map[string]any{}),
	}
	for _, tc := range []struct {
		name     string
		exclude  []string
		selected bool
		want     []string
	}{{"closure", nil, false, []string{"pingone_application.app", "pingone_sign_on_policy.policy", "pingone_mfa_device_policy.mfa"}}, {"excluded", []string{"pingone_sign_on_policy.*"}, false, []string{"pingone_application.app"}}, {"selected", nil, true, []string{"pingone_application.app"}}} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &coreReaderFixture{reads: map[string]int{}}
			c := &CoreExporter{Inventory: inv, Reader: reader}
			var e *Export
			var err error
			if tc.selected {
				e, err = c.SelectedExport(context.Background(), Credentials{}, []string{"pingone_application.app"}, tc.exclude)
			} else {
				e, err = c.ScopedExport(context.Background(), Credentials{}, []string{"pingone_application.app"}, tc.exclude)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertCoreAddresses(t, e, tc.want...)
			for a, n := range reader.reads {
				if n != 1 {
					t.Fatalf("%s imported %d times", a, n)
				}
			}
			if !regexp.MustCompile(`name\s*=\s*"app-id"`).Match(e.Files[ModuleDir+"/pingone_application.tf"]) {
				t.Fatal("business string was rewritten")
			}
		})
	}
}
func TestCoreWholeExcludesBeforeReadAndRejectsUsers(t *testing.T) {
	reader := &coreReaderFixture{reads: map[string]int{}}
	c := &CoreExporter{Inventory: coreInventoryFixture{coreObject("pingone_application", "app", "a", nil), coreObject("pingone_group", "group", "g", nil)}, Reader: reader}
	e, err := c.WholeExport(context.Background(), Credentials{}, []string{"pingone_application.*"})
	if err != nil {
		t.Fatal(err)
	}
	assertCoreAddresses(t, e, "pingone_group.group")
	if reader.reads["pingone_application.app"] != 0 {
		t.Fatal("excluded resource imported")
	}
	c.Inventory = coreInventoryFixture{coreObject("pingone_user", "person", "u", nil)}
	if _, err = c.CatalogAllIDs(context.Background(), Credentials{}); err == nil {
		t.Fatal("user inventory accepted")
	}
}

type compositeDVFixture struct{ objects coreInventoryFixture }

func (f *compositeDVFixture) Catalog(_ context.Context, _ Credentials, _ bool) ([]Resource, error) {
	var r []Resource
	for _, v := range f.objects {
		r = append(r, v.Resource)
	}
	return r, nil
}
func (f *compositeDVFixture) CatalogAllIDs(ctx context.Context, c Credentials) ([]Resource, error) {
	return f.Catalog(ctx, c, true)
}
func (f *compositeDVFixture) WholeExport(ctx context.Context, c Credentials, x []string) (*Export, error) {
	var s []string
	for _, r := range f.objects {
		if !matchesAny(x, r.String()) {
			s = append(s, r.String())
		}
	}
	return f.SelectedExport(ctx, c, s, x)
}
func (f *compositeDVFixture) ScopedExport(ctx context.Context, c Credentials, s, x []string) (*Export, error) {
	return f.SelectedExport(ctx, c, s, x)
}
func (f *compositeDVFixture) SelectedExport(ctx context.Context, c Credentials, s, x []string) (*Export, error) {
	var rs []SourceResource
	for _, r := range f.objects {
		for _, a := range s {
			if a == r.String() && !matchesAny(x, a) {
				rs = append(rs, r)
			}
		}
	}
	return (&coreReaderFixture{reads: map[string]int{}}).Read(ctx, c, rs)
}
func TestCompositeCrossProviderCycleAndLongClosure(t *testing.T) {
	inv := coreInventoryFixture{}
	for i := 0; i < 7; i++ {
		next := fmt.Sprintf("group-%d", i+1)
		if i == 6 {
			next = "flow-id"
		}
		inv = append(inv, coreObject("pingone_group", fmt.Sprintf("g%d", i), fmt.Sprintf("group-%d", i), map[string]any{"parent_id": next}))
	}
	inv[0].Values["flow_id"] = "flow-id"
	dv := &compositeDVFixture{objects: coreInventoryFixture{coreObject("pingone_davinci_flow", "flow", "flow-id", map[string]any{"group_id": "group-0"})}}
	reader := &coreReaderFixture{reads: map[string]int{}}
	c := &Composite{Core: &CoreExporter{Inventory: inv, Reader: reader}, DaVinci: dv}
	e, err := c.ScopedExport(context.Background(), Credentials{}, []string{"pingone_davinci_flow.flow"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pingone_davinci_flow.flow"}
	for i := 0; i < 7; i++ {
		want = append(want, fmt.Sprintf("pingone_group.g%d", i))
	}
	assertCoreAddresses(t, e, want...)
	for a, n := range reader.reads {
		if n != 1 {
			t.Fatalf("%s imported %d times", a, n)
		}
	}
	if !strings.Contains(string(e.Files[ModuleDir+"/pingone_davinci_flow.tf"]), "pingone_group.g0.id") {
		t.Fatal("DaVinci to core reference not linked")
	}
	if !strings.Contains(string(e.Files[ModuleDir+"/pingone_group.tf"]), "pingone_davinci_flow.flow.id") {
		t.Fatal("core to DaVinci reference not linked")
	}
}
func TestCompositeNoDependenciesAndCoreOnly(t *testing.T) {
	c := &Composite{Core: &CoreExporter{Inventory: coreInventoryFixture{coreObject("pingone_application", "app", "a", map[string]any{"group_id": "g"}), coreObject("pingone_group", "group", "g", nil)}, Reader: &coreReaderFixture{reads: map[string]int{}}}}
	e, err := c.SelectedExport(context.Background(), Credentials{}, []string{"pingone_application.app"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertCoreAddresses(t, e, "pingone_application.app")
}
func TestCoreConfigurationFamiliesSelectedDeterministically(t *testing.T) {
	types := []string{"pingone_application", "pingone_resource", "pingone_resource_scope", "pingone_application_resource_grant", "pingone_sign_on_policy", "pingone_sign_on_policy_action", "pingone_population", "pingone_group", "pingone_schema_attribute", "pingone_password_policy", "pingone_mfa_device_policy", "pingone_identity_provider", "pingone_gateway", "pingone_branding_theme", "pingone_notification_template_content", "pingone_form", "pingone_agreement", "pingone_language", "pingone_risk_policy", "pingone_authorize_api_service", "pingone_verify_policy", "pingone_credential_type"}
	var inv coreInventoryFixture
	var seeds []string
	for i, typ := range types {
		r := coreObject(typ, fmt.Sprintf("source_%02d", i), fmt.Sprintf("source-id-%02d", i), map[string]any{"name": fmt.Sprintf("Configuration %02d", i)})
		inv = append(inv, r)
		seeds = append(seeds, r.String())
	}
	reader := &coreReaderFixture{reads: map[string]int{}}
	c := &CoreExporter{Inventory: inv, Reader: reader}
	e, err := c.SelectedExport(context.Background(), Credentials{}, seeds, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertCoreAddresses(t, e, seeds...)
	first, err := c.CatalogAllIDs(context.Background(), Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	for l, r := 0, len(inv)-1; l < r; l, r = l+1, r-1 {
		inv[l], inv[r] = inv[r], inv[l]
	}
	second, err := c.CatalogAllIDs(context.Background(), Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("catalog order or source identity changed with inventory order")
	}
}

type disabledDVInventory struct{ coreInventoryFixture }

func (disabledDVInventory) ServiceEnabled(context.Context, Credentials, string) (bool, error) {
	return false, nil
}

type forbiddenDVSource struct{ compositeDVFixture }

func (*forbiddenDVSource) CatalogAllIDs(context.Context, Credentials) ([]Resource, error) {
	return nil, fmt.Errorf("unlicensed DaVinci was queried")
}
func TestCompositeUnlicensedDaVinciIsNotQueried(t *testing.T) {
	c := &Composite{Core: &CoreExporter{Inventory: disabledDVInventory{coreInventoryFixture{coreObject("pingone_application", "app", "app-id", nil)}}, Reader: &coreReaderFixture{reads: map[string]int{}}}, DaVinci: &forbiddenDVSource{}}
	e, err := c.WholeExport(context.Background(), Credentials{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertCoreAddresses(t, e, "pingone_application.app")
	if _, err := c.SelectedExport(context.Background(), Credentials{}, []string{"pingone_davinci_flow.flow"}, nil); err == nil {
		t.Fatal("unlicensed flow selection accepted")
	}
}
func TestCoreAdoptionMetadata(t *testing.T) {
	c := &CoreExporter{Inventory: coreInventoryFixture{coreObject("pingone_population", "default", "p", map[string]any{"default": true}), coreObject("pingone_administrator_security", "settings", "env", nil)}, Reader: &coreReaderFixture{reads: map[string]int{}}}
	e, err := c.WholeExport(context.Background(), Credentials{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !e.AdoptOnly["pingone_population.default"] || !e.AdoptOnly["pingone_administrator_security.settings"] {
		t.Fatalf("adoption metadata lost: %v", e.AdoptOnly)
	}
}

type coreSchemaReader struct {
	schema []byte
	state  []byte
}

func (r coreSchemaReader) Read(_ context.Context, c Credentials, rs []SourceResource) (*Export, error) {
	var doc struct {
		Values struct {
			Root struct {
				Resources []stateResource `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal(r.state, &doc); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	imports := map[string]string{}
	for _, r := range rs {
		want[r.String()] = true
		imports[r.String()] = r.ImportID
	}
	var chosen []stateResource
	for _, r := range doc.Values.Root.Resources {
		if want[r.Address] {
			chosen = append(chosen, r)
		}
	}
	doc.Values.Root.Resources = chosen
	state, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	e, err := RenderProviderState(r.schema, state, imports)
	if e != nil {
		e.EnvironmentID = c.ExportEnvironment()
	}
	return e, err
}
func TestCorePinnedSchemaConfigurationFamilies(t *testing.T) {
	schema, err := os.ReadFile("exportertest/fixtures/core/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile("exportertest/fixtures/core/state.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Values struct {
			Root struct {
				Resources []stateResource `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal(state, &doc); err != nil {
		t.Fatal(err)
	}
	var inv coreInventoryFixture
	var want []string
	for _, r := range doc.Values.Root.Resources {
		inv = append(inv, coreObject(r.Type, "fixture", r.Values["id"].(string), r.Values))
		want = append(want, r.Address)
	}
	c := &CoreExporter{Inventory: inv, Reader: coreSchemaReader{schema, state}}
	e, err := c.WholeExport(context.Background(), Credentials{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertCoreAddresses(t, e, want...)
	if len(want) != 22 {
		t.Fatalf("fixture covered %d resource families", len(want))
	}
	for _, p := range []string{ModuleName + "-module.tf", ModuleName + "-variables.tf", ModuleName + "-imports.tf", ModuleDir + "/versions.tf"} {
		if len(e.Files[p]) == 0 {
			t.Fatalf("missing canonical module file %s", p)
		}
	}
}

func TestCoreBuiltInTypeClassificationIsResourceSpecific(t *testing.T) {
	e := &Export{}
	rs := []SourceResource{
		{Resource: Resource{Address: Address{Type: "pingone_identity_provider", Label: "partner"}}, Values: map[string]any{"type": "OPENID_CONNECT"}},
		{Resource: Resource{Address: Address{Type: "pingone_resource", Label: "openid"}}, Values: map[string]any{"type": "OPENID_CONNECT"}},
	}
	markCoreAdoption(e, rs)
	if e.AdoptOnly[rs[0].String()] {
		t.Fatal("ordinary OIDC identity provider marked built-in")
	}
	if !e.AdoptOnly[rs[1].String()] {
		t.Fatal("built-in OIDC resource not adopted")
	}
}
