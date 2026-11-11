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
