package exporter

import (
	"encoding/json"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"strings"
	"testing"

	"pingone-promote/internal/neutralize"
)

// This fixture catches three realistic regressions: copying sensitive state
// values into generated files, retaining provider-generated secrets, and
// failing to map PingOne environment IDs to the shared input variable.
func TestRenderProviderStateSensitiveFieldsAndEnvironmentReference(t *testing.T) {
	schema := []byte(`{"format_version":"1.0","provider_schemas":{"registry.terraform.io/pingidentity/pingone":{"resource_schemas":{"pingone_application":{"version":0,"block":{"attributes":{"environment_id":{"type":"string","required":true},"name":{"type":"string","required":true},"client_secret":{"type":"string","computed":true,"sensitive":true},"client_id":{"type":"string","computed":true},"client_secret_value":{"type":"string","required":true,"sensitive":true}}}}}}}}`)
	state := []byte(`{"values":{"root_module":{"resources":[{"address":"pingone_application.z_app","mode":"managed","type":"pingone_application","name":"z_app","values":{"environment_id":"11111111-1111-4111-8111-111111111111","name":"Example","client_secret":"GENERATED-SECRET-SENTINEL","client_id":"computed-id","client_secret_value":"CONFIGURED-SECRET-SENTINEL"}}]}}}`)
	exp, err := RenderProviderState(schema, state, map[string]string{"pingone_application.z_app": "11111111-1111-4111-8111-111111111111/abc"})
	if err != nil {
		t.Fatal(err)
	}
	resource := string(exp.Files[ModuleDir+"/pingone_application.tf"])
	vars := string(exp.Files[ModuleDir+"/variables.tf"])
	all := flattenFiles(exp.Files)
	for _, want := range []string{`environment_id`, `var.pingone_environment_id`, `name`, `"Example"`, `client_secret_value`, `var.pingone_application_z_app_client_secret_value`} {
		if !strings.Contains(resource, want) {
			t.Errorf("resource file missing %q:\n%s", want, resource)
		}
	}
	if strings.Contains(resource, "client_secret =") || strings.Contains(resource, "client_id =") {
		t.Errorf("computed/generated fields were emitted:\n%s", resource)
	}
	if !strings.Contains(vars, "variable \"pingone_application_z_app_client_secret_value\"") || !strings.Contains(vars, "sensitive = true") {
		t.Errorf("sensitive variable declaration missing:\n%s", vars)
	}
	if strings.Contains(all, "CONFIGURED-SECRET-SENTINEL") || strings.Contains(all, "GENERATED-SECRET-SENTINEL") {
		t.Fatal("raw secret value escaped into export files")
	}
	if !strings.Contains(all, `to = module.ping-export.pingone_application.z_app`) || !strings.Contains(all, `id = "11111111-1111-4111-8111-111111111111/abc"`) {
		t.Fatalf("import metadata missing:\n%s", all)
	}
}

// This catches renderers that lose configured false/zero/empty values, emit
// absent optional nulls, or sort output according to provider API order.
func TestRenderProviderStatePreservesConfiguredZeroValuesAndSorts(t *testing.T) {
	schema := []byte(`{"provider_schemas":{"registry.terraform.io/pingidentity/pingone":{"resource_schemas":{"pingone_application":{"block":{"attributes":{"environment_id":{"type":"string","required":true},"enabled":{"type":"bool","optional":true},"priority":{"type":"number","optional":true},"labels":{"type":["list","string"],"optional":true},"description":{"type":"string","optional":true}}}}}}}}`)
	state := []byte(`{"values":{"root_module":{"resources":[{"address":"pingone_application.z_app","mode":"managed","type":"pingone_application","values":{"environment_id":"11111111-1111-4111-8111-111111111111","enabled":false,"priority":0,"labels":[],"description":null}},{"address":"pingone_application.a_app","mode":"managed","type":"pingone_application","values":{"environment_id":"11111111-1111-4111-8111-111111111111","enabled":true}}]}}}`)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(exp.Files[ModuleDir+"/pingone_application.tf"])
	compact := strings.Join(strings.Fields(got), " ")
	for _, want := range []string{"enabled = false", "priority = 0", "labels = []"} {
		if !strings.Contains(compact, want) {
			t.Errorf("configured value %q missing:\n%s", want, got)
		}
	}
	if strings.Contains(got, "description") {
		t.Errorf("null optional field emitted:\n%s", got)
	}
	if strings.Index(got, `resource "pingone_application" "a_app"`) > strings.Index(got, `resource "pingone_application" "z_app"`) {
		t.Errorf("resources are not sorted by address:\n%s", got)
	}
}

func TestRenderProviderStateRejectsUnknownConfiguredSchema(t *testing.T) {
	schema := []byte(`{"provider_schemas":{"registry.terraform.io/pingidentity/pingone":{"resource_schemas":{"pingone_application":{"block":{"attributes":{"environment_id":{"type":"string","required":true},"name":{"type":"string","optional":true}}}}}}}}`)
	state := []byte(`{"values":{"root_module":{"resources":[{"address":"pingone_application.demo","mode":"managed","type":"pingone_application","values":{"environment_id":"11111111-1111-4111-8111-111111111111","name":"demo","future_field":"x"}}]}}}`)
	if _, err := RenderProviderState(schema, state, nil); err == nil {
		t.Fatal("expected unknown configured state field to fail")
	}
}

