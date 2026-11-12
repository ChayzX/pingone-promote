package multiexport

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/exporter/exportertest"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/pingone/pingonetest"
)

const env = "11111111-1111-4111-8111-111111111111"

var creds = exporter.Credentials{ClientID: "fake-client", ClientSecret: "fake-secret-value-not-real", WorkerEnvironmentID: "99999999-9999-4999-8999-999999999999", RegionCode: "NA", ExportEnvironmentID: env}

type harness struct {
	e    *Exporter
	core *pingonetest.Fake
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := exportertest.Build(t, t.TempDir(), exportertest.FixtureDir("cross"))
	core := pingonetest.Start(t, pingonetest.LoadTenant(t, "../coreexport/testdata/tenant.json"))
	e := &Exporter{
		DaVinci: &exporter.Runner{Config: exporter.Config{Path: fake.Path, SHA256: fake.SHA256, Version: fake.Version}},
		NewReader: func(c exporter.Credentials) (coreexport.Reader, error) {
			cl, err := pingone.New(pingone.Credentials{ClientID: c.ClientID, ClientSecret: c.ClientSecret, WorkerEnvironmentID: c.WorkerEnvironmentID, RegionCode: c.RegionCode})
			if err != nil {
				return nil, err
			}
			cl.AuthBase, cl.APIBase = core.AuthBase(), core.APIBase()
			return cl, nil
		},
	}
	return &harness{e: e, core: core}
}

