package coreexport

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/hclblocks"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/pingone/pingonetest"
)

const env = "11111111-1111-4111-8111-111111111111"

func tenant(t *testing.T) (*pingonetest.Fake, *Inventory) {
	t.Helper()
	f := pingonetest.Start(t, pingonetest.LoadTenant(t, "testdata/tenant.json"))
	c, err := pingone.New(pingone.Credentials{ClientID: f.ClientID, ClientSecret: f.ClientSecret, WorkerEnvironmentID: f.WorkerEnvID, RegionCode: "NA"})
	if err != nil {
		t.Fatal(err)
	}
	c.AuthBase, c.APIBase = f.AuthBase(), f.APIBase()
	inv, err := Read(context.Background(), c, env)
	if err != nil {
		t.Fatal(err)
	}
	return f, inv
}

func addrs(inv *Inventory) []string {
	var out []string
	for _, o := range inv.Objects {
		out = append(out, o.Address())
	}
	return out
}

func TestInventoryReadsEveryFamilyReadOnly(t *testing.T) {
	f, inv := tenant(t)
	if f.Writes() != 0 {
		t.Fatalf("the exporter sent %d non-GET requests", f.Writes())
	}
	got := strings.Join(addrs(inv), "\n")
	for _, want := range []string{
		"pingone_population.pingcli__Customers",
		"pingone_population_default.pingcli__Default",
		"pingone_password_policy.pingcli__Strict",
		"pingone_group.pingcli__Support-0020-Agents",
		"pingone_schema_attribute.pingcli__loyaltyTier",
		"pingone_sign_on_policy.pingcli__Customer-0020-Login",
		"pingone_sign_on_policy_action.pingcli__Customer-0020-Login_2",
		"pingone_mfa_device_policy.pingcli__Strong-0020-MFA",
		"pingone_mfa_device_policy_default.pingcli__Default-0020-MFA-0020-Policy",
		"pingone_mfa_settings.pingcli__environment",
		"pingone_application.pingcli__Web-0020-Portal",
		"pingone_application_flow_policy_assignment.pingcli__Web-0020-Portal_1",
		"pingone_system_application.pingcli__PING_ONE_SELF_SERVICE",
		"pingone_resource_scope.pingcli__Orders-0020-API_orders-003A-read",
		"pingone_resource_scope_openid.pingcli__openid_email",
		"pingone_identity_provider.pingcli__Corporate-0020-OIDC",
		"pingone_gateway.pingcli__Corp-0020-LDAP",
		"pingone_branding_theme.pingcli__Corporate",
		"pingone_notification_template_content.pingcli__recovery_code_template_en_Email",
		"pingone_form.pingcli__Customer-0020-signup",
		"pingone_agreement_localization_revision.pingcli__Terms-0020-of-0020-Service_en_2026-01-01T00-003A-00-003A-00Z",
		"pingone_language.pingcli__fr-CA",
		"pingone_risk_predictor.pingcli__vipList",
		"pingone_authorize_api_service_operation.pingcli__Orders-0020-gateway_List-0020-orders",
		"pingone_credential_type.pingcli__Employee-0020-badge",
		"pingone_webhook.pingcli__Audit-0020-to-0020-SIEM",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("inventory lacks %s", want)
		}
	}
	if _, ok := inv.Skipped["pingone_verify_voice_phrase"]; !ok {
		t.Errorf("a 403 service must be skipped and reported: %v", inv.Skipped)
	}
	for _, o := range inv.Objects {
		if strings.HasPrefix(o.Type, "pingone_user") {
			t.Fatalf("identity data in the inventory: %s", o.Address())
		}
	}
	// Built-in objects are marked for adoption.
	for _, a := range []string{"pingone_sign_on_policy.pingcli__Single_Factor", "pingone_population_default.pingcli__Default", "pingone_language.pingcli__en"} {
		if o := inv.Object(a); o == nil || !o.Builtin {
			t.Errorf("%s should be a built-in object", a)
		}
	}
}

