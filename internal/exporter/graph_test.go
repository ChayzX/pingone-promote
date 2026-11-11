package exporter

import (
	"strings"
	"testing"
)

func TestGraphTypedReferencesJSONAndBusinessStrings(t *testing.T) {
	catalog := []Resource{{Address: Address{Type: "pingone_application", Label: "app"}, ID: "shared"}, {Address: Address{Type: "pingone_application_secret", Label: "secret"}, ID: "shared"}, {Address: Address{Type: "pingone_group", Label: "group"}, ID: "group-id"}}
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/pingone_davinci_flow.tf": []byte("resource \"pingone_davinci_flow\" \"flow\" {\n application_id=\"shared\"\n name=\"group-id\"\n flow_json=\"{\\\"groupId\\\":\\\"group-id\\\",\\\"description\\\":\\\"group-id\\\"}\"\n}\n")}}
	got, deps, err := rewriteGraph(e, catalog, map[string]bool{"pingone_application.app": true, "pingone_group.group": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 {
		t.Fatalf("dependencies %v", deps)
	}
	s := string(got.Files[ModuleDir+"/pingone_davinci_flow.tf"])
	if !strings.Contains(s, "pingone_application.app.id") || strings.Contains(s, "pingone_application_secret.secret.id") {
		t.Fatal(s)
	}
	if !strings.Contains(s, "pingone_group.group.id") || !strings.Contains(s, `"group-id"`) {
		t.Fatal(s)
	}
}
func TestGraphAmbiguousIDsFailClosed(t *testing.T) {
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/x.tf": []byte("resource \"pingone_application\" \"app\" {\n target_id=\"same\"\n}\n")}}
	_, _, err := rewriteGraph(e, []Resource{{Address: Address{Type: "pingone_group", Label: "a"}, ID: "same"}, {Address: Address{Type: "pingone_group", Label: "b"}, ID: "same"}}, nil, nil)
	if err == nil {
		t.Fatal("ambiguous ID accepted")
	}
}
func TestGraphMergeDetectsCollision(t *testing.T) {
	a := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{"values.tfvars": []byte("environment_id=\"one\"\n")}}
	b := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{"values.tfvars": []byte("environment_id=\"two\"\n")}}
	if _, err := mergeExports(a, b); err == nil {
		t.Fatal("conflicting values overwritten")
	}
}
func TestGraphMixedJSONExpressionPreservesDynamicAndLinksReferences(t *testing.T) {
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/flow.tf": []byte("resource \"pingone_davinci_flow\" \"flow\" {\n graph_data = {\n data=jsonencode({ applicationId={value=\"app-id\"}, description=var.description, business=\"app-id\" })\n }\n}\n")}}
	got, deps, err := rewriteGraph(e, []Resource{{Address: Address{Type: "pingone_application", Label: "app"}, ID: "app-id"}}, map[string]bool{"pingone_application.app": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got.Files[ModuleDir+"/flow.tf"])
	if !deps["pingone_application.app"] || !strings.Contains(s, "pingone_application.app.id") || !strings.Contains(s, "var.description") || !strings.Contains(s, `"app-id"`) {
		t.Fatal(s)
	}
}
func TestGraphMergeCompatibleEnvironmentVariable(t *testing.T) {
	a := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/variables.tf": []byte("variable \"pingone_environment_id\" {\n type=string\n}\n")}}
	b := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/variables.tf": []byte("variable \"pingone_environment_id\" {\n type=string\n description=\"The PingOne environment ID\"\n validation {\n condition=length(var.pingone_environment_id)>0\n error_message=\"required\"\n }\n}\n")}}
	got, err := mergeExports(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.Files[ModuleDir+"/variables.tf"]), "validation") {
		t.Fatal("validation lost")
	}
}
func TestGraphUnknownReferenceBecomesInput(t *testing.T) {
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/app.tf": []byte("resource \"pingone_application\" \"app\" {\n population_id=\"11111111-1111-4111-8111-111111111111\"\n name=\"11111111-1111-4111-8111-111111111111\"\n}\n")}}
	got, _, err := rewriteGraph(e, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got.Files[ModuleDir+"/app.tf"])
	if !strings.Contains(s, "var.pingone_application_app_population_id_") || !strings.Contains(s, `"11111111-1111-4111-8111-111111111111"`) {
		t.Fatal(s)
	}
	if len(got.Files[ModuleDir+"/variables.tf"]) == 0 || len(got.Files[ModuleName+"-module.tf"]) == 0 {
		t.Fatal("target input not wired")
	}
}
func TestGraphNestedAPIParentReference(t *testing.T) {
	idx := newReferenceIndex([]Resource{{Address: Address{Type: "pingone_application", Label: "app"}, ID: "application-id"}, {Address: Address{Type: "pingone_environment", Label: "environment"}, ID: "environment-id"}})
	deps, err := idx.rawDependencies(map[string]any{"application": map[string]any{"id": "application-id"}, "environment": map[string]any{"id": "environment-id"}, "description": "application-id"}, "pingone_application_sign_on_policy_assignment")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0] != "pingone_application.app" {
		t.Fatalf("dependencies %v", deps)
	}
}
func TestGraphGrantScopesUseResourceScopeIDs(t *testing.T) {
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/grant.tf": []byte("resource \"pingone_application_resource_grant\" \"grant\" {\n scopes=[\"scope-id\"]\n custom_resource_id=\"resource-id\"\n}\n")}}
	catalog := []Resource{{Address: Address{Type: "pingone_resource_scope", Label: "scope"}, ID: "scope-id"}, {Address: Address{Type: "pingone_resource", Label: "resource"}, ID: "resource-id"}, {Address: Address{Type: "pingone_resource_secret", Label: "secret"}, ID: "resource-id"}}
	got, deps, err := rewriteGraph(e, catalog, map[string]bool{"pingone_resource_scope.scope": true, "pingone_resource.resource": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got.Files[ModuleDir+"/grant.tf"])
	if !deps["pingone_resource_scope.scope"] || !strings.Contains(s, "pingone_resource_scope.scope.id") || !strings.Contains(s, "pingone_resource.resource.id") {
		t.Fatal(s)
	}
}
func TestGraphJSONPreservesLargeNumbersAndEscapes(t *testing.T) {
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/flow.tf": []byte("resource \"pingone_davinci_flow\" \"flow\" {\n flow_json=\"{\\\"groupId\\\":\\\"g\\\",\\\"counter\\\":9007199254740993,\\\"message\\\":\\\"quote: \\\\\\\" x\\\\n\\\"}\"\n}\n")}}
	got, _, err := rewriteGraph(e, []Resource{{Address: Address{Type: "pingone_group", Label: "group"}, ID: "g"}}, map[string]bool{"pingone_group.group": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got.Files[ModuleDir+"/flow.tf"])
	if !strings.Contains(s, "9007199254740993") || !strings.Contains(s, `quote: \" x\n`) {
		t.Fatal(s)
	}
}

