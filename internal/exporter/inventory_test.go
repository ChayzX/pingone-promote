package exporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pingone-promote/internal/resourcecatalog"
)

const inventoryEnv = "11111111-1111-4111-8111-111111111111"
const inventoryWorkerEnv = "22222222-2222-4222-8222-222222222222"

func inventoryCredentials() Credentials {
	return Credentials{ClientID: "worker-app", ClientSecret: "NEVER-PRINT-SECRET", WorkerEnvironmentID: inventoryWorkerEnv, ExportEnvironmentID: inventoryEnv, RegionCode: "NA"}
}
func inventoryHAL(items ...map[string]any) map[string]any {
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"_embedded": map[string]any{"items": items}}
}
func inventoryNamed(id, name string) map[string]any { return map[string]any{"id": id, "name": name} }

// These fixtures follow the pinned SDK's literal configuration endpoints and
// response shapes. They intentionally do not derive paths from recipes.
func inventoryFixtures() map[string]map[string]any {
	m := map[string]map[string]any{}
	for path, name := range map[string]string{
		"/agreements": "Terms", "/alertChannels": "Operations", "/apiServers": "Orders API", "/applicationResources": "Orders", "/applicationRoles": "Order reader", "/decisionEndpoints": "Order decision", "/certificates": "Signing certificate", "/credentialTypes": "Employee card", "/customDomains": "login.example.test", "/roles": "Configuration administrator", "/digitalWalletApplications": "Employee wallet", "/forms": "Login", "/gateways": "Directory", "/groups": "Administrators", "/propagation/plans": "Directory sync", "/identityProviders": "Corporate identity", "/keys": "Signing key", "/keyRotationPolicies": "Monthly rotation", "/fido2Policies": "Passkeys", "/notificationsPolicies": "Email quota", "/passwordPolicies": "Standard password", "/notificationsSettings/phoneDeliverySettings": "SMS", "/rateLimitIpConfigs": "Trusted network", "/riskPolicySets": "Login risk", "/riskPredictors": "Network predictor", "/schemas": "User schema", "/signOnPolicies": "Login", "/emailDomains": "example.test", "/voicePhrases": "Login phrase", "/verifyPolicies": "Identity verification", "/subscriptions": "Configuration webhook",
	} {
		m[path] = inventoryHAL(inventoryNamed("object", name))
	}
	m["/applicationResources"] = inventoryHAL(map[string]any{"id": "object", "name": "Orders", "resource": map[string]any{"id": "custom"}})
	for _, path := range []string{"/adminConfig", "/brandingSettings", "/recaptchaV2Config", "/mfaSettings", "/notificationsSettings", "/notificationsSettings/emailDeliverySettings"} {
		m[path] = map[string]any{"enabled": true}
	}
	m[""] = map[string]any{"id": inventoryEnv, "name": "Source environment"}
	m["/credentialIssuerProfile"] = inventoryNamed("issuer", "Corporate issuer")
	m["/applications"] = inventoryHAL(map[string]any{"id": "app", "name": "Customer portal", "protocol": "OPENID_CONNECT", "type": "WEB_APP", "tokenEndpointAuthMethod": "CLIENT_SECRET_BASIC"}, map[string]any{"id": "system", "name": "Account portal", "type": "PING_ONE_PORTAL"}, map[string]any{"id": "adminconsole", "name": "Admin console", "type": "PING_ONE_ADMIN_CONSOLE"})
	m["/themes"] = inventoryHAL(map[string]any{"id": "theme", "configuration": map[string]any{"name": "Corporate theme"}, "default": true})
	m["/populations"] = inventoryHAL(map[string]any{"id": "population", "name": "Customers", "default": true})
	m["/languages"] = inventoryHAL(map[string]any{"id": "en", "name": "English", "locale": "en", "customerAdded": false}, map[string]any{"id": "customlang", "name": "Custom language", "locale": "xx-XX", "customerAdded": true})
	m["/deviceAuthenticationPolicies"] = inventoryHAL(map[string]any{"id": "default-policy", "name": "Default devices", "default": true}, map[string]any{"id": "custom-policy", "name": "High assurance", "default": false})
	m["/resources"] = inventoryHAL(map[string]any{"id": "custom", "name": "Orders API", "type": "CUSTOM", "introspectEndpointAuthMethod": "CLIENT_SECRET_BASIC"}, map[string]any{"id": "openid", "name": "openid", "type": "OPENID_CONNECT"}, map[string]any{"id": "p1api", "name": "PingOne API", "type": "PINGONE_API"})
	m["/templates"] = inventoryHAL(map[string]any{"id": "unlisted_custom_template", "displayName": "Dynamic template"})
	m["/templates/unlisted_custom_template/contents"] = inventoryHAL(map[string]any{"id": "email-content", "locale": "en", "deliveryMethod": "Email", "body": "Example"}, map[string]any{"id": "sms-content", "locale": "en", "deliveryMethod": "SMS", "body": "Example SMS"})
	for _, path := range []string{"/agreements/object/languages", "/agreements/object/languages/object/revisions", "/applications/app/attributes", "/applications/app/flowPolicyAssignments", "/applications/app/grants", "/applications/app/roleAssignments", "/applications/app/signOnPolicyAssignments", "/applications/app/pushCredentials", "/applicationResources/object/permissions", "/apiServers/object/operations", "/applicationRoles/object/permissions", "/credentialTypes/object/issuanceRules", "/gateways/object/credentials", "/gateways/object/roleAssignments", "/groups/object/memberOfGroups", "/groups/object/roleAssignments", "/identityProviders/object/attributes", "/resources/custom/attributes", "/schemas/object/attributes", "/signOnPolicies/object/actions", "/emailDomains/object/trustedEmails", "/voicePhrases/object/contents"} {
		m[path] = inventoryHAL(inventoryNamed("object", "Configured child"))
	}
	m["/applications/app/metadata"] = map[string]any{"metadata": map[string]any{"source": "configuration"}}
	m["/apiServers/object/deployment"] = map[string]any{"status": map[string]any{"code": "DEPLOYMENT_SUCCESSFUL"}}
	m["/populations/population/defaultIdentityProvider"] = map[string]any{"id": "object"}
	for _, locale := range []string{"en", "xx-XX"} {
		m["/translations/"+locale] = inventoryHAL(map[string]any{"key": "login", "value": "Sign in"})
	}
	for _, resource := range []string{"custom", "openid", "p1api"} {
		m["/resources/"+resource+"/scopes"] = inventoryHAL(inventoryNamed(resource+"-scope", "Read"))
	}
	return m
}