// This catches loss of repeated nested blocks and typed object attributes,
// both common in PingOne policy resources.
func TestRenderProviderStateNestedBlocksAndObjects(t *testing.T) {
	schema := []byte(`{"provider_schemas":{"registry.terraform.io/pingidentity/pingone":{"resource_schemas":{"pingone_application":{"block":{"attributes":{"environment_id":{"type":"string","required":true},"settings":{"optional":true,"nested_type":{"nesting_mode":"single","attributes":{"enabled":{"type":"bool","optional":true},"labels":{"type":["list","string"],"optional":true}}}}},"block_types":{"redirect_uri":{"nesting_mode":"list","block":{"attributes":{"uri":{"type":"string","required":true}}}}}}}}}}}`)
	state := []byte(`{"values":{"root_module":{"resources":[{"address":"pingone_application.demo","mode":"managed","type":"pingone_application","values":{"environment_id":"11111111-1111-4111-8111-111111111111","settings":{"enabled":false,"labels":["a","b"]},"redirect_uri":[{"uri":"https://one.example/callback"},{"uri":"https://two.example/callback"}]}}]}}}`)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(exp.Files[ModuleDir+"/pingone_application.tf"])
	for _, want := range []string{`redirect_uri {`, `"https://one.example/callback"`, `"https://two.example/callback"`, `settings`, `labels`, `false`, `"a"`, `"b"`} {
		if !strings.Contains(got, want) {
			t.Errorf("nested value %q missing:\n%s", want, got)
		}
	}
}

func flattenFiles(files map[string][]byte) string {
	var b strings.Builder
	for name, body := range files {
		b.WriteString(name)
		b.Write(body)
	}
	return b.String()
}

// Terraform state sensitivity can be more specific than the provider schema.
// A renderer that only consults schema sensitive flags publishes these values.
func TestRenderProviderStateStateSensitivityAndTypedSecrets(t *testing.T) {
	attrs := map[string]any{
		"environment_id": map[string]any{"type": "string", "required": true},
		"switch":         map[string]any{"type": "bool", "required": true},
		"weights":        map[string]any{"type": []any{"map", "number"}, "optional": true, "sensitive": true},
		"object":         map[string]any{"type": []any{"object", map[string]any{"public": "string", "private": []any{"list", "string"}}}, "optional": true},
		"nested": map[string]any{"optional": true, "nested_type": map[string]any{"nesting_mode": "list", "attributes": map[string]any{
			"public":    map[string]any{"type": "string", "required": true},
			"private":   map[string]any{"type": []any{"set", "string"}, "optional": true},
			"generated": map[string]any{"type": "string", "computed": true, "sensitive": true},
		}}},
	}
	values := map[string]any{"environment_id": "source-env", "switch": false, "weights": map[string]any{"x": 42}, "object": map[string]any{"public": "visible", "private": []any{"RAW-OBJECT-SECRET"}}, "nested": []any{map[string]any{"public": "visible-too", "private": []any{"RAW-NESTED-SECRET"}, "generated": "RAW-GENERATED-SECRET"}}}
	sensitive := map[string]any{"switch": true, "object": map[string]any{"private": true}, "nested": []any{map[string]any{"private": true}}}
	schema, state := renderFixture(t, attrs, nil, values, sensitive)
	exp, err := RenderProviderState(schema, state, map[string]string{"pingone_application.demo": "environment/application/compound-id"})
	if err != nil {
		t.Fatal(err)
	}
	all := flattenFiles(exp.Files)
	for _, secret := range []string{"RAW-OBJECT-SECRET", "RAW-NESTED-SECRET", "RAW-GENERATED-SECRET"} {
		if strings.Contains(all, secret) {
			t.Errorf("sensitive value %s escaped", secret)
		}
	}
	assertValidExport(t, exp)
	resource := string(exp.Files[ModuleDir+"/pingone_application.tf"])
	if strings.Contains(resource, `"generated"`) {
		t.Errorf("generated nested secret emitted: %s", resource)
	}
	for _, file := range []string{ModuleDir + "/variables.tf", ModuleName + "-variables.tf"} {
		want := map[string]cty.Type{
			"pingone_environment_id":                    cty.String,
			"pingone_application_demo_switch":           cty.Bool,
			"pingone_application_demo_weights":          cty.Map(cty.Number),
			"pingone_application_demo_object_private":   cty.List(cty.String),
			"pingone_application_demo_nested_0_private": cty.Set(cty.String),
		}
		assertVariableTypes(t, exp.Files[file], want)
	}
	if !strings.Contains(all, `id = "environment/application/compound-id"`) {
		t.Error("compound import metadata was lost")
	}
}