func TestGraphMixedMapKeysUseApplicationReferences(t *testing.T) {
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/mfa.tf": []byte("resource \"pingone_mfa_device_policy\" \"policy\" {\n mobile={applications={\"application-id\"={enabled=var.enabled}}}\n}\n")}}
	got, deps, err := rewriteGraph(e, []Resource{{Address: Address{Type: "pingone_application", Label: "app"}, ID: "application-id"}, {Address: Address{Type: "pingone_application_secret", Label: "secret"}, ID: "application-id"}}, map[string]bool{"pingone_application.app": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got.Files[ModuleDir+"/mfa.tf"])
	if !deps["pingone_application.app"] || !strings.Contains(s, "(pingone_application.app.id)") || !strings.Contains(s, "var.enabled") {
		t.Fatal(s)
	}
}
func TestGraphMalformedJSONStringPreservesTrailingContent(t *testing.T) {
	e := &Export{ModuleDir: ModuleDir, ModuleName: ModuleName, Files: map[string][]byte{ModuleDir + "/flow.tf": []byte("resource \"pingone_davinci_flow\" \"flow\" {\n description=\"{\\\"groupId\\\":\\\"g\\\"} trailing text\"\n}\n")}}
	got, deps, err := rewriteGraph(e, []Resource{{Address: Address{Type: "pingone_group", Label: "group"}, ID: "g"}}, map[string]bool{"pingone_group.group": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 0 || !strings.Contains(string(got.Files[ModuleDir+"/flow.tf"]), "trailing text") {
		t.Fatal("ordinary string changed")
	}
}