// blocks lists the resource addresses of an export.
func blocks(t *testing.T, exp *exporter.Export) []string {
	t.Helper()
	a, err := exporter.Addresses(exp)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// neutralized runs the export through the neutralizer the way the engine
// does: referenced-but-not-exported resources are excluded references.
func neutralized(t *testing.T, exp *exporter.Export) *neutralize.Result {
	t.Helper()
	var ex []neutralize.Excluded
	for a, id := range exp.Referenced {
		ex = append(ex, neutralize.Excluded{Address: a, ID: id})
	}
	sort.Slice(ex, func(i, j int) bool { return ex[i].Address < ex[j].Address })
	res, err := neutralize.Neutralize(&neutralize.Input{Files: exp.Files, ModuleDir: exp.ModuleDir, ModuleName: exp.ModuleName},
		neutralize.Options{SourceEnvironmentID: env, Excluded: ex})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func text(res *neutralize.Result) string {
	var b strings.Builder
	for _, bl := range res.Blocks {
		b.Write(bl.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func has(list []string, a string) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

// One item of every covered type family can be exported alone (with its
// owned children only), and with its upstream dependencies.
var families = []struct {
	family, seed string
	// owned are children that always travel with the seed.
	owned []string
	// upstream are dependencies that only "include upstream" adds.
	upstream []string
}{
	{"davinci", "pingone_davinci_flow.pingcli__Flow-0020-A", []string{"pingone_davinci_flow_deploy.pingcli__Flow-0020-A", "pingone_davinci_flow_enable.pingcli__Flow-0020-A"},
		[]string{"pingone_davinci_connector_instance.pingcli__Http"}},
	{"applications", "pingone_application.pingcli__Web-0020-Portal", []string{"pingone_application_attribute_mapping.pingcli__Web-0020-Portal_email",
		"pingone_application_sign_on_policy_assignment.pingcli__Web-0020-Portal_1", "pingone_application_flow_policy_assignment.pingcli__Web-0020-Portal_1",
		"pingone_application_resource_grant.pingcli__Web-0020-Portal_Orders-0020-API"},
		[]string{"pingone_sign_on_policy.pingcli__Customer_Login", "pingone_resource.pingcli__Orders-0020-API", "pingone_davinci_application_flow_policy.pingcli__Web-0020-Policy"}},
	{"resources_scopes_grants", "pingone_resource.pingcli__Orders-0020-API", []string{"pingone_resource_scope.pingcli__Orders-0020-API_orders-003A-read"}, nil},
	{"sign_on", "pingone_sign_on_policy.pingcli__Customer_Login", []string{"pingone_sign_on_policy_action.pingcli__Customer_Login_1", "pingone_sign_on_policy_action.pingcli__Customer_Login_2"},
		[]string{"pingone_population.pingcli__Customers", "pingone_password_policy.pingcli__Strict", "pingone_mfa_device_policy.pingcli__Strong-0020-MFA"}},
	{"directory", "pingone_population.pingcli__Customers", nil, []string{"pingone_password_policy.pingcli__Strict"}},
	{"directory", "pingone_group.pingcli__Support-0020-Agents", []string{"pingone_group_nesting.pingcli__Support-0020-Agents_Staff"},
		[]string{"pingone_population.pingcli__Customers", "pingone_group.pingcli__Staff"}},
	{"directory", "pingone_schema_attribute.pingcli__loyaltyTier", nil, nil},
	{"mfa_devices", "pingone_mfa_device_policy.pingcli__Strong-0020-MFA", nil, nil},
	{"mfa_devices", "pingone_mfa_fido2_policy.pingcli__Passkeys", nil, nil},
	{"identity_providers", "pingone_identity_provider.pingcli__Corporate-0020-OIDC", []string{"pingone_identity_provider_attribute.pingcli__Corporate-0020-OIDC_email"},
		[]string{"pingone_population.pingcli__Customers"}},
	{"gateways", "pingone_gateway.pingcli__Corp-0020-LDAP", nil, nil},
	{"branding", "pingone_branding_theme.pingcli__Corporate", []string{"pingone_branding_theme_default.pingcli__Corporate"}, nil},
	{"notifications", "pingone_notification_template_content.pingcli__recovery_code_template_en_Email", nil, nil},
	{"notifications", "pingone_notification_policy.pingcli__Default-0020-quotas", nil, nil},
	{"forms", "pingone_form.pingcli__Customer-0020-signup", nil, nil},
	{"agreements", "pingone_agreement.pingcli__Terms-0020-of-0020-Service", []string{"pingone_agreement_enable.pingcli__Terms-0020-of-0020-Service",
		"pingone_agreement_localization.pingcli__Terms-0020-of-0020-Service_en", "pingone_agreement_localization_enable.pingcli__Terms-0020-of-0020-Service_en",
		"pingone_agreement_localization_revision.pingcli__Terms-0020-of-0020-Service_en_2026-01-01T00-003A-00-003A-00Z"}, []string{"pingone_language_update.pingcli__en"}},
	{"languages", "pingone_language.pingcli__fr-BE", nil, nil},
	{"languages", "pingone_language_update.pingcli__en", nil, nil},
	{"protect", "pingone_risk_predictor.pingcli__vipList", nil, nil},
	{"authorize", "pingone_authorize_api_service.pingcli__Orders-0020-gateway", []string{"pingone_authorize_api_service_operation.pingcli__Orders-0020-gateway_List-0020-orders"},
		[]string{"pingone_resource.pingcli__Orders-0020-API"}},
	{"credentials", "pingone_credential_type.pingcli__Employee-0020-badge", nil, nil},
	{"verify", "pingone_verify_policy.pingcli__Standard-0020-ID-0020-check", nil, nil},
	{"platform", "pingone_webhook.pingcli__Audit-0020-to-0020-SIEM", nil, nil},
	{"platform", "pingone_custom_domain.pingcli__auth-002E-dev-002E-example-002E-com", nil, nil},
	{"platform", "pingone_mfa_settings.pingcli__environment", nil, nil},
}

func TestSingleItemOfEveryFamilyAloneAndWithUpstream(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, f := range families {
		t.Run(f.family+"/"+f.seed, func(t *testing.T) {
			alone, err := h.e.SelectedExport(ctx, creds, []string{f.seed}, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := blocks(t, alone)
			want := append([]string{f.seed}, f.owned...)
			for _, w := range want {
				if !has(got, w) {
					t.Errorf("alone: %s missing from %v", w, got)
				}
			}
			for _, a := range got {
				if !has(want, a) && !strings.HasPrefix(a, "pingone_davinci_") {
					t.Errorf("alone: %s exported without being selected or owned", a)
				}
			}
			for _, u := range f.upstream {
				if has(got, u) {
					t.Errorf("alone: dependency %s was exported without include upstream", u)
				}
			}
			res := neutralized(t, alone)
			if strings.Contains(text(res), env) {
				t.Error("the source environment ID reached the definitions")
			}
			up, err := h.e.ScopedExport(ctx, creds, []string{f.seed}, nil)
			if err != nil {
				t.Fatal(err)
			}
			upGot := blocks(t, up)
			for _, w := range append(want, f.upstream...) {
				if !has(upGot, w) {
					t.Errorf("upstream: %s missing from %v", w, upGot)
				}
			}
			neutralized(t, up)
		})
	}
	if h.core.Writes() != 0 {
		t.Fatalf("%d write requests reached PingOne", h.core.Writes())
	}
}

// Every family of the catalog with an exporter has a case above.
func TestEveryCoveredFamilyHasACase(t *testing.T) {
	covered := map[string]bool{}
	for _, f := range families {
		covered[f.family] = true
	}
	for _, d := range catalog.All() {
		if d.Exporter != "" && !covered[d.Family] {
			t.Errorf("family %s (%s) has no single-item export case", d.Family, d.Type)
		}
	}
}

func TestOneNotificationTemplateExportsNothingElse(t *testing.T) {
	h := newHarness(t)
	seed := "pingone_notification_template_content.pingcli__recovery_code_template_en_Email"
	exp, err := h.e.ScopedExport(context.Background(), creds, []string{seed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := blocks(t, exp)
	if len(got) != 1 || got[0] != seed {
		t.Fatalf("export = %v, want only %s", got, seed)
	}
	out := text(neutralized(t, exp))
	for _, want := range []string{`template_name = "recovery_code_template"`, `locale        = "en"`, `subject = "Your recovery code"`, "$${code}"} {
		if !strings.Contains(out, want) {
			t.Errorf("template content lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Recovery code: ") {
		t.Error("the SMS variant of the template was exported too")
	}
}

// Core -> DaVinci: an application's flow policy assignment references a
// DaVinci flow policy, which pulls the DaVinci application and its flows.
func TestUpstreamCrossesFromCoreToDaVinci(t *testing.T) {
	h := newHarness(t)
	exp, err := h.e.ScopedExport(context.Background(), creds, []string{"pingone_application.pingcli__Web-0020-Portal"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := blocks(t, exp)
	for _, w := range []string{"pingone_davinci_application_flow_policy.pingcli__Web-0020-Policy", "pingone_davinci_application.pingcli__Web", "pingone_davinci_flow.pingcli__Flow-0020-A"} {
		if !has(got, w) {
			t.Errorf("%s missing: %v", w, got)
		}
	}
	out := text(neutralized(t, exp))
	if !strings.Contains(out, "flow_policy_id = pingone_davinci_application_flow_policy.pingcli__Web-0020-Policy.id") {
		t.Errorf("the assignment does not reference the DaVinci flow policy:\n%s", out)
	}
}

// DaVinci -> core: a flow node refers to a core population by ID.
func TestUpstreamCrossesFromDaVinciToCore(t *testing.T) {
	h := newHarness(t)
	seed := "pingone_davinci_flow.pingcli__Flow-0020-C"
	exp, err := h.e.ScopedExport(context.Background(), creds, []string{seed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := blocks(t, exp)
	for _, w := range []string{"pingone_population.pingcli__Customers", "pingone_password_policy.pingcli__Strict"} {
		if !has(got, w) {
			t.Errorf("%s missing: %v", w, got)
		}
	}
	out := text(neutralized(t, exp))
	if !strings.Contains(out, "pingone_population.pingcli__Customers.id") || strings.Contains(out, "20000000-0000-4000-8000-000000000002") {
		t.Errorf("the flow should reference the population, not its source ID:\n%s", out)
	}
	// Without upstream the population is a per-environment ID variable.
	alone, err := h.e.SelectedExport(context.Background(), creds, []string{seed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if has(blocks(t, alone), "pingone_population.pingcli__Customers") {
		t.Fatal("the population was exported without include upstream")
	}
	if alone.Referenced["pingone_population.pingcli__Customers"] != "20000000-0000-4000-8000-000000000002" {
		t.Fatalf("referenced = %v", alone.Referenced)
	}
	res := neutralized(t, alone)
	v := res.Variables[neutralize.ExcludedVariableName("pingone_population.pingcli__Customers")]
	if v == nil || v.Kind != neutralize.KindExcludedRef {
		t.Fatalf("no per-environment ID variable for the population: %v", res.Variables)
	}
}

func TestWholeEnvironmentWithExclusions(t *testing.T) {
	h := newHarness(t)
	exclude := []string{"pingone_application.pingcli__Web-0020-Portal", "pingone_davinci_flow.pingcli__Flow-0020-A", "pingone_notification_template_content.pingcli__recovery_code_template_en_SMS"}
	exp, err := h.e.WholeExport(context.Background(), creds, exclude)
	if err != nil {
		t.Fatal(err)
	}
	got := blocks(t, exp)
	for _, a := range got {
		if strings.HasPrefix(a, "pingone_application_") && strings.Contains(a, "Web-0020-Portal") {
			t.Errorf("%s is owned by an excluded application but was exported", a)
		}
		if has(exclude, a) {
			t.Errorf("excluded %s was exported", a)
		}
		if strings.HasPrefix(a, "pingone_user") {
			t.Errorf("identity data exported: %s", a)
		}
	}
	for _, w := range []string{"pingone_population.pingcli__Customers", "pingone_notification_template_content.pingcli__recovery_code_template_en_Email",
		"pingone_davinci_flow.pingcli__Flow-0020-B", "pingone_sign_on_policy.pingcli__Customer_Login", "pingone_mfa_settings.pingcli__environment"} {
		if !has(got, w) {
			t.Errorf("%s missing from the whole export", w)
		}
	}
	if len(exp.Adopt) == 0 || !has(exp.Adopt, "pingone_sign_on_policy.pingcli__Single_Factor") {
		t.Errorf("built-in objects must be listed for adoption: %v", exp.Adopt)
	}
	out := text(neutralized(t, exp))
	for _, secret := range []string{"Bearer hidden", creds.ClientSecret} {
		if strings.Contains(out, secret) {
			t.Fatalf("a secret reached the definitions: %q", secret)
		}
	}
	for p, b := range exp.Files {
		if strings.Contains(string(b), creds.ClientSecret) {
			t.Fatalf("the worker secret is in %s", p)
		}
	}
	if h.core.Writes() != 0 {
		t.Fatalf("%d write requests reached PingOne", h.core.Writes())
	}
}

func TestCatalogSearchesCoreAndDaVinciByNameAndID(t *testing.T) {
	h := newHarness(t)
	cat, err := h.e.Catalog(context.Background(), creds, true)
	if err != nil {
		t.Fatal(err)
	}
	if m := exporter.SearchType(cat, "pingone_notification_template_content", "recovery_code_template / en / Email"); len(m) != 1 {
		t.Errorf("template search = %v", m)
	}
	if m := exporter.SearchType(cat, "pingone_population", "20000000-0000-4000-8000-000000000002"); len(m) != 1 || m[0].Name != "Customers" {
		t.Errorf("population by ID = %v", m)
	}
	if m := exporter.SearchType(cat, "pingone_davinci_flow", "Flow C"); len(m) != 1 {
		t.Errorf("flow search = %v", m)
	}
	for _, r := range cat {
		if catalog.IdentityData(r.Type) {
			t.Errorf("identity data in the catalog: %s", r)
		}
	}
}

type failingDaVinci struct{ DaVinci }

func (failingDaVinci) CatalogAllIDs(context.Context, exporter.Credentials) ([]exporter.Resource, error) {
	return nil, errors.New("exporter: terraformer exited with status 1: 403 Forbidden")
}

// An environment whose DaVinci cannot be read still exports core items.
func TestCoreWorksWhenDaVinciCannotBeRead(t *testing.T) {
	h := newHarness(t)
	h.e.DaVinci = failingDaVinci{}
	cat, err := h.e.Catalog(context.Background(), creds, true)
	if err != nil {
		t.Fatalf("catalog failed because DaVinci could not be read: %v", err)
	}
	if len(cat) == 0 {
		t.Fatal("no core items")
	}
	exp, err := h.e.WholeExport(context.Background(), creds, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !has(blocks(t, exp), "pingone_population.pingcli__Customers") {
		t.Fatal("core items missing")
	}
	found := false
	for _, w := range exp.Warnings {
		found = found || strings.Contains(w, "DaVinci items were not read")
	}
	if !found {
		t.Fatalf("no warning: %v", exp.Warnings)
	}
	if _, err := h.e.SelectedExport(context.Background(), creds, []string{"pingone_davinci_flow.pingcli__Flow-0020-A"}, nil); err == nil {
		t.Fatal("selecting a DaVinci item must explain that DaVinci cannot be read")
	}
}