func renderAll(t *testing.T, inv *Inventory, sel []string, exported map[string]bool) *Rendered {
	t.Helper()
	ids := map[string]string{}
	for _, o := range inv.Objects {
		if o.ID != "" && o.ID != env && ids[o.ID] == "" {
			ids[o.ID] = o.Address()
		}
	}
	if exported == nil {
		exported = map[string]bool{}
		for _, a := range sel {
			exported[a] = true
		}
	}
	r, err := Render(RenderInput{Inventory: inv, Addresses: sel, Exported: exported, IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRenderEverythingParsesAndNeutralizes(t *testing.T) {
	_, inv := tenant(t)
	all := addrs(inv)
	r := renderAll(t, inv, all, nil)
	files := map[string][]byte{"ping-export-module/variables.tf": []byte("variable \"pingone_environment_id\" {\n  type = string\n}\n")}
	for p, b := range r.Files {
		if p == "ping-export-module/variables.tf" {
			files[p] = append(files[p], b...)
			continue
		}
		files[p] = b
	}
	if os.Getenv("PINGONE_PROMOTE_DUMP") != "" {
		keys := make([]string, 0, len(files))
		for k := range files {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("==== %s\n%s", k, files[k])
		}
	}
	res, err := neutralize.Neutralize(&neutralize.Input{Files: files}, neutralize.Options{SourceEnvironmentID: env})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocks) != len(all) {
		t.Fatalf("neutralized %d blocks, rendered %d", len(res.Blocks), len(all))
	}
	text := ""
	for _, b := range res.Blocks {
		text += string(b.Text)
	}
	if strings.Contains(text, "Bearer hidden") {
		t.Fatal("a webhook header (a secret) reached the definitions")
	}
	if strings.Contains(text, env) {
		t.Fatal("the source environment ID reached the definitions")
	}
	for _, want := range []string{
		"pingone_password_policy.pingcli__Strict.id",            // population -> password policy
		"pingone_population.pingcli__Customers.id",              // sign-on action -> population
		"pingone_mfa_device_policy.pingcli__Strong-0020-MFA.id", // MFA action -> device policy
		"pingone_sign_on_policy.pingcli__Customer-0020-Login.id",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("definitions lack the reference %s", want)
		}
	}
}

// Every rendered resource sets every attribute its schema requires, and the
// import IDs follow each type's template.
func TestRenderedBlocksHaveRequiredAttributes(t *testing.T) {
	_, inv := tenant(t)
	r := renderAll(t, inv, addrs(inv), nil)
	for p, src := range r.Files {
		if !strings.HasPrefix(p, "ping-export-module/") || strings.HasSuffix(p, "variables.tf") {
			continue
		}
		body, err := hclblocks.Parse(src, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range body.Blocks {
			schema, _ := catalog.Schema(b.Labels[0])
			for name, a := range schema {
				if !a.Required {
					continue
				}
				_, attr := b.Body.Attributes[name]
				blk := false
				for _, nb := range b.Body.Blocks {
					blk = blk || nb.Type == name
				}
				if !attr && !blk {
					t.Errorf("%s.%s lacks the required attribute %s", b.Labels[0], b.Labels[1], name)
				}
			}
		}
	}
	want := map[string]string{
		"pingone_agreement_localization_enable.pingcli__Terms-0020-of-0020-Service_en":                                  "34000000-0000-4000-8000-000000000001/34100000-0000-4000-8000-000000000001",
		"pingone_agreement_localization_revision.pingcli__Terms-0020-of-0020-Service_en_2026-01-01T00-003A-00-003A-00Z": "34000000-0000-4000-8000-000000000001/34100000-0000-4000-8000-000000000001/34200000-0000-4000-8000-000000000001",
		"pingone_language_update.pingcli__fr-CA":                                                                        "35000000-0000-4000-8000-000000000002",
		"pingone_group_nesting.pingcli__Support-0020-Agents_Staff":                                                      "22000000-0000-4000-8000-000000000002/22000000-0000-4000-8000-000000000001",
	}
	for a, id := range want {
		o := inv.Object(a)
		if o == nil {
			t.Errorf("%s is not in the inventory", a)
			continue
		}
		if o.ImportID != id {
			t.Errorf("%s import ID = %s, want %s", a, o.ImportID, id)
		}
	}
	// Children of a built-in object are adopted too.
	if o := inv.Object("pingone_sign_on_policy_action.pingcli__Single_Factor_1"); o == nil || !o.Builtin {
		t.Error("the default sign-on policy's action must be adopted, not created")
	}
	// A deprecated <x>_id is not written next to its object attribute <x>.
	if strings.Contains(string(r.Files["ping-export-module/pingone_population.tf"]), "password_policy_id") {
		t.Error("population sets both password_policy and password_policy_id")
	}
}