func inventoryServer(t *testing.T, fixtures map[string]map[string]any) (*APIInventory, *[]string) {
	t.Helper()
	var mu sync.Mutex
	calls := []string{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if strings.Contains(r.URL.RawQuery, "NEVER") || strings.Contains(r.URL.Path, "NEVER") {
			t.Error("credential in request URL")
		}
		if r.URL.Path == "/"+inventoryWorkerEnv+"/as/token" {
			id, secret, ok := r.BasicAuth()
			if r.Method != "POST" || !ok || id != "worker-app" || secret != "NEVER-PRINT-SECRET" {
				t.Error("incorrect token request")
			}
			if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("client_secret") != "" {
				t.Error("incorrect token form")
			}
			fmt.Fprint(w, `{"access_token":"BEARER-NEVER-LOG","token_type":"Bearer"}`)
			return
		}
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer BEARER-NEVER-LOG" {
			t.Error("API request was not an authenticated GET")
		}
		p := strings.TrimPrefix(r.URL.Path, "/v1/environments/"+inventoryEnv)
		if unsafeInventoryPath(p) || strings.Contains(p, "/secret") {
			t.Errorf("forbidden inventory endpoint %s", p)
		}
		data, ok := fixtures[p]
		if !ok {
			t.Errorf("unexpected endpoint %s", p)
			http.NotFound(w, r)
			return
		}
		if status, ok := data["_testHTTPStatus"].(int); ok {
			w.WriteHeader(status)
			fmt.Fprint(w, "PRIVATE RESPONSE SECRET")
			return
		}
		json.NewEncoder(w).Encode(data)
	}))
	t.Cleanup(srv.Close)
	return &APIInventory{HTTP: srv.Client(), APIBase: srv.URL + "/v1", AuthBase: srv.URL, Services: []string{"base", "sso", "mfa", "risk", "authorize", "credentials", "verify"}}, &calls
}

