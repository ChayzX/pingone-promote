package exporter

import (
	"context"
	"pingone-promote/internal/neutralize"
	"strings"
	"testing"
)

func TestIndependentExcludedCoreReferenceNeutralization(t *testing.T) {
	const env = "11111111-1111-4111-8111-111111111111"
	const pop = "22222222-2222-4222-8222-222222222222"
	c := &CoreExporter{Inventory: coreInventoryFixture{coreObject("pingone_application", "app", "app-id", map[string]any{"population_id": pop}), coreObject("pingone_population", "population", pop, nil)}, Reader: &coreReaderFixture{reads: map[string]int{}}}
	e, err := c.SelectedExport(context.Background(), Credentials{ExportEnvironmentID: env}, []string{"pingone_application.app"}, []string{"pingone_population.population"})
	if err != nil {
		t.Fatal(err)
	}
	e.Files[ModuleDir+"/variables.tf"] = []byte("variable \"pingone_environment_id\" { type=string }\n")
	n, err := neutralize.Neutralize(&neutralize.Input{Files: e.Files, ModuleDir: e.ModuleDir, ModuleName: e.ModuleName}, neutralize.Options{SourceEnvironmentID: env, Excluded: []neutralize.Excluded{{Address: "pingone_population.population", ID: pop}}})
	if err != nil {
		t.Fatal(err)
	}
	b := n.Block("pingone_application.app")
	if b == nil {
		t.Fatal("missing app")
	}
	if strings.Contains(string(b.Text), pop) {
		t.Fatalf("excluded source ID survives neutralization: %s", b.Text)
	}
}
func TestIndependentPopulationDefaultThemeAlias(t *testing.T) {
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/population.tf": []byte("resource \"pingone_population\" \"population\" {\n theme={id=\"theme-id\"}\n}\n")}}
	cat := []Resource{{Address: Address{Type: "pingone_branding_theme", Label: "theme"}, ID: "theme-id"}, {Address: Address{Type: "pingone_branding_theme_default", Label: "default"}, ID: "theme-id"}}
	_, _, err := rewriteGraph(e, cat, map[string]bool{"pingone_branding_theme.theme": true}, nil)
	if err != nil {
		t.Fatalf("real theme object reference rejected: %v", err)
	}
}
func TestIndependentTerraformerVariableCoreReference(t *testing.T) {
	const app = "22222222-2222-4222-8222-222222222222"
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{
		ModuleDir + "/connector.tf": []byte("resource \"pingone_davinci_connector_instance\" \"connector\" {\n properties=jsonencode({clientId={value=var.davinci_connection_connector_clientId}})\n}\n"),
		ModuleDir + "/variables.tf": []byte("variable \"davinci_connection_connector_clientId\" {type=string}\n"),
		"ping-export.auto.tfvars":   []byte("davinci_connection_connector_clientId=\"" + app + "\"\n")}}
	cat := []Resource{{Address: Address{Type: "pingone_application", Label: "app"}, ID: app}}
	_, deps, err := rewriteGraph(e, cat, map[string]bool{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !deps["pingone_application.app"] {
		t.Fatalf("DaVinci variable-valued application ID does not enter core closure: %v", deps)
	}
}
func TestIndependentUnknownConnectorReferenceIsEnvironmentLocal(t *testing.T) {
	const env = "11111111-1111-4111-8111-111111111111"
	e := &Export{EnvironmentID: env, ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{
		ModuleDir + "/connector.tf": []byte("resource \"pingone_davinci_connector_instance\" \"connector\" {\n properties=jsonencode({applicationId={value=\"22222222-2222-4222-8222-222222222222\"}})\n}\n"),
		ModuleDir + "/variables.tf": []byte("variable \"pingone_environment_id\" {type=string}\n")}}
	g, _, err := rewriteGraph(e, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := neutralize.Neutralize(&neutralize.Input{Files: g.Files, ModuleDir: g.ModuleDir, ModuleName: g.ModuleName, EnvLocalVariables: g.EnvLocalVariables}, neutralize.Options{SourceEnvironmentID: env})
	if err != nil {
		t.Fatalf("unknown connector reference cannot become env input: %v", err)
	}
	if strings.Contains(string(n.Blocks[0].Text), "22222222-2222-4222-8222-222222222222") {
		t.Fatal("unknown connector ID inlined into shared HCL")
	}
}
func TestIndependentMalformedBOMProductFailsClosed(t *testing.T) {
	fixtures := inventoryFixtures()
	fixtures["/billOfMaterials"] = map[string]any{"products": []any{map[string]any{}}}
	a, _ := inventoryServer(t, fixtures)
	a.Services = nil
	_, err := a.ServiceEnabled(context.Background(), inventoryCredentials(), "davinci")
	if err == nil {
		t.Fatal("BOM product missing required type silently reports DaVinci disabled")
	}
}
func TestIndependentUnknownApplicationMapKey(t *testing.T) {
	const id = "22222222-2222-4222-8222-222222222222"
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/mfa.tf": []byte("resource \"pingone_mfa_device_policy\" \"policy\" {\n mobile={applications={\"" + id + "\"={enabled=true}}}\n}\n")}}
	got, _, err := rewriteGraph(e, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got.Files[ModuleDir+"/mfa.tf"]), id) {
		t.Fatal("unknown application ID map key remains source literal")
	}
}
func TestIndependentScopedApplicationReference(t *testing.T) {
	cat := []Resource{{Address: Address{Type: "pingone_application", Label: "app"}, ID: "app-id"}, {Address: Address{Type: "pingone_application_secret", Label: "secret"}, ID: "app-id"}}
	idx := newReferenceIndex(cat)
	a, err := idx.resolve("app-id", "scope_application_id", "pingone_group_role_assignment")
	if err != nil || a != "pingone_application.app" {
		t.Fatalf("real scoped role application reference rejected: address=%s err=%v", a, err)
	}
}
func TestIndependentScopedRoleAssignmentLabels(t *testing.T) {
	f := inventoryFixtures()
	f["/groups/object/roleAssignments"] = inventoryHAL(
		map[string]any{"id": "assignment-one", "role": map[string]any{"name": "Role"}, "scope": map[string]any{"id": "population-one", "name": "Population one", "type": "POPULATION"}},
		map[string]any{"id": "assignment-two", "role": map[string]any{"name": "Role"}, "scope": map[string]any{"id": "population-two", "name": "Population two", "type": "POPULATION"}})
	a, _ := inventoryServer(t, f)
	_, err := a.List(context.Background(), inventoryCredentials())
	if err != nil {
		t.Fatalf("distinct role scopes cannot be inventoried: %v", err)
	}
}
func TestIndependentMalformedCollectionFailsClosed(t *testing.T) {
	f := inventoryFixtures()
	f["/groups"] = map[string]any{"_embedded": map[string]any{"groups": "not-an-array"}}
	a, _ := inventoryServer(t, f)
	_, err := a.List(context.Background(), inventoryCredentials())
	if err == nil {
		t.Fatal("malformed groups collection accepted as empty")
	}
}
