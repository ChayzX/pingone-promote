package neutralize_test

import (
	"errors"
	"strings"
	"testing"

	"pingone-promote/internal/neutralize"
)

func coreInput(src string) *neutralize.Input {
	return &neutralize.Input{Files: map[string][]byte{
		"ping-export-module/variables.tf": {},
		"ping-export-module/core.tf":      []byte(src),
	}}
}

func TestCoreLiftApplicationAndIdentityProvider(t *testing.T) {
	in := coreInput(`resource "pingone_application" "web" {
  name = "Customer portal"
  description = "https://documentation.example/shared"
  oidc_options = {
    redirect_uris = ["https://dev.example/callback"]
    post_logout_redirect_uris = ["https://dev.example/logout"]
    grant_types = ["AUTHORIZATION_CODE"]
    token_endpoint_auth_method = "CLIENT_SECRET_BASIC"
  }
  saml_options = { acs_urls = ["https://dev.example/acs"], sp_entity_id = "urn:dev:portal" }
}
resource "pingone_identity_provider" "partner" {
  name = "Partner"
  saml = { sso_endpoint = "https://idp.dev.example/sso", idp_entity_id = "https://idp.dev.example" }
}
`)
	r, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"application_web_oidc_options_redirect_uris", "application_web_oidc_options_post_logout_redirect_uris", "application_web_saml_options_acs_urls", "application_web_saml_options_sp_entity_id", "identity_provider_partner_saml_sso_endpoint", "identity_provider_partner_saml_idp_entity_id"} {
		v := r.Variables[name]
		if v == nil || v.Kind != neutralize.KindEnvLocal {
			t.Errorf("missing environment variable %s", name)
		}
		if _, ok := r.SourceValues[name]; !ok {
			t.Errorf("missing source value for %s", name)
		}
	}
	text := string(r.Blocks[0].Text)
	for _, want := range []string{"Customer portal", "https://documentation.example/shared", "AUTHORIZATION_CODE", "CLIENT_SECRET_BASIC"} {
		if !strings.Contains(text, want) {
			t.Errorf("shared value %q changed", want)
		}
	}
	if strings.Contains(text, "https://dev.example") {
		t.Fatal("source endpoint retained in shared block")
	}
}

func TestCoreLiftNestedRepeatedGatewayCredentials(t *testing.T) {
	r, err := neutralize.Neutralize(coreInput(`resource "pingone_gateway" "ldap" {
  name = "LDAP"
  servers = ["ldap.dev.example:636"]
  bind_dn = "cn=service,dc=dev,dc=example"
  bind_password = "source-bind-password"
  kerberos = { service_account_password = "source-kerberos-password", service_account_upn = "ldap@dev.example" }
  radius_clients = [
    { ip = "192.0.2.1", shared_secret = "source-radius-one" },
    { ip = "192.0.2.2", shared_secret = "source-radius-two" }
  ]
  user_types { search_base_dn = "dc=first,dc=example" }
  user_types { search_base_dn = "dc=second,dc=example" }
}
`), neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"bind_password", "kerberos_service_account_password", "radius_clients_0_shared_secret", "radius_clients_1_shared_secret"} {
		name := "gateway_ldap_" + path
		v := r.Variables[name]
		if v == nil || v.Kind != neutralize.KindSecret || v.Type != "string" {
			t.Errorf("missing typed secret %s", name)
		}
		if _, ok := r.SourceValues[name]; ok {
			t.Errorf("secret source value persisted for %s", name)
		}
	}
	for _, path := range []string{"servers", "bind_dn", "kerberos_service_account_upn", "radius_clients_0_ip", "radius_clients_1_ip", "user_types_0_search_base_dn", "user_types_1_search_base_dn"} {
		if r.Variables["gateway_ldap_"+path] == nil {
			t.Errorf("missing nested lift %s", path)
		}
	}
	if strings.Contains(string(r.Blocks[0].Text), "source-") {
		t.Fatal("source credential leaked in shared HCL")
	}
	for _, v := range r.SourceValues {
		if strings.Contains(v.GoString(), "source-") {
			t.Fatal("source credential leaked in source overlay")
		}
	}
}

func TestCoreLiftMFAEndpointsAndPublicCertificates(t *testing.T) {
	r, err := neutralize.Neutralize(coreInput(`resource "pingone_mfa_fido2_policy" "passkey" {
  name = "Passkeys"
  relying_party_id = "login.dev.example"
  user_verification = { option = "REQUIRED", enforce_during_authentication = true }
}
resource "pingone_certificate" "partner" {
  pem_file = "-----BEGIN CERTIFICATE-----\nPUBLIC-CERTIFICATE\n-----END CERTIFICATE-----"
  usage_type = "SIGNING"
}
`), neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mfa_fido2_policy_passkey_relying_party_id", "certificate_partner_pem_file"} {
		if v := r.Variables[name]; v == nil || v.Kind != neutralize.KindEnvLocal {
			t.Errorf("missing environment literal %s", name)
		}
	}
	if len(r.Findings) != 0 {
		t.Fatalf("public certificate flagged as secret: %#v", r.Findings)
	}
}