func TestInventoryEveryRecipeWithConfigurationFixtures(t *testing.T) {
	a, calls := inventoryServer(t, inventoryFixtures())
	got, err := a.List(context.Background(), inventoryCredentials())
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	addresses := map[string]SourceResource{}
	for _, r := range got {
		types[r.Type]++
		addresses[r.String()] = r
		if !resourcecatalog.Allowed(r.Type) {
			t.Fatalf("disallowed %s", r.Type)
		}
		if !strings.HasPrefix(r.ImportID, inventoryEnv) {
			t.Fatalf("wrong import environment %s", r.ImportID)
		}
	}
	for _, r := range inventoryRecipes {
		if len(r.Steps) > 0 && types[r.Type] == 0 {
			t.Errorf("fixture did not exercise %s", r.Type)
		}
	}
	if types["pingone_notification_template_content"] != 2 {
		t.Fatal("dynamic template content missing")
	}
	for _, typ := range []string{"pingone_resource_scope_openid", "pingone_resource_scope_pingone_api"} {
		for _, r := range got {
			if r.Type == typ && r.ImportID != inventoryEnv+"/"+r.ID {
				t.Errorf("scope importer got parent resource instead of scope ID: %s", r.ImportID)
			}
		}
	}
	if types["pingone_application"] != 1 || types["pingone_system_application"] != 1 || types["pingone_mfa_device_policy"] != 1 || types["pingone_mfa_device_policy_default"] != 1 || types["pingone_language"] != 1 || types["pingone_language_update"] != 1 {
		t.Fatal("overlapping physical resource classification")
	}
	counts := map[string]int{}
	for _, call := range *calls {
		counts[call]++
	}
	for call, n := range counts {
		if n > 1 {
			t.Errorf("uncached endpoint %s fetched %d times", call, n)
		}
	}
}

func TestInventoryPaginationBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, next string
		wantError  bool
	}{
		{"relative cursor", "?cursor=second", false}, {"foreign origin", "https://example.test/v1/environments/" + inventoryEnv + "/groups?cursor=x", true}, {"users path", "/v1/environments/" + inventoryEnv + "/users", true}, {"credentials query", "?client_secret=do-not-send", true},
		{"malformed query", "?expand=users;cursor=second", true}, {"cycle", "?cursor=second", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				v := inventoryHAL(inventoryNamed("g", "Group"))
				if requests == 1 || test.name == "cycle" {
					v["_links"] = map[string]any{"next": map[string]any{"href": test.next}}
				}
				json.NewEncoder(w).Encode(v)
			}))
			defer srv.Close()
			s := &inventorySession{ctx: context.Background(), client: srv.Client(), base: srv.URL + "/v1", cache: map[string]map[string]any{}, pages: map[string][]map[string]any{}}
			got, err := s.records("/environments/"+inventoryEnv+"/groups", "pingone_group", false)
			if (err != nil) != test.wantError {
				t.Fatalf("got %d records, err %v", len(got), err)
			}
			if !test.wantError && (len(got) != 2 || requests != 2) {
				t.Fatal("did not read every page")
			}
			if test.wantError && test.name != "cycle" && requests != 1 {
				t.Fatal("followed untrusted pagination link")
			}
		})
	}
}
func TestInventoryErrorsDoNotExposeBodiesOrCredentials(t *testing.T) {
	for _, status := range []int{302, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.test/leak")
				w.WriteHeader(status)
				fmt.Fprint(w, "NEVER-PRINT-SECRET BEARER-NEVER-LOG")
			}))
			defer srv.Close()
			a := &APIInventory{HTTP: srv.Client(), APIBase: srv.URL + "/v1", AuthBase: srv.URL}
			_, err := a.List(context.Background(), inventoryCredentials())
			if err == nil {
				t.Fatal("error status silently skipped")
			}
			if strings.Contains(err.Error(), "NEVER") || strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("unsafe diagnostic %s", err)
			}
			var statusErr *InventoryHTTPError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != status {
				t.Fatalf("missing status: %v", err)
			}
		})
	}
}
func TestInventoryRejectsPublicOverrideAndBadCredentials(t *testing.T) {
	for _, a := range []*APIInventory{{APIBase: "https://example.test/v1", AuthBase: "https://example.test"}, {APIBase: "http://localhost/v1", AuthBase: "http://localhost"}} {
		if _, err := a.List(context.Background(), inventoryCredentials()); err == nil {
			t.Fatal("unsafe endpoint override")
		}
	}
	c := inventoryCredentials()
	c.ExportEnvironmentID = "../../users"
	if _, err := (&APIInventory{}).List(context.Background(), c); err == nil {
		t.Fatal("unsafe environment")
	}
}
func TestInventoryLabelCollisionAndEnvironmentStability(t *testing.T) {
	f := inventoryFixtures()
	f["/groups"] = inventoryHAL(inventoryNamed("a", "Duplicate"), inventoryNamed("b", "Duplicate"))
	// Keep nested fixtures empty so collision, rather than fixture routing, decides.
	for _, id := range []string{"a", "b"} {
		for _, child := range []string{"memberOfGroups", "roleAssignments"} {
			f["/groups/"+id+"/"+child] = inventoryHAL()
		}
	}
	a, _ := inventoryServer(t, f)
	_, err := a.List(context.Background(), inventoryCredentials())
	var collision *LabelCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("expected collision, got %v", err)
	}
	n1 := inventoryContentName(map[string]any{"id": "11111111-1111-4111-8111-111111111111", "text": "Terms", "createdAt": "yesterday"}, nil)
	n2 := inventoryContentName(map[string]any{"id": "22222222-2222-4222-8222-222222222222", "text": "Terms", "createdAt": "today"}, nil)
	if n1 != n2 {
		t.Fatal("source IDs changed stable content label")
	}
}
func TestInventoryManifestAccountsForEveryAllowedNonDaVinciType(t *testing.T) {
	recipes := map[string]bool{}
	for _, r := range inventoryRecipes {
		if recipes[r.Type] {
			t.Fatal("duplicate recipe")
		}
		recipes[r.Type] = true
	}
	for _, r := range resourcecatalog.All() {
		if r.Allowed && r.Family != "davinci" && !recipes[r.Type] {
			t.Errorf("unaccounted provider resource %s", r.Type)
		}
	}
}