// Map nesting must preserve labels and arbitrary map keys, including keys that
// would otherwise collide after sanitizing a required-secret variable name.
func TestRenderProviderStateNestedMapKeysAndSecretNameCollisions(t *testing.T) {
	attrs := map[string]any{"settings": map[string]any{"optional": true, "nested_type": map[string]any{"nesting_mode": "map", "attributes": map[string]any{
		"token": map[string]any{"type": "string", "required": true, "sensitive": true},
	}}}}
	blocks := map[string]any{"endpoint": map[string]any{"nesting_mode": "map", "block": map[string]any{"attributes": map[string]any{"url": map[string]any{"type": "string", "required": true}}}}}
	values := map[string]any{"settings": map[string]any{"a-b": map[string]any{"token": "SECRET-A"}, "a_b": map[string]any{"token": "SECRET-B"}}, "endpoint": map[string]any{"en-US": map[string]any{"url": "https://example.test"}}}
	schema, state := renderFixture(t, attrs, blocks, values, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertValidExport(t, exp)
	body := string(exp.Files[ModuleDir+"/pingone_application.tf"])
	for _, want := range []string{`"a-b"`, `"a_b"`, `endpoint "en-US"`} {
		if !strings.Contains(body, want) {
			t.Errorf("lost map key %q: %s", want, body)
		}
	}
	parsed, _ := hclwrite.ParseConfig(exp.Files[ModuleDir+"/variables.tf"], "vars.tf", hcl.InitialPos)
	names := map[string]bool{}
	for _, b := range parsed.Body().Blocks() {
		if b.Type() == "variable" {
			names[b.Labels()[0]] = true
		}
	}
	if len(names) != 3 {
		t.Errorf("expected environment and two independent secret variables, got %v", names)
	}
}

// Invalid metadata must fail the whole export. Silently skipping it would make
// a preview look complete while omitting adoption or unsupported configuration.
func TestRenderProviderStateRejectsMalformedInputWithoutLeakingValues(t *testing.T) {
	attrs := map[string]any{"name": map[string]any{"type": "string", "optional": true}}
	schema, state := renderFixture(t, attrs, nil, map[string]any{"name": "visible"}, nil)
	for name, imports := range map[string]map[string]string{
		"empty id":           {"pingone_application.demo": ""},
		"unknown address":    {"pingone_application.other": "id"},
		"expression address": {"pingone_application.demo[0]": "id"},
		"newline id":         {"pingone_application.demo": "id\nsecret"},
	} {
		t.Run(name, func(t *testing.T) {
			if exp, err := RenderProviderState(schema, state, imports); err == nil || exp != nil {
				t.Fatalf("expected closed failure, got export=%v err=%v", exp, err)
			}
		})
	}
	for name, attr := range map[string]any{
		"unknown schema type": map[string]any{"type": "dynamic", "optional": true},
		"wrong secret type":   map[string]any{"type": "number", "optional": true, "sensitive": true},
	} {
		t.Run(name, func(t *testing.T) {
			s, v := renderFixture(t, map[string]any{"secret": attr}, nil, map[string]any{"secret": "NEVER-ECHO-THIS-SECRET"}, nil)
			exp, err := RenderProviderState(s, v, nil)
			if err == nil || exp != nil {
				t.Fatal("expected invalid type to fail")
			}
			if strings.Contains(err.Error(), "NEVER-ECHO-THIS-SECRET") {
				t.Fatal("error leaked secret input")
			}
		})
	}
}

// JSON float64 decoding rounds large integer state values before HCL output.
func TestRenderProviderStatePreservesNumberPrecision(t *testing.T) {
	schema, state := renderFixture(t, map[string]any{"number": map[string]any{"type": "number", "required": true}}, nil, map[string]any{"number": json.Number("9007199254740993")}, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flattenFiles(exp.Files), "9007199254740993") {
		t.Fatal("state number was rounded")
	}
}

func renderFixture(t *testing.T, attrs, blocks, values map[string]any, sensitive any) ([]byte, []byte) {
	t.Helper()
	return renderResourceFixture(t, "pingone_application", attrs, blocks, values, sensitive)
}

func renderResourceFixture(t *testing.T, typ string, attrs, blocks, values map[string]any, sensitive any) ([]byte, []byte) {
	t.Helper()
	schema, err := json.Marshal(map[string]any{"provider_schemas": map[string]any{"registry.terraform.io/pingidentity/pingone": map[string]any{"resource_schemas": map[string]any{typ: map[string]any{"block": map[string]any{"attributes": attrs, "block_types": blocks}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(map[string]any{"values": map[string]any{"root_module": map[string]any{"resources": []any{map[string]any{"address": typ + ".demo", "mode": "managed", "type": typ, "values": values, "sensitive_values": sensitive}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return schema, state
}

func assertValidExport(t *testing.T, exp *Export) {
	t.Helper()
	for name, src := range exp.Files {
		if strings.HasSuffix(name, ".tf") {
			if _, diags := hclsyntax.ParseConfig(src, name, hcl.InitialPos); diags.HasErrors() {
				t.Errorf("invalid HCL in %s: %s\n%s", name, diags, src)
			}
		}
	}
}

func assertVariableTypes(t *testing.T, src []byte, want map[string]cty.Type) {
	t.Helper()
	f, diags := hclsyntax.ParseConfig(src, "variables.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	got := map[string]cty.Type{}
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "variable" {
			continue
		}
		a, ok := b.Body.Attributes["type"]
		if !ok {
			t.Error("variable has no type")
			continue
		}
		typ, d := typeexpr.TypeConstraint(a.Expr)
		if d.HasErrors() {
			t.Error(d)
			continue
		}
		got[b.Labels[0]] = typ
		if b.Labels[0] != "pingone_environment_id" {
			if _, ok := b.Body.Attributes["default"]; ok {
				t.Error("secret variable has a default")
			}
			a, ok := b.Body.Attributes["sensitive"]
			if !ok {
				t.Error("secret variable is not sensitive")
			} else if v, d := a.Expr.Value(nil); d.HasErrors() || !v.RawEquals(cty.True) {
				t.Error("secret variable is not sensitive")
			}
		}
	}
	for name, typ := range want {
		if v, ok := got[name]; !ok || !v.Equals(typ) {
			t.Errorf("variable %s type got %v, want %v", name, v, typ)
		}
	}
}

// Two distinct typed-object paths can have the same dotted spelling. Their
// secret references must remain independently assignable.
func TestRenderProviderStateTypedObjectPathsDoNotAlias(t *testing.T) {
	attrs := map[string]any{"object": map[string]any{"optional": true, "type": []any{"object", map[string]any{"a.b": "string", "a": []any{"object", map[string]any{"b": "string"}}}}}}
	values := map[string]any{"object": map[string]any{"a.b": "SECRET-ONE", "a": map[string]any{"b": "SECRET-TWO"}}}
	mask := map[string]any{"object": map[string]any{"a.b": true, "a": map[string]any{"b": true}}}
	schema, state := renderFixture(t, attrs, nil, values, mask)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, d := hclsyntax.ParseConfig(exp.Files[ModuleDir+"/variables.tf"], "variables.tf", hcl.InitialPos)
	if d.HasErrors() {
		t.Fatal(d)
	}
	if got := len(f.Body.(*hclsyntax.Body).Blocks); got != 3 {
		t.Fatalf("expected environment and two distinct secret variables, got %d", got)
	}
}

// These nested shapes are copied from the public v1.24.0 provider schema
// (descriptions removed). Localized maps, notification objects, and mobile
// application secrets exercise the framework attribute form used in production.
func TestRenderProviderStatePinnedProviderShapes(t *testing.T) {
	tests := []struct {
		name, attr, schema, value string
		secrets                   []string
		wants                     []string
	}{
		{name: "localized map", attr: "footer_localized", schema: `{"nested_type":{"attributes":{"content":{"nested_type":{"attributes":{"input_text":{"type":"string","required":true}},"nesting_mode":"map"},"required":true},"content_type":{"type":"string","required":true},"default_language":{"type":"string","required":true},"enabled":{"type":"bool","optional":true,"computed":true}},"nesting_mode":"single"},"optional":true,"computed":true}`, value: `{"content":{"en-US":{"input_text":"Welcome"},"fr":{"input_text":"Bienvenue"}},"content_type":"TEXT","default_language":"en-US","enabled":false}`, secrets: []string{}, wants: []string{"\"en-US\"", "\"Bienvenue\"", "false"}},
		{name: "notification email", attr: "email", schema: `{"nested_type":{"attributes":{"body":{"type":"string","required":true},"character_set":{"type":"string","optional":true,"computed":true},"content_type":{"type":"string","optional":true,"computed":true},"from":{"nested_type":{"attributes":{"address":{"type":"string","optional":true,"computed":true},"name":{"type":"string","optional":true,"computed":true}},"nesting_mode":"single"},"optional":true},"reply_to":{"nested_type":{"attributes":{"address":{"type":"string","optional":true,"computed":true},"name":{"type":"string","optional":true,"computed":true}},"nesting_mode":"single"},"optional":true},"subject":{"type":"string","required":true}},"nesting_mode":"single"},"optional":true}`, value: `{"body":"Hello ${user.name} %{\"safe\"}","subject":"Reset password","from":{"address":"noreply@example.test","name":"Example"},"reply_to":null}`, secrets: []string{}, wants: []string{"$${user.name}", "%%{", "\"Reset password\"", "\"noreply@example.test\""}},
		{name: "nested application secrets", attr: "integrity_detection", schema: `{"nested_type":{"attributes":{"apple_app_attest_settings":{"nested_type":{"attributes":{"enabled":{"type":"bool","optional":true,"computed":true}},"nesting_mode":"single"},"optional":true},"cache_duration":{"nested_type":{"attributes":{"amount":{"type":"number","required":true},"units":{"type":"string","optional":true,"computed":true}},"nesting_mode":"single"},"optional":true},"enabled":{"type":"bool","optional":true,"computed":true},"excluded_platforms":{"type":["set","string"],"optional":true},"google_play":{"nested_type":{"attributes":{"decryption_key":{"type":"string","optional":true,"sensitive":true},"service_account_credentials_json":{"type":"string","optional":true,"sensitive":true},"verification_key":{"type":"string","optional":true,"sensitive":true},"verification_type":{"type":"string","required":true}},"nesting_mode":"single"},"optional":true}},"nesting_mode":"single"},"optional":true,"computed":true}`, value: `{"cache_duration":{"amount":0,"units":"MINUTES"},"enabled":false,"excluded_platforms":[],"google_play":{"verification_type":"INTERNAL","decryption_key":"GOOGLE-KEY-SENTINEL","verification_key":"VERIFY-KEY-SENTINEL","service_account_credentials_json":null}}`, secrets: []string{"GOOGLE-KEY-SENTINEL", "VERIFY-KEY-SENTINEL"}, wants: []string{"var.pingone_application_demo_integrity_detection_google_play_decryption_key", "var.pingone_application_demo_integrity_detection_google_play_verification_key", "\"MINUTES\""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attr, value any
			if err := json.Unmarshal([]byte(tt.schema), &attr); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.value), &value); err != nil {
				t.Fatal(err)
			}
			schema, state := renderFixture(t, map[string]any{tt.attr: attr}, nil, map[string]any{tt.attr: value}, nil)
			exp, err := RenderProviderState(schema, state, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertValidExport(t, exp)
			all := flattenFiles(exp.Files)
			for _, secret := range tt.secrets {
				if strings.Contains(all, secret) {
					t.Fatal("provider nested secret escaped")
				}
			}
			for _, want := range tt.wants {
				if !strings.Contains(all, want) {
					t.Errorf("missing %q in provider-shaped output: %s", want, all)
				}
			}
		})
	}
}

// Failures must be atomic for invalid state identity, sensitivity masks and
// resource types, rather than returning a partial configuration.
func TestRenderProviderStateRejectsInvalidStateAndCatalogTypes(t *testing.T) {
	schema, state := renderFixture(t, map[string]any{"name": map[string]any{"type": "string", "optional": true}}, nil, map[string]any{"name": "visible"}, nil)
	for _, mutate := range []func(map[string]any){
		func(r map[string]any) { r["sensitive_values"] = map[string]any{"name": "invalid-secret-mask"} },
		func(r map[string]any) { r["sensitive_values"] = map[string]any{"unknown": true} },
		func(r map[string]any) { r["address"] = "module.child.pingone_application.demo" },
		func(r map[string]any) { r["address"] = "pingone_application.demo[0]" },
		func(r map[string]any) { r["type"] = "pingone_user"; r["address"] = "pingone_user.demo" },
		func(r map[string]any) { r["type"] = "pingone_future_type"; r["address"] = "pingone_future_type.demo" },
	} {
		var doc map[string]any
		if err := json.Unmarshal(state, &doc); err != nil {
			t.Fatal(err)
		}
		r := doc["values"].(map[string]any)["root_module"].(map[string]any)["resources"].([]any)[0].(map[string]any)
		mutate(r)
		src, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if exp, err := RenderProviderState(schema, src, nil); err == nil || exp != nil {
			t.Fatal("invalid state produced an export")
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(state, &doc); err != nil {
		t.Fatal(err)
	}
	root := doc["values"].(map[string]any)["root_module"].(map[string]any)
	rs := root["resources"].([]any)
	root["resources"] = append(rs, rs[0])
	duplicate, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if exp, err := RenderProviderState(schema, duplicate, nil); err == nil || exp != nil {
		t.Fatal("duplicate address produced an export")
	}
}

// Unlike a computed-only generated secret, v1.24.0 webhook authentication
// headers are optional+computed+sensitive configuration. Omitting them removes
// the outbound authentication settings from the exported resource.
func TestRenderProviderStateOptionalComputedSecretIsConfiguration(t *testing.T) {
	attrs := map[string]any{"connection_details_headers": map[string]any{"type": []any{"map", "string"}, "optional": true, "computed": true, "sensitive": true}}
	schema, state := renderResourceFixture(t, "pingone_webhook", attrs, nil, map[string]any{"connection_details_headers": map[string]any{"Authorization": "WEBHOOK-SECRET-SENTINEL"}}, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertVariableTypes(t, exp.Files[ModuleDir+"/variables.tf"], map[string]cty.Type{"pingone_webhook_demo_connection_details_headers": cty.Map(cty.String)})
	all := flattenFiles(exp.Files)
	if strings.Contains(all, "WEBHOOK-SECRET-SENTINEL") {
		t.Fatal("webhook auth header escaped")
	}
	if !strings.Contains(all, "var.pingone_webhook_demo_connection_details_headers") {
		t.Fatal("webhook authentication configuration was omitted")
	}
}

// The actual application_secret resource exports rotation configuration and
// adoption metadata, never the generated current/previous credential values.
func TestRenderProviderStatePinnedApplicationSecretOutputsOmitted(t *testing.T) {
	var attrs map[string]any
	if err := json.Unmarshal([]byte(`{"application_id":{"type":"string","required":true},"environment_id":{"type":"string","required":true},"previous":{"nested_type":{"attributes":{"expires_at":{"type":"string","optional":true},"last_used":{"type":"string","computed":true},"secret":{"type":"string","computed":true,"sensitive":true}},"nesting_mode":"single"},"optional":true},"regenerate_trigger_values":{"type":["map","string"],"optional":true},"secret":{"type":"string","computed":true,"sensitive":true}}`), &attrs); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{"environment_id": "source-env", "application_id": "application-id", "secret": "GENERATED-CURRENT-SENTINEL", "previous": map[string]any{"expires_at": "2026-11-11T10:00:00Z", "last_used": "2026-11-11T09:00:00Z", "secret": "GENERATED-PREVIOUS-SENTINEL"}, "regenerate_trigger_values": map[string]any{"rotation": "quarterly"}}
	schema, state := renderResourceFixture(t, "pingone_application_secret", attrs, nil, values, nil)
	exp, err := RenderProviderState(schema, state, map[string]string{"pingone_application_secret.demo": "source-env/application-id"})
	if err != nil {
		t.Fatal(err)
	}
	assertValidExport(t, exp)
	all := flattenFiles(exp.Files)
	for _, secret := range []string{"GENERATED-CURRENT-SENTINEL", "GENERATED-PREVIOUS-SENTINEL"} {
		if strings.Contains(all, secret) {
			t.Fatal("generated application credential escaped")
		}
	}
	for _, want := range []string{"regenerate_trigger_values", "quarterly", "expires_at", "var.pingone_environment_id", `id = "source-env/application-id"`} {
		if !strings.Contains(all, want) {
			t.Errorf("lost application secret configuration %q", want)
		}
	}
	if strings.Contains(all, `"secret" =`) || strings.Contains(all, "last_used") {
		t.Fatal("computed secret output emitted")
	}
}

// The v1.24.0 image read returns processed image metadata, but cannot recover
// the required upload body. It must become an input for every environment.
func TestRenderProviderStateMissingImageBodyBecomesEnvironmentInput(t *testing.T) {
	attrs := map[string]any{
		"environment_id":    map[string]any{"type": "string", "required": true},
		"id":                map[string]any{"type": "string", "computed": true},
		"image_file_base64": map[string]any{"type": "string", "required": true},
		"uploaded_image": map[string]any{"computed": true, "nested_type": map[string]any{"nesting_mode": "single", "attributes": map[string]any{
			"height": map[string]any{"type": "number", "computed": true},
			"href":   map[string]any{"type": "string", "computed": true},
			"type":   map[string]any{"type": "string", "computed": true},
			"width":  map[string]any{"type": "number", "computed": true},
		}}},
	}
	for _, missing := range []bool{false, true} {
		values := map[string]any{"environment_id": "source-env", "id": "image-id", "uploaded_image": map[string]any{"href": "https://images.example/source", "height": 1, "width": 1, "type": "png"}}
		if !missing {
			values["image_file_base64"] = nil
		}
		schema, state := renderResourceFixture(t, "pingone_image", attrs, nil, values, nil)
		exp, err := RenderProviderState(schema, state, nil)
		if err != nil {
			t.Fatal(err)
		}
		name := "pingone_image_demo_image_file_base64"
		assertRequiredInput(t, exp, name, cty.String, false)
		if !exp.EnvLocalVariables[name] || len(exp.EnvLocalVariables) != 1 {
			t.Fatalf("missing upload must remain environment-local: %v", exp.EnvLocalVariables)
		}
		assertValidExport(t, exp)
		if !strings.Contains(string(exp.Files[ModuleDir+"/pingone_image.tf"]), "var."+name) {
			t.Fatal("missing image body was not connected to its input")
		}
		if strings.Contains(flattenFiles(exp.Files), "images.example") {
			t.Fatal("computed upload metadata was used as a substitute image")
		}
	}
}

func TestRenderProviderStateMissingCredentialsRemainRequiredSecrets(t *testing.T) {
	for _, tt := range []struct {
		name   string
		attr   map[string]any
		marker any
	}{
		{"schema required secret", map[string]any{"type": []any{"map", "string"}, "required": true, "sensitive": true}, nil},
		{"state required secret", map[string]any{"type": []any{"map", "string"}, "required": true}, true},
		{"state explicit optional secret", map[string]any{"type": []any{"map", "string"}, "optional": true}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			schema, state := renderFixture(t, map[string]any{"credential": tt.attr, "client_secret": map[string]any{"type": "string", "computed": true, "sensitive": true}}, nil, map[string]any{"credential": nil, "client_secret": "GENERATED-NOT-CONFIGURATION"}, map[string]any{"credential": tt.marker, "client_secret": true})
			exp, err := RenderProviderState(schema, state, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertRequiredInput(t, exp, "pingone_application_demo_credential", cty.Map(cty.String), true)
			if len(exp.EnvLocalVariables) != 0 || strings.Contains(flattenFiles(exp.Files), "GENERATED-NOT-CONFIGURATION") || strings.Contains(flattenFiles(exp.Files), "demo_client_secret") {
				t.Fatal("secret source values or generated outputs became configuration")
			}
		})
	}
}

func TestRenderProviderStateMissingNestedInputsKeepSchemaTypes(t *testing.T) {
	attrs := map[string]any{"settings": map[string]any{"required": true, "nested_type": map[string]any{"nesting_mode": "single", "attributes": map[string]any{
		"enabled":     map[string]any{"required": true, "type": "bool"},
		"credentials": map[string]any{"required": true, "sensitive": true, "type": []any{"list", "string"}},
	}}}}
	blocks := map[string]any{"endpoint": map[string]any{"nesting_mode": "list", "min_items": 1, "block": map[string]any{"attributes": map[string]any{"port": map[string]any{"type": "number", "required": true}}}}}
	schema, state := renderFixture(t, attrs, blocks, map[string]any{"settings": map[string]any{}, "endpoint": []any{map[string]any{}}}, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertRequiredInput(t, exp, "pingone_application_demo_settings_enabled", cty.Bool, false)
	assertRequiredInput(t, exp, "pingone_application_demo_settings_credentials", cty.List(cty.String), true)
	assertRequiredInput(t, exp, "pingone_application_demo_endpoint_0_port", cty.Number, false)
	if len(exp.EnvLocalVariables) != 2 || !exp.EnvLocalVariables["pingone_application_demo_settings_enabled"] || !exp.EnvLocalVariables["pingone_application_demo_endpoint_0_port"] {
		t.Fatalf("nested missing configuration lost environment-local classification: %v", exp.EnvLocalVariables)
	}
	f, d := hclsyntax.ParseConfig(exp.Files[ModuleDir+"/pingone_application.tf"], "resource.tf", hcl.InitialPos)
	if d.HasErrors() {
		t.Fatal(d)
	}
	settings, d := f.Body.(*hclsyntax.Body).Blocks[0].Body.Attributes["settings"].Expr.Value(&hcl.EvalContext{Variables: map[string]cty.Value{"var": cty.ObjectVal(map[string]cty.Value{
		"pingone_application_demo_settings_enabled":     cty.False,
		"pingone_application_demo_settings_credentials": cty.ListVal([]cty.Value{cty.StringVal("environment-secret")}),
	})}})
	if d.HasErrors() || !settings.GetAttr("enabled").RawEquals(cty.False) || !settings.GetAttr("credentials").Type().Equals(cty.List(cty.String)) {
		t.Fatalf("nested configuration does not accept typed environment inputs: %s", d)
	}
}

func TestRenderProviderStateMissingWholeNestedInputAndOptionalOmission(t *testing.T) {
	attrs := map[string]any{
		"settings":        map[string]any{"required": true, "nested_type": map[string]any{"nesting_mode": "single", "attributes": map[string]any{"enabled": map[string]any{"type": "bool", "required": true}}}},
		"optional_secret": map[string]any{"type": "string", "optional": true, "sensitive": true},
		"optional_public": map[string]any{"type": "string", "optional": true},
	}
	schema, state := renderFixture(t, attrs, nil, map[string]any{"optional_secret": nil, "optional_public": nil}, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertRequiredInput(t, exp, "pingone_application_demo_settings", cty.Object(map[string]cty.Type{"enabled": cty.Bool}), false)
	if strings.Contains(flattenFiles(exp.Files), "optional_secret") || strings.Contains(flattenFiles(exp.Files), "optional_public") {
		t.Fatal("absent optional configuration was invented")
	}
}

// Missing whole objects must retain their exact constraint, including optional
// fields, and inherit sensitivity from configurable descendants.
func TestRenderProviderStateMissingWholeObjectTypesAndSensitivity(t *testing.T) {
	for _, secret := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "secret descendant"}[secret], func(t *testing.T) {
			attrs := map[string]any{"settings": map[string]any{"required": true, "nested_type": map[string]any{"nesting_mode": "single", "attributes": map[string]any{
				"enabled":    map[string]any{"type": "bool", "required": true},
				"credential": map[string]any{"type": "string", "optional": true, "sensitive": secret},
				"generated":  map[string]any{"type": "string", "computed": true, "sensitive": true},
			}}}}
			schema, state := renderFixture(t, attrs, nil, map[string]any{"settings": nil}, nil)
			exp, err := RenderProviderState(schema, state, nil)
			if err != nil {
				t.Fatal(err)
			}
			name := "pingone_application_demo_settings"
			assertRequiredInput(t, exp, name, cty.ObjectWithOptionalAttrs(map[string]cty.Type{"enabled": cty.Bool, "credential": cty.String}, []string{"credential"}), secret)
			if exp.EnvLocalVariables[name] == secret {
				t.Fatal("whole object input has incorrect environment classification")
			}
		})
	}
}

// HCL type constructors accept identifier keys, including Unicode and keywords.
// Configured object values must also preserve keys that require string literals.
func TestRenderProviderStateObjectInputKeys(t *testing.T) {
	objectTypes := map[string]any{"en-US": "bool", "café": "string", "true": "number", "null": "string"}
	want := cty.Object(map[string]cty.Type{"en-US": cty.Bool, "café": cty.String, "true": cty.Number, "null": cty.String})
	schema, state := renderFixture(t, map[string]any{"settings": map[string]any{"required": true, "type": []any{"object", objectTypes}}}, nil, map[string]any{}, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertRequiredInput(t, exp, "pingone_application_demo_settings", want, false)

	for _, key := range []string{"space key", "a.b", "", "${unsafe}"} {
		t.Run(key, func(t *testing.T) {
			attrs := map[string]any{"settings": map[string]any{"required": true, "type": []any{"object", map[string]any{key: "string"}}}}
			schema, state := renderFixture(t, attrs, nil, map[string]any{"settings": map[string]any{key: "preserved"}}, nil)
			exp, err := RenderProviderState(schema, state, nil)
			if err != nil {
				t.Fatal(err)
			}
			f, d := hclsyntax.ParseConfig(exp.Files[ModuleDir+"/pingone_application.tf"], "resource.tf", hcl.InitialPos)
			if d.HasErrors() {
				t.Fatal(d)
			}
			value, d := f.Body.(*hclsyntax.Body).Blocks[0].Body.Attributes["settings"].Expr.Value(nil)
			if d.HasErrors() || !value.GetAttr(key).RawEquals(cty.StringVal("preserved")) {
				t.Fatalf("object key lost: %s", d)
			}
			schema, state = renderFixture(t, attrs, nil, map[string]any{}, nil)
			if exp, err := RenderProviderState(schema, state, nil); err == nil || exp != nil {
				t.Fatal("unrepresentable typed input must fail closed")
			}
		})
	}
}

// A literal schema key containing a period must not alias a nested key path.
func TestRenderProviderStateNestedInputPathsDoNotAlias(t *testing.T) {
	attrs := map[string]any{"settings": map[string]any{"required": true, "nested_type": map[string]any{"nesting_mode": "single", "attributes": map[string]any{
		"a.b": map[string]any{"type": "string", "required": true},
		"a":   map[string]any{"required": true, "nested_type": map[string]any{"nesting_mode": "single", "attributes": map[string]any{"b": map[string]any{"type": "string", "required": true, "sensitive": true}}}},
	}}}}
	schema, state := renderFixture(t, attrs, nil, map[string]any{"settings": map[string]any{"a": map[string]any{}}}, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.EnvLocalVariables) != 1 {
		t.Fatal("distinct public nested key was aliased to secret path")
	}
	f, d := hclsyntax.ParseConfig(exp.Files[ModuleDir+"/variables.tf"], "vars.tf", hcl.InitialPos)
	if d.HasErrors() || len(f.Body.(*hclsyntax.Body).Blocks) != 3 {
		t.Fatal("distinct typed paths were not preserved")
	}
}

func TestRenderProviderStateMissingInputsKeepMalformedMetadataClosed(t *testing.T) {
	for _, attr := range []map[string]any{
		{"required": true},
		{"required": true, "type": "dynamic"},
		{"required": true, "type": "string", "nested_type": map[string]any{"nesting_mode": "single"}},
	} {
		schema, state := renderFixture(t, map[string]any{"credential": attr}, nil, map[string]any{"credential": nil}, nil)
		if exp, err := RenderProviderState(schema, state, nil); err == nil || exp != nil {
			t.Fatal("missing value bypassed schema validation")
		}
	}
	schema, state := renderFixture(t, nil, map[string]any{"endpoint": map[string]any{"nesting_mode": "list", "min_items": 1, "block": map[string]any{}}}, map[string]any{}, nil)
	if exp, err := RenderProviderState(schema, state, nil); err == nil || exp != nil {
		t.Fatal("missing required block was fabricated")
	}
}

func TestRenderProviderStateMissingInputNamesDoNotAliasSecrets(t *testing.T) {
	attrs := map[string]any{
		"a-b": map[string]any{"type": "string", "required": true},
		"a_b": map[string]any{"type": "string", "required": true, "sensitive": true},
	}
	schema, state := renderFixture(t, attrs, nil, map[string]any{}, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	for filename, body := range exp.Files {
		if string(body) != string(again.Files[filename]) {
			t.Fatalf("nondeterministic %s", filename)
		}
	}
	f, d := hclsyntax.ParseConfig(exp.Files[ModuleDir+"/pingone_application.tf"], "resource.tf", hcl.InitialPos)
	if d.HasErrors() {
		t.Fatal(d)
	}
	attributes := f.Body.(*hclsyntax.Body).Blocks[0].Body.Attributes
	public := attributes["a-b"].Expr.Variables()[0][1].(hcl.TraverseAttr).Name
	secret := attributes["a_b"].Expr.Variables()[0][1].(hcl.TraverseAttr).Name
	if public == secret || !exp.EnvLocalVariables[public] || exp.EnvLocalVariables[secret] {
		t.Fatal("public input and secret aliased")
	}
	assertRequiredInput(t, exp, public, cty.String, false)
	assertRequiredInput(t, exp, secret, cty.String, true)
}

func TestStateRendererRepeatedInputSensitivityAndTypeCollisions(t *testing.T) {
	for _, secretFirst := range []bool{false, true} {
		r := stateRenderer{vars: map[string]secretVariable{}}
		for _, secret := range []bool{secretFirst, !secretFirst} {
			if _, _, err := r.attribute(nil, nil, schemaAttribute{Type: json.RawMessage(`"string"`), Required: true, Sensitive: secret}, "pingone_application.demo.credential"); err != nil {
				t.Fatal(err)
			}
		}
		f, d := hclsyntax.ParseConfig(r.variables(), "variables.tf", hcl.InitialPos)
		if d.HasErrors() {
			t.Fatal(d)
		}
		if len(f.Body.(*hclsyntax.Body).Blocks) != 2 {
			t.Fatal("repeated path created multiple declarations")
		}
		b := f.Body.(*hclsyntax.Body).Blocks[1]
		a, ok := b.Body.Attributes["sensitive"]
		if !ok {
			t.Fatal("secret path was downgraded to a public input")
		}
		v, d := a.Expr.Value(nil)
		if d.HasErrors() || !v.RawEquals(cty.True) {
			t.Fatal("secret path was downgraded")
		}
		if _, _, err := r.attribute(nil, nil, schemaAttribute{Type: json.RawMessage(`"number"`), Required: true}, "pingone_application.demo.credential"); err == nil {
			t.Fatal("conflicting input types were silently replaced")
		}
	}
}

func TestRenderProviderStateSensitiveParentDoesNotCreateMissingChildInputs(t *testing.T) {
	attrs := map[string]any{"settings": map[string]any{"required": true, "sensitive": true, "nested_type": map[string]any{"nesting_mode": "single", "attributes": map[string]any{"enabled": map[string]any{"type": "bool", "required": true}}}}}
	schema, state := renderFixture(t, attrs, nil, map[string]any{"settings": map[string]any{}}, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertRequiredInput(t, exp, "pingone_application_demo_settings", cty.Object(map[string]cty.Type{"enabled": cty.Bool}), true)
	if len(exp.EnvLocalVariables) != 0 || strings.Contains(flattenFiles(exp.Files), "demo_settings_enabled") {
		t.Fatal("sensitive parent created an orphaned public child input")
	}
}

// Required inputs must survive the renderer/neutralizer boundary without
// acquiring a source value, since no API value was available to export.
func TestRenderProviderStateMissingInputsRemainUnvaluedAfterNeutralize(t *testing.T) {
	attrs := map[string]any{
		"credential": map[string]any{"type": []any{"map", "string"}, "required": true, "sensitive": true},
		"settings": map[string]any{"required": true, "nested_type": map[string]any{"nesting_mode": "single", "attributes": map[string]any{
			"enabled":     map[string]any{"type": "bool", "required": true},
			"description": map[string]any{"type": "string", "optional": true},
		}}},
	}
	schema, state := renderFixture(t, attrs, nil, map[string]any{}, nil)
	exp, err := RenderProviderState(schema, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := neutralize.Neutralize(&neutralize.Input{Files: exp.Files, ModuleDir: exp.ModuleDir, ModuleName: exp.ModuleName, EnvLocalVariables: exp.EnvLocalVariables}, neutralize.Options{SourceEnvironmentID: "11111111-1111-4111-8111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		kind neutralize.Kind
		typ  cty.Type
	}{
		{"pingone_application_demo_credential", neutralize.KindSecret, cty.Map(cty.String)},
		{"pingone_application_demo_settings", neutralize.KindEnvLocal, cty.ObjectWithOptionalAttrs(map[string]cty.Type{"enabled": cty.Bool, "description": cty.String}, []string{"description"})},
	} {
		variable := result.Variables[tt.name]
		if variable == nil || variable.Kind != tt.kind {
			t.Fatalf("missing input classification lost for %s", tt.name)
		}
		expr, d := hclsyntax.ParseExpression([]byte(variable.Type), "type.tf", hcl.InitialPos)
		if d.HasErrors() {
			t.Fatal(d)
		}
		got, d := typeexpr.TypeConstraint(expr)
		if d.HasErrors() || !got.Equals(tt.typ) {
			t.Fatalf("input type changed for %s: %s", tt.name, d)
		}
		if _, ok := result.SourceValues[tt.name]; ok {
			t.Fatalf("unreadable input %s acquired a source value", tt.name)
		}
	}
}

func assertRequiredInput(t *testing.T, exp *Export, name string, typ cty.Type, sensitive bool) {
	t.Helper()
	for _, filename := range []string{ModuleDir + "/variables.tf", ModuleName + "-variables.tf"} {
		f, d := hclsyntax.ParseConfig(exp.Files[filename], filename, hcl.InitialPos)
		if d.HasErrors() {
			t.Fatal(d)
		}
		found := false
		for _, b := range f.Body.(*hclsyntax.Body).Blocks {
			if b.Type != "variable" || b.Labels[0] != name {
				continue
			}
			found = true
			got, d := typeexpr.TypeConstraint(b.Body.Attributes["type"].Expr)
			if d.HasErrors() || !got.Equals(typ) {
				t.Errorf("%s has type %s, want %s: %s", name, got.GoString(), typ.GoString(), d)
			}
			if _, ok := b.Body.Attributes["default"]; ok {
				t.Errorf("%s unexpectedly has a default", name)
			}
			if a, ok := b.Body.Attributes["sensitive"]; ok {
				v, d := a.Expr.Value(nil)
				if d.HasErrors() || !v.RawEquals(cty.BoolVal(sensitive)) {
					t.Errorf("%s has wrong sensitivity", name)
				}
			} else if sensitive {
				t.Errorf("%s must be sensitive", name)
			}
		}
		if !found {
			t.Errorf("%s missing in %s", name, filename)
		}
	}
	f, d := hclsyntax.ParseConfig(exp.Files[ModuleName+"-module.tf"], "module.tf", hcl.InitialPos)
	if d.HasErrors() {
		t.Fatal(d)
	}
	a, ok := f.Body.(*hclsyntax.Body).Blocks[0].Body.Attributes[name]
	if !ok {
		t.Fatalf("%s missing from module inputs", name)
	}
	vars := a.Expr.Variables()
	if len(vars) != 1 || len(vars[0]) != 2 || vars[0].RootName() != "var" || vars[0][1].(hcl.TraverseAttr).Name != name {
		t.Errorf("%s is not passed from its root input", name)
	}
}