func TestCoreLiftPreservesReferencesAndMixedCollections(t *testing.T) {
	in := coreInput(`resource "pingone_application" "web" {
  name = "Web"
  oidc_options = {
    home_page_url = var.home_page
    redirect_uris = [var.callback, "https://dev.example/callback"]
    post_logout_redirect_uris = [pingone_application.other.name]
  }
}
resource "pingone_application" "other" { name = "Other" }
`)
	in.Files["ping-export-module/variables.tf"] = []byte("variable \"home_page\" {}\nvariable \"callback\" {}\n")
	r, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	text := string(r.Block("pingone_application.web").Text)
	for _, want := range []string{"var.home_page", "var.callback", "pingone_application.other.name", "var.application_web_oidc_options_redirect_uris_1"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing preserved reference or literal lift %s in %s", want, text)
		}
	}
	if r.Variables["application_web_oidc_options_home_page_url"] != nil {
		t.Fatal("existing variable reference lifted again")
	}
}

func TestCoreLiftRejectsSecretInPublicCertificateWithoutEcho(t *testing.T) {
	_, err := neutralize.Neutralize(coreInput(`resource "pingone_certificate" "partner" {
  pem_file = "-----BEGIN PRIVATE KEY-----\nSECRET-PRIVATE-CONTENT\n-----END PRIVATE KEY-----"
  usage_type = "SIGNING"
}

`), neutralize.Options{SourceEnvironmentID: devEnv})
	if err == nil {
		t.Fatal("private key in public certificate must fail closed")
	}
	if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "SECRET-PRIVATE-CONTENT") {
		t.Fatal("error echoes source secret")
	}
}

func TestCoreLiftSecretCollectionHasConcreteTypeWithoutValues(t *testing.T) {
	r, err := neutralize.Neutralize(coreInput(`resource "pingone_webhook" "audit" {
  http_endpoint_url = "https://audit.dev.example/events"
  http_endpoint_headers = { Authorization = "Bearer source-credential" }
}
`), neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	name := "webhook_audit_http_endpoint_headers"
	v := r.Variables[name]
	if v == nil || v.Kind != neutralize.KindSecret || v.Type != `object({"Authorization" = string})` {
		t.Fatalf("missing concrete sensitive collection: %#v", v)
	}
	if _, exists := r.SourceValues[name]; exists {
		t.Fatal("header credential entered source values")
	}
	if strings.Contains(string(r.Blocks[0].Text), "source-credential") {
		t.Fatal("header credential retained in HCL")
	}
}

func TestCoreLiftCustomRuleCannotRetainSecretSource(t *testing.T) {
	r, err := neutralize.Neutralize(coreInput(`resource "pingone_gateway" "ldap" {
  bind_password = "source-password"
}
`), neutralize.Options{SourceEnvironmentID: devEnv, Lift: []neutralize.LiftRule{{ResourceType: "pingone_gateway", Attribute: "bind_password"}}})
	if err != nil {
		t.Fatal(err)
	}
	name := "gateway_ldap_bind_password"
	if r.Variables[name].Kind != neutralize.KindSecret {
		t.Fatal("custom rule downgraded a credential to an environment value")
	}
	if _, exists := r.SourceValues[name]; exists {
		t.Fatal("custom rule persisted a source credential")
	}
}

func TestCoreLiftRejectsCredentialInEndpointWithoutEcho(t *testing.T) {
	for _, endpoint := range []string{"https://user:source-password@example.test/", "https://example.test/?client_secret=source-password"} {
		_, err := neutralize.Neutralize(coreInput(`resource "pingone_webhook" "audit" {
  http_endpoint_url = "`+endpoint+`"
}
`), neutralize.Options{SourceEnvironmentID: devEnv})
		if err == nil {
			t.Fatal("credential-bearing endpoint must fail closed")
		}
		if strings.Contains(err.Error(), "source-password") {
			t.Fatal("error echoes endpoint credential")
		}
	}
}

func TestCoreLiftVariableCollisionFailsClosed(t *testing.T) {
	in := coreInput(`resource "pingone_application" "web" {
  oidc_options = { redirect_uris = ["https://dev.example/callback"], home_page_url = var.application_web_oidc_options_redirect_uris }
}
`)
	in.Files["ping-export-module/variables.tf"] = []byte(`variable "application_web_oidc_options_redirect_uris" {}`)
	_, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv})
	var collision *neutralize.CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("expected variable collision, got %v", err)
	}
}