func TestInventoryImageReferenceDiscoveryAndExplicitLookup(t *testing.T) {
	const id = "33333333-3333-4333-8333-333333333333"
	fixtures := inventoryFixtures()
	fixtures["/brandingSettings"] = map[string]any{"logo": map[string]any{"id": id}}
	// The pinned image response has targets but has no name or fileName.
	fixtures["/images/"+id] = map[string]any{"id": id, "targets": map[string]any{"original": map[string]any{"href": "https://images.example.test/opaque-object"}}}
	a, _ := inventoryServer(t, fixtures)
	got, err := a.List(context.Background(), inventoryCredentials())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range got {
		if r.Type == "pingone_image" {
			found = true
			if r.ID != id || r.Label != "branding_settings__logo" || r.ImportID != inventoryEnv+"/"+id {
				t.Fatalf("unexpected image %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("configuration-referenced image missing")
	}
	r, err := a.Lookup(context.Background(), inventoryCredentials(), "pingone_image", id)
	if err != nil || r.ID != id {
		t.Fatalf("explicit lookup: %v", err)
	}
	if _, err := a.Lookup(context.Background(), inventoryCredentials(), "pingone_user", id); err == nil {
		t.Fatal("lookup escaped type allowlist")
	}
	if _, err := a.Lookup(context.Background(), inventoryCredentials(), "pingone_image", "../users"); err == nil {
		t.Fatal("lookup escaped image path")
	}
}

type inventoryRoundTrip func(*http.Request) (*http.Response, error)

func (f inventoryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestInventoryProductionRegions(t *testing.T) {
	for region, domain := range map[string]string{"AP": "asia", "AU": "com.au", "CA": "ca", "EU": "eu", "NA": "com", "SG": "sg"} {
		t.Run(region, func(t *testing.T) {
			calls := 0
			a := &APIInventory{HTTP: &http.Client{Transport: inventoryRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				body := `{"access_token":"token"}`
				status := 200
				if calls == 1 {
					if r.URL.String() != "https://auth.pingone."+domain+"/"+inventoryWorkerEnv+"/as/token" {
						t.Errorf("wrong regional auth URL: %s", r.URL)
					}
				} else {
					if r.URL.Host != "api.pingone."+domain || !strings.HasPrefix(r.URL.Path, "/v1/environments/"+inventoryEnv) {
						t.Errorf("wrong regional API URL: %s", r.URL)
					}
					status = 403
					body = "PRIVATE RESPONSE SECRET"
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}}
			c := inventoryCredentials()
			c.RegionCode = region
			_, err := a.List(context.Background(), c)
			if err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("API error handling: %v", err)
			}
			if calls != 2 {
				t.Fatalf("made %d requests", calls)
			}
		})
	}
}
func TestInventoryResponseAndCancellationLimits(t *testing.T) {
	for _, test := range []struct {
		name, body string
		bytes      int64
	}{
		{"malformed", "not JSON", 0}, {"oversize", strings.Repeat(" ", (8<<20)+1), 0}, {"aggregate", `{"id":"x"}`, 64 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, test.body) }))
			defer srv.Close()
			s := &inventorySession{ctx: context.Background(), client: srv.Client(), base: srv.URL, cache: map[string]map[string]any{}, bytes: test.bytes}
			if _, err := s.get("/environments/"+inventoryEnv, "pingone_environment"); err == nil {
				t.Fatal("invalid/unbounded response accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &APIInventory{}
	if _, err := a.List(ctx, inventoryCredentials()); err == nil {
		t.Fatal("canceled context was ignored")
	}
}

func TestInventoryBillOfMaterialsCoreOnlyGating(t *testing.T) {
	fixtures := inventoryFixtures()
	fixtures["/billOfMaterials"] = map[string]any{"products": []any{map[string]any{"type": "PING_ONE_BASE"}}}
	a, calls := inventoryServer(t, fixtures)
	a.Services = nil
	got, err := a.List(context.Background(), inventoryCredentials())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		catalog, _ := resourcecatalog.Lookup(r.Type)
		if catalog.Family != "base" && catalog.Family != "sso" {
			t.Errorf("unlicensed resource %s", r.Type)
		}
		if r.Type == "pingone_application_resource" {
			t.Error("Authorize endpoint exposed under SSO provider namespace")
		}
	}
	for _, call := range *calls {
		for _, fragment := range []string{"/apiServers", "/applicationResources", "/applicationRoles", "/decisionEndpoints", "/credentialTypes", "/credentialIssuerProfile", "/digitalWallet", "/risk", "/verify", "/voicePhrases", "/deviceAuthentication", "/mfaSettings", "/fido2", "/pushCredentials"} {
			if strings.Contains(call, fragment) {
				t.Errorf("unlicensed service request: %s", call)
			}
		}
	}
	enabled, err := a.ServiceEnabled(context.Background(), inventoryCredentials(), "davinci")
	if err != nil || enabled {
		t.Fatalf("DaVinci service should be disabled: %v", err)
	}
}
func TestInventoryEnabledServiceFailureIsNotSkipped(t *testing.T) {
	fixtures := inventoryFixtures()
	fixtures["/billOfMaterials"] = map[string]any{"products": []any{map[string]any{"type": "PING_ONE_BASE"}, map[string]any{"type": "PING_ONE_MFA"}}}
	fixtures["/deviceAuthenticationPolicies"] = map[string]any{"_testHTTPStatus": 403}
	a, _ := inventoryServer(t, fixtures)
	a.Services = nil
	_, err := a.List(context.Background(), inventoryCredentials())
	var status *InventoryHTTPError
	if !errors.As(err, &status) || status.StatusCode != 403 || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("enabled MFA 403 must fail without exposing its body: %v", err)
	}
}
func TestInventoryExplicitServicesAndBOMProductMappings(t *testing.T) {
	fixtures := inventoryFixtures()
	a, calls := inventoryServer(t, fixtures)
	enabled, err := a.ServiceEnabled(context.Background(), inventoryCredentials(), "verify")
	if err != nil || !enabled || len(*calls) != 0 {
		t.Fatal("explicit service detection made an HTTP call")
	}
	if _, err := a.ServiceEnabled(context.Background(), inventoryCredentials(), "users"); err == nil {
		t.Fatal("unknown family accepted")
	}
	a.Services = []string{"users"}
	if _, err := a.List(context.Background(), inventoryCredentials()); err == nil || len(*calls) != 0 {
		t.Fatal("unknown operator service accepted or made an HTTP call")
	}
	for product, family := range map[string]string{"PING_ONE_MFA": "mfa", "PING_ONE_RISK": "risk", "PING_ONE_FRAUD": "risk", "PING_ONE_AUTHORIZE": "authorize", "PING_ONE_CREDENTIALS": "credentials", "PING_ONE_VERIFY": "verify", "PING_ONE_DAVINCI": "davinci", "PING_ONE_ORCHESTRATE": "davinci"} {
		t.Run(product, func(t *testing.T) {
			fixtures := inventoryFixtures()
			fixtures["/billOfMaterials"] = map[string]any{"products": []any{map[string]any{"type": product}}}
			a, _ := inventoryServer(t, fixtures)
			a.Services = nil
			enabled, err := a.ServiceEnabled(context.Background(), inventoryCredentials(), family)
			if err != nil || !enabled {
				t.Fatalf("product %s should enable %s: %v", product, family, err)
			}
		})
	}
}
func TestInventoryBOMFailureExplainsExplicitConfiguration(t *testing.T) {
	fixtures := inventoryFixtures()
	fixtures["/billOfMaterials"] = map[string]any{"_testHTTPStatus": 403}
	a, calls := inventoryServer(t, fixtures)
	a.Services = nil
	_, err := a.List(context.Background(), inventoryCredentials())
	if err == nil || !strings.Contains(err.Error(), "pingone.services") || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("missing safe remediation: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatal("BOM detection failure proceeded with inventory")
	}
}

func TestInventoryImageDiscoveryUsesReferenceFields(t *testing.T) {
	ids := map[string]string{}
	const appID = "44444444-4444-4444-8444-444444444444"
	const imageID = "55555555-5555-4555-8555-555555555555"
	inventoryImages(map[string]any{"id": appID, "name": "Image application", "icon": map[string]any{"id": imageID}}, "Image_application", ids)
	if len(ids) != 1 || ids[appID] != "" || ids[imageID] != "Image_application__icon" {
		t.Fatalf("object name leaked into image reference discovery: %v", ids)
	}
}