func TestCoreLiftMixedSecretMapPreservesReferences(t *testing.T) {
	in := coreInput(`resource "pingone_webhook" "audit" {
  http_endpoint_headers = { Authorization = var.authorization, "X-Api-Key" = "source-api-key" }
}
`)
	in.Files["ping-export-module/variables.tf"] = []byte("variable \"authorization\" {\n sensitive = true\n}\n")
	r, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	text := string(r.Blocks[0].Text)
	if !strings.Contains(text, "var.authorization") {
		t.Fatal("existing secret reference changed")
	}
	name := "webhook_audit_http_endpoint_headers_X_Api_Key"
	if v := r.Variables[name]; v == nil || v.Kind != neutralize.KindSecret {
		t.Fatalf("literal in mixed sensitive map not protected: %s", text)
	}
	if _, exists := r.SourceValues[name]; exists {
		t.Fatal("mixed map secret entered source values")
	}
	if strings.Contains(text, "source-api-key") {
		t.Fatal("mixed map secret retained in HCL")
	}
}

func TestCoreLiftCustomParentRuleProtectsNestedSecret(t *testing.T) {
	r, err := neutralize.Neutralize(coreInput(`resource "pingone_gateway" "ldap" {
  kerberos = { service_account_password = "source-password", service_account_upn = "user@dev.example", retain_previous_credentials_mins = 5 }
}
`), neutralize.Options{SourceEnvironmentID: devEnv, Lift: []neutralize.LiftRule{{ResourceType: "pingone_gateway", Attribute: "kerberos"}}})
	if err != nil {
		t.Fatal(err)
	}
	name := "gateway_ldap_kerberos"
	if v := r.Variables[name]; v == nil || v.Kind != neutralize.KindSecret || v.Type == "any" {
		t.Fatalf("nested credential not protected by concrete type: %#v", v)
	}
	if _, exists := r.SourceValues[name]; exists {
		t.Fatal("parent object persisted a nested source credential")
	}
}

func TestCoreLiftExistingCredentialVariableDropsSourceValue(t *testing.T) {
	in := coreInput(`resource "pingone_gateway" "ldap" {
  bind_password = var.gateway_bind
}
`)
	in.Files["ping-export-module/variables.tf"] = []byte(`variable "gateway_bind" { type = string }`)
	in.Files["source.auto.tfvars"] = []byte(`gateway_bind = "source-password"`)
	r, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.Blocks[0].Text), "var.gateway_bind") {
		t.Fatal("existing credential reference changed")
	}
	if r.Variables["gateway_bind"].Kind != neutralize.KindSecret {
		t.Fatal("credential variable was not made sensitive")
	}
	if _, exists := r.SourceValues["gateway_bind"]; exists {
		t.Fatal("source credential retained behind existing variable")
	}
}

func TestCoreLiftLiteralSecretUsesMappingName(t *testing.T) {
	in := coreInput(`resource "pingone_gateway" "ldap" { bind_password="source-password" }`)
	r, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv, SecretNames: map[string]string{"gateway_ldap_bind_password": "LDAP_PASSWORD"}})
	if err != nil {
		t.Fatal(err)
	}
	v := r.Variables["LDAP_PASSWORD"]
	if v == nil || v.Kind != neutralize.KindSecret || v.Origin != "gateway_ldap_bind_password" {
		t.Fatal("literal secret does not expose a valid mapping key")
	}
	if !strings.Contains(string(r.Blocks[0].Text), "var.LDAP_PASSWORD") {
		t.Fatal("mapped variable reference missing")
	}
	if len(r.SourceValues) != 1 {
		t.Fatal("literal secret source value retained")
	}
}

func TestCoreLiftCredentialFunctionsFailClosed(t *testing.T) {
	for _, expr := range []string{`join("",["source-password"])`, `join("",[var.existing,"source-password"])`, `"prefix-${var.existing}-source-password"`, `["source-password",var.existing][0]`} {
		in := coreInput(`resource "pingone_gateway" "ldap" { bind_password=` + expr + ` }`)
		in.Files["ping-export-module/variables.tf"] = []byte(`variable "existing" {}`)
		_, err := neutralize.Neutralize(in, neutralize.Options{SourceEnvironmentID: devEnv})
		if err == nil {
			t.Fatal("unsupported credential expression accepted")
		}
		if strings.Contains(err.Error(), "source-password") {
			t.Fatal("credential error echoes input")
		}
	}
}

func TestCoreLiftEnvironmentIdentityAndProtectionArePerEnvironment(t *testing.T) {
	r, err := neutralize.Neutralize(coreInput(`resource "pingone_environment" "managed" {
 name="Development"
 type="SANDBOX"
 region="NA"
 solution="CUSTOMER"
 license_id="license-dev"
}`), neutralize.Options{SourceEnvironmentID: devEnv})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"name", "type", "region", "solution", "license_id"} {
		if v := r.Variables["environment_managed_"+field]; v == nil || v.Kind != neutralize.KindEnvLocal {
			t.Fatal("environment identity/protection was shared")
		}
	}
	text := string(r.Blocks[0].Text)
	if strings.Contains(text, `"Development"`) || strings.Contains(text, `"SANDBOX"`) {
		t.Fatal("source environment identity/protection leaked into shared definitions")
	}
}
