// Package catalog is the single registry of PingOne resource types that
// pingone-promote knows (ADR 0019). Every resource of the pinned
// pingidentity/pingone provider has an entry: which exporter produces it,
// how it is read from the PingOne Management API, how it is labelled and
// imported, which of its values belong to one environment, and, when it
// cannot be exported, why. The exporters, the neutralizer, the merge and
// docs/coverage.md all read this registry.
//
// API paths, field names and import formats were written from the provider
// documentation and the PingOne Platform API reference without a tenant to
// test against. Every type therefore carries Verified: false until a live
// acceptance run (docs/acceptance/core-export.md) confirms it.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ProviderVersion is the pinned pingidentity/pingone provider.
const ProviderVersion = "1.24.0"

// Exporters.
const (
	// ExporterTerraformer is the pinned pingcli-terraformer (DaVinci).
	ExporterTerraformer = "pingcli-terraformer"
	// ExporterAPI is the native read-only PingOne API exporter.
	ExporterAPI = "pingone-api"
)

// Coverage status.
const (
	Covered     = "covered"
	Limited     = "covered with documented limits"
	NotPossible = "not possible"
)

// Import formats: the part after "<environment_id>/" that import_ids holds.
const (
	ImportID       = "{id}"          // <env>/<id>
	ImportParentID = "{parent}/{id}" // <env>/<parent id>/<id>
	// ImportEnvironment is a singleton: the import ID is the environment ID
	// itself, so the import block needs no import_ids entry.
	ImportEnvironment = "{env}"
)

// Def describes one resource type.
type Def struct {
	Type   string `json:"type"`
	Family string `json:"family"`
	// Exporter is ExporterTerraformer, ExporterAPI or "" (not exported).
	Exporter string `json:"exporter,omitempty"`
	Status   string `json:"status"`
	// Reason explains a limit or why the type is not exported.
	Reason string `json:"reason,omitempty"`

	// Path is the API collection (or singleton) path below
	// /v1/environments/{env}/. {parent} is the parent object's ID.
	Path string `json:"path,omitempty"`
	// Embedded is the _embedded key of the collection response.
	Embedded string `json:"embedded,omitempty"`
	// Parent is the type whose objects own this type's objects; ParentAttr
	// is the schema attribute that holds the parent ID.
	Parent     string `json:"parent,omitempty"`
	ParentAttr string `json:"parentAttr,omitempty"`
	// Owned children are exported together with their parent, like a flow's
	// deploy and enable (they can still be excluded).
	Owned bool `json:"owned,omitempty"`
	// Singleton types have one object per environment, read from Path.
	Singleton bool `json:"singleton,omitempty"`
	// NameAttrs are the API fields (dot paths) that form the label, in
	// order; the parent's label is prefixed for child types.
	NameAttrs []string `json:"nameAttrs,omitempty"`
	// Import is the import_ids value format.
	Import string `json:"import"`
	// Builtin marks API objects that exist in every environment on their own
	// (field=value, for example "default=true"); they are adopted with
	// import IDs in every environment, never created.
	Builtin string `json:"builtin,omitempty"`
	// EnvLocal are schema attribute paths whose values differ per
	// environment; they become per-environment variables.
	EnvLocal []string `json:"envLocal,omitempty"`
	// Hoist maps a nested schema attribute to the condition under which it
	// is read from the API object's root ("protocol=OPENID_CONNECT").
	Hoist map[string]string `json:"hoist,omitempty"`
	// Rename maps a schema attribute path to its API field path when the
	// default (camelCase of each segment) does not hold; "-" skips it.
	Rename map[string]string `json:"rename,omitempty"`
	// Fallback lists API sub-objects searched when a field is not at the
	// object's root (a theme's colours live under "configuration").
	Fallback []string `json:"fallback,omitempty"`
	// Secrets declares secret attributes the API never returns but the
	// resource needs, with the condition under which it needs them
	// ("type=LDAP"; "" always). They become customer_secrets.yml entries.
	Secrets map[string]string `json:"secrets,omitempty"`
	// Match keeps only API objects that satisfy a condition ("default!=true",
	// "type=A|B"); ParentMatch does the same for the parent object. Types
	// that share an API collection are told apart this way.
	Match       string `json:"match,omitempty"`
	ParentMatch string `json:"parentMatch,omitempty"`
	// Verified is true only after a live acceptance run.
	Verified bool `json:"verified"`
}

// Families, in delivery order.
var Families = []string{
	"davinci", "applications", "resources_scopes_grants", "sign_on", "directory", "mfa_devices",
	"identity_providers", "gateways", "branding", "notifications", "forms", "agreements", "languages",
	"protect", "authorize", "verify", "credentials", "platform", "identity_data",
}

const userData = "holds users, user data or user assignments; pingone-promote never exports identity data"

func api(t, fam, path, emb string, name ...string) Def {
	return Def{Type: t, Family: fam, Exporter: ExporterAPI, Status: Covered, Path: path, Embedded: emb, NameAttrs: name, Import: ImportID}
}

func child(t, fam, parent, parentAttr, path, emb string, owned bool, name ...string) Def {
	d := api(t, fam, path, emb, name...)
	d.Parent, d.ParentAttr, d.Owned, d.Import = parent, parentAttr, owned, ImportParentID
	return d
}

func single(t, fam, path string) Def {
	return Def{Type: t, Family: fam, Exporter: ExporterAPI, Status: Covered, Path: path, Singleton: true, Import: ImportEnvironment}
}

func none(t, fam, reason string) Def {
	return Def{Type: t, Family: fam, Status: NotPossible, Reason: reason, Import: ImportID}
}

func dv(t string) Def {
	return Def{Type: t, Family: "davinci", Exporter: ExporterTerraformer, Status: Covered, Import: ImportID, Verified: true}
}

func limit(d Def, reason string) Def { d.Status, d.Reason = Limited, reason; return d }

func with(d Def, f func(*Def)) Def { f(&d); return d }

var defs = []Def{
	// --- DaVinci (pingcli-terraformer, verified live in earlier releases) ---
	dv("pingone_davinci_application"),
	with(dv("pingone_davinci_application_flow_policy"), func(d *Def) { d.Import = ImportParentID; d.Parent = "pingone_davinci_application" }),
	dv("pingone_davinci_connector_instance"),
	dv("pingone_davinci_flow"),
	with(dv("pingone_davinci_flow_deploy"), func(d *Def) { d.Parent, d.Owned = "pingone_davinci_flow", true }),
	with(dv("pingone_davinci_flow_enable"), func(d *Def) { d.Parent, d.Owned = "pingone_davinci_flow", true }),
	dv("pingone_davinci_variable"),
	none("pingone_davinci_application_key", "davinci", "the API key is generated in each environment and read by Terraform there, never copied"),
	none("pingone_davinci_application_secret", "davinci", "the client secret is generated in each environment and read by Terraform there, never copied"),

	// --- Applications ---
	with(api("pingone_application", "applications", "applications", "applications", "name"), func(d *Def) {
		d.Match = "type!=PING_ONE_SELF_SERVICE|PING_ONE_PORTAL"
		d.Hoist = map[string]string{"oidc_options": "protocol=OPENID_CONNECT", "saml_options": "protocol=SAML", "wsfed_options": "protocol=WS_FED"}
		d.Rename = map[string]string{"oidc_options.type": "type", "saml_options.type": "type", "wsfed_options.type": "type",
			"oidc_options.redirect_uris": "redirectUris", "oidc_options.post_logout_redirect_uris": "postLogoutRedirectUris",
			"saml_options.acs_urls": "acsUrls", "saml_options.nameid_format": "nameIdFormat", "saml_options.sp_entity_id": "spEntityId",
			"saml_options.idp_signing_key": "idpSigning.key", "saml_options.idp_signing_key.key_id": "id", "wsfed_options.idp_signing_key": "idpSigning.key",
			"access_control_group_options": "accessControl.group", "access_control_role_type": "accessControl.role.type"}
		d.EnvLocal = []string{"oidc_options.redirect_uris", "oidc_options.post_logout_redirect_uris", "oidc_options.initiate_login_uri",
			"oidc_options.target_link_uri", "oidc_options.home_page_url", "oidc_options.jwks_url", "oidc_options.device_custom_verification_uri",
			"saml_options.acs_urls", "saml_options.sp_entity_id", "saml_options.default_target_url", "saml_options.home_page_url",
			"saml_options.slo_endpoint", "saml_options.slo_response_endpoint", "wsfed_options.domain_name", "wsfed_options.reply_url",
			"wsfed_options.slo_endpoint", "login_page_url", "external_link_options.home_page_url"}
	}),
	limit(child("pingone_application_attribute_mapping", "applications", "pingone_application", "application_id", "applications/{parent}/attributes", "attributes", true, "name"),
		"core mappings that PingOne creates with the application (sub and similar) are adopted by import, not created"),
	child("pingone_application_sign_on_policy_assignment", "applications", "pingone_application", "application_id", "applications/{parent}/signOnPolicyAssignments", "signOnPolicyAssignments", true, "priority"),
	child("pingone_application_flow_policy_assignment", "applications", "pingone_application", "application_id", "applications/{parent}/flowPolicyAssignments", "flowPolicyAssignments", true, "priority"),
	with(child("pingone_application_resource_grant", "applications", "pingone_application", "application_id", "applications/{parent}/grants", "grants", true, "resource.id"), func(d *Def) {
		d.Rename = map[string]string{"custom_resource_id": "resource.id"}
	}),
	child("pingone_application_role_assignment", "applications", "pingone_application", "application_id", "applications/{parent}/roleAssignments", "roleAssignments", true, "role.id", "scope.type"),
	limit(child("pingone_application_metadata", "applications", "pingone_application", "application_id", "applications/{parent}/metadata", "metadata", true, "key"),
		"metadata is read as a key/value map; nested values are exported as strings"),
	none("pingone_application_secret", "applications", "client secrets are generated per environment and read by Terraform in that environment; copying them is forbidden"),
	limit(child("pingone_mfa_application_push_credential", "mfa_devices", "pingone_application", "application_id", "applications/{parent}/pushCredentials", "pushCredentials", true, "type"),
		"FCM, APNs and HMS keys are secrets: they become customer_secrets.yml entries; the API never returns them"),
	with(api("pingone_system_application", "applications", "applications", "applications", "type"), func(d *Def) {
		d.Status, d.Reason, d.Builtin = Limited, "system applications (Self-Service, Application Portal) exist in every environment; they are adopted by import and only their settings are managed", "type=PING_ONE_SELF_SERVICE|PING_ONE_PORTAL"
		d.Match = d.Builtin
	}),
	child("pingone_digital_wallet_application", "credentials", "pingone_application", "application_id", "digitalWalletApplications", "digitalWalletApplications", false, "name"),

	// --- Resources, scopes, grants ---
	with(api("pingone_resource", "resources_scopes_grants", "resources", "resources", "name"), func(d *Def) {
		d.EnvLocal = []string{"audience"}
		d.Builtin = "type=OPENID_CONNECT|PINGONE_API"
	}),
	with(child("pingone_resource_scope", "resources_scopes_grants", "pingone_resource", "resource_id", "resources/{parent}/scopes", "scopes", true, "name"), func(d *Def) {
		d.ParentMatch = "type=CUSTOM"
	}),
	limit(child("pingone_resource_attribute", "resources_scopes_grants", "pingone_resource", "custom_resource_id", "resources/{parent}/attributes", "attributes", true, "name"),
		"predefined attributes of the built-in resources are adopted by import"),
	with(child("pingone_resource_scope_openid", "resources_scopes_grants", "pingone_resource", "", "resources/{parent}/scopes", "scopes", false, "name"), func(d *Def) {
		d.Status, d.Reason, d.Builtin = Limited, "scopes of the built-in OpenID Connect resource; built-in ones (openid, profile, email, ...) are adopted by import", "builtin=true"
		d.ParentMatch = "type=OPENID_CONNECT"
		d.Import = ImportID
	}),
	with(child("pingone_resource_scope_pingone_api", "resources_scopes_grants", "pingone_resource", "", "resources/{parent}/scopes", "scopes", false, "name"), func(d *Def) {
		d.Status, d.Reason, d.Builtin = Limited, "scopes of the built-in PingOne API resource; predefined ones are adopted by import", "builtin=true"
		d.ParentMatch = "type=PINGONE_API"
		d.Import = ImportID
	}),
	none("pingone_resource_secret", "resources_scopes_grants", "resource secrets are generated per environment and read by Terraform there"),

	// --- Sign-on policies and actions ---
	with(api("pingone_sign_on_policy", "sign_on", "signOnPolicies", "signOnPolicies", "name"), func(d *Def) { d.Builtin = "default=true" }),
	with(child("pingone_sign_on_policy_action", "sign_on", "pingone_sign_on_policy", "sign_on_policy_id", "signOnPolicies/{parent}/actions", "actions", true, "priority"), func(d *Def) {
		d.Hoist = map[string]string{"login": "type=LOGIN", "mfa": "type=MULTI_FACTOR_AUTHENTICATION", "identity_provider": "type=IDENTITY_PROVIDER",
			"identifier_first": "type=IDENTIFIER_FIRST", "agreement": "type=AGREEMENT", "progressive_profiling": "type=PROGRESSIVE_PROFILING",
			"pingid": "type=PINGID_AUTHENTICATION", "pingid_windows_login_passwordless": "type=PINGID_WINLOGIN_PASSWORDLESS_AUTHENTICATION"}
		d.Rename = map[string]string{"mfa.device_sign_on_policy_id": "deviceAuthenticationPolicy.id", "registration_local_population_id": "registration.population.id",
			"registration_external_href": "registration.externalHref", "identity_provider.identity_provider_id": "identityProvider.id",
			"agreement.agreement_id": "agreement.id", "social_provider_ids": "-"}
	}),

	// --- Directory ---
	with(api("pingone_population", "directory", "populations", "populations", "name"), func(d *Def) {
		d.Status, d.Reason = Limited, "the default population is managed by pingone_population_default and adopted by import"
		d.Match = "default!=true"
	}),
	with(single("pingone_population_default", "directory", "populations"), func(d *Def) {
		d.Status, d.Reason, d.Singleton, d.Import, d.Embedded, d.Builtin = Limited, "adopts the environment's default population (one per environment)", false, ImportID, "populations", "default=true"
		d.NameAttrs, d.Match = []string{"name"}, "default=true"
	}),
	child("pingone_population_default_identity_provider", "directory", "pingone_population", "population_id", "populations/{parent}/defaultIdentityProvider", "", true),
	api("pingone_group", "directory", "groups", "groups", "name"),
	child("pingone_group_nesting", "directory", "pingone_group", "group_id", "groups/{parent}/memberOfGroups", "groupMemberships", true, "id"),
	child("pingone_group_role_assignment", "directory", "pingone_group", "group_id", "groups/{parent}/roleAssignments", "roleAssignments", true, "role.id", "scope.type"),
	with(child("pingone_schema_attribute", "directory", "", "", "schemas/{schema}/attributes", "attributes", false, "name"), func(d *Def) {
		d.Status, d.Reason, d.Builtin, d.Import = Limited, "the API returns the user schema's standard attributes too; only CUSTOM attributes are created, standard ones are adopted", "schemaType=CORE|STANDARD", ImportParentID
	}),
	with(api("pingone_password_policy", "directory", "passwordPolicies", "passwordPolicies", "name"), func(d *Def) { d.Builtin = "default=true" }),

	// --- MFA and devices ---
	with(api("pingone_mfa_device_policy", "mfa_devices", "deviceAuthenticationPolicies", "deviceAuthenticationPolicies", "name"), func(d *Def) { d.Match = "default!=true" }),
	with(api("pingone_mfa_device_policy_default", "mfa_devices", "deviceAuthenticationPolicies", "deviceAuthenticationPolicies", "name"), func(d *Def) {
		d.Status, d.Reason, d.Builtin = Limited, "the default device policy exists in every environment and is adopted by import", "default=true"
		d.Match = d.Builtin
	}),
	with(api("pingone_mfa_fido2_policy", "mfa_devices", "fido2Policies", "fido2Policies", "name"), func(d *Def) { d.EnvLocal = []string{"relying_party_id"} }),
	single("pingone_mfa_settings", "mfa_devices", "mfaSettings"),

	// --- Identity providers and gateways ---
	with(api("pingone_identity_provider", "identity_providers", "identityProviders", "identityProviders", "name"), func(d *Def) {
		d.Status, d.Reason = Limited, "client secrets and signing certificates are secrets or per-environment values: secrets map to customer_secrets.yml, certificate IDs become per-environment variables"
		d.Hoist = map[string]string{"google": "type=GOOGLE", "facebook": "type=FACEBOOK", "apple": "type=APPLE", "amazon": "type=AMAZON", "github": "type=GITHUB",
			"linkedin": "type=LINKEDIN", "linkedin_oidc": "type=LINKEDIN_OIDC", "microsoft": "type=MICROSOFT", "paypal": "type=PAYPAL", "twitter": "type=TWITTER",
			"yahoo": "type=YAHOO", "openid_connect": "type=OPENID_CONNECT", "saml": "type=SAML"}
		d.EnvLocal = []string{"openid_connect.issuer", "openid_connect.authorization_endpoint", "openid_connect.token_endpoint", "openid_connect.userinfo_endpoint",
			"openid_connect.jwks_endpoint", "saml.idp_entity_id", "saml.sso_endpoint", "saml.slo_endpoint", "saml.idp_verification.certificates"}
	}),
	child("pingone_identity_provider_attribute", "identity_providers", "pingone_identity_provider", "identity_provider_id", "identityProviders/{parent}/attributes", "attributes", true, "name"),
	with(api("pingone_gateway", "gateways", "gateways", "gateways", "name"), func(d *Def) {
		d.Status, d.Reason = Limited, "bind passwords and RADIUS shared secrets are secrets (customer_secrets.yml); LDAP servers and bind DN are per-environment values"
		d.EnvLocal = []string{"servers", "bind_dn", "radius_clients"}
		d.Secrets = map[string]string{"bind_password": "type=LDAP", "radius_default_shared_secret": "type=RADIUS"}
	}),
	none("pingone_gateway_credential", "gateways", "gateway credentials are generated per environment and installed on that environment's gateway; never copied"),
	child("pingone_gateway_role_assignment", "gateways", "pingone_gateway", "gateway_id", "gateways/{parent}/roleAssignments", "roleAssignments", true, "role.id", "scope.type"),

	// --- Branding, notifications, forms, agreements, languages ---
	single("pingone_branding_settings", "branding", "branding"),
	with(api("pingone_branding_theme", "branding", "themes", "themes", "configuration.name"), func(d *Def) {
		d.Status, d.Reason = Limited, "logo and background images are referenced by image ID and URL; the image itself is a separate pingone_image (not exportable)"
		d.Rename = map[string]string{"name": "configuration.name"}
		d.Fallback = []string{"configuration"}
	}),
	with(child("pingone_branding_theme_default", "branding", "pingone_branding_theme", "branding_theme_id", "", "", true), func(d *Def) {
		d.Status, d.Reason = Limited, "records which theme is the default; exported with the default theme and imported by environment ID"
		d.Match, d.Import = "default=true", ImportEnvironment
	}),
	with(single("pingone_notification_settings", "notifications", "notificationsSettings"), func(d *Def) { d.EnvLocal = []string{"from.email_address", "reply_to.email_address"} }),
	with(single("pingone_notification_settings_email", "notifications", "notificationsSettings/emailDeliverySettings"), func(d *Def) {
		d.Status, d.Reason = Limited, "SMTP host, port and user are per-environment values; the SMTP password and OAuth client secret are secrets (customer_secrets.yml)"
		d.EnvLocal = []string{"host", "port", "username", "from.email_address", "reply_to.email_address"}
		d.Secrets = map[string]string{"password": "protocol=SMTP|SMTPS"}
	}),
	with(api("pingone_notification_template_content", "notifications", "templates/{template}/contents", "contents", "template.id", "locale", "deliveryMethod", "variant"), func(d *Def) {
		d.Import = ImportParentID
		d.Hoist = map[string]string{"email": "deliveryMethod=Email", "sms": "deliveryMethod=SMS", "voice": "deliveryMethod=Voice", "push": "deliveryMethod=Push"}
		d.Rename = map[string]string{"template_name": "template.id"}
	}),
	api("pingone_notification_policy", "notifications", "notificationsPolicies", "notificationsPolicies", "name"),
	with(single("pingone_phone_delivery_settings", "notifications", "notificationsSettings/phoneDeliverySettings"), func(d *Def) {
		d.Status, d.Reason = Limited, "custom provider auth tokens are secrets (customer_secrets.yml)"
	}),
	api("pingone_form", "forms", "forms", "forms", "name"),
	with(single("pingone_forms_recaptcha_v2", "forms", "forms/recaptchaV2"), func(d *Def) {
		d.Status, d.Reason = Limited, "the reCAPTCHA secret key is a secret (customer_secrets.yml); the site key is a per-environment value"
		d.EnvLocal = []string{"site_key"}
	}),
	api("pingone_agreement", "agreements", "agreements", "agreements", "name"),
	with(child("pingone_agreement_enable", "agreements", "pingone_agreement", "agreement_id", "agreements", "agreements", true), func(d *Def) {
		d.Path, d.Embedded, d.Import = "", "", ImportID
		d.Reason = "derived from the agreement's enabled flag"
	}),
	child("pingone_agreement_localization", "agreements", "pingone_agreement", "agreement_id", "agreements/{parent}/languages", "languages", true, "locale"),
	with(child("pingone_agreement_localization_enable", "agreements", "pingone_agreement_localization", "agreement_localization_id", "", "", true), func(d *Def) {
		d.Reason = "derived from the localization's enabled flag"
	}),
	child("pingone_agreement_localization_revision", "agreements", "pingone_agreement_localization", "agreement_localization_id", "agreements/{grandparent}/languages/{parent}/revisions", "revisions", true, "effectiveAt"),
	with(api("pingone_language", "languages", "languages", "languages", "locale"), func(d *Def) {
		d.Status, d.Reason, d.Builtin = Limited, "the predefined languages exist in every environment and are adopted; only custom locales are created", "customerAdded=false"
	}),
	with(child("pingone_language_update", "languages", "pingone_language", "language_id", "", "", true), func(d *Def) {
		d.Reason = "derived from the language's enabled and default flags"
	}),
	with(api("pingone_language_translation", "languages", "languages/{parent}/localizationStrings", "localizationStrings", "locale"), func(d *Def) {
		d.Parent = "pingone_language"
		d.Status, d.Reason = Limited, "translations are exported as the full key/value set of the locale; partial edits are not merged"
	}),
	none("pingone_image", "branding", "image bytes are uploaded, not readable as configuration; manage images by hand and reference their IDs per environment"),

	// --- Protect, Authorize, Verify, Credentials ---
	api("pingone_risk_policy", "protect", "riskPolicySets", "riskPolicySets", "name"),
	with(api("pingone_risk_predictor", "protect", "riskPredictors", "riskPredictors", "compactName"), func(d *Def) {
		d.Status, d.Reason, d.Builtin = Limited, "predefined predictors exist in every environment and are adopted by import", "default=true"
	}),
	api("pingone_authorize_api_service", "authorize", "apiServers", "apiServers", "name"),
	with(child("pingone_authorize_api_service_deployment", "authorize", "pingone_authorize_api_service", "api_service_id", "apiServers/{parent}/deployment", "", true), func(d *Def) {
		d.Status, d.Reason = Limited, "a deployment is an action; it is written so the pipeline deploys the API service after changes"
	}),
	child("pingone_authorize_api_service_operation", "authorize", "pingone_authorize_api_service", "api_service_id", "apiServers/{parent}/operations", "operations", true, "name"),
	api("pingone_authorize_application_role", "authorize", "applicationRoles", "applicationRoles", "name"),
	child("pingone_authorize_application_role_permission", "authorize", "pingone_authorize_application_role", "application_role_id", "applicationRoles/{parent}/permissions", "permissions", true, "id"),
	api("pingone_application_resource", "authorize", "applicationResources", "applicationResources", "name"),
	child("pingone_application_resource_permission", "authorize", "pingone_application_resource", "application_resource_id", "applicationResources/{parent}/permissions", "permissions", true, "action"),
	with(api("pingone_authorize_decision_endpoint", "authorize", "decisionEndpoints", "decisionEndpoints", "name"), func(d *Def) {
		d.Status, d.Reason = Limited, "the policy version (authorization_version_id) is per environment and becomes a per-environment variable"
		d.EnvLocal = []string{"authorization_version_id"}
	}),
	api("pingone_verify_policy", "verify", "verifyPolicies", "verifyPolicies", "name"),
	api("pingone_verify_voice_phrase", "verify", "voicePhrases", "voicePhrases", "displayName"),
	child("pingone_verify_voice_phrase_content", "verify", "pingone_verify_voice_phrase", "voice_phrase_id", "voicePhrases/{parent}/contents", "contents", true, "locale"),
	single("pingone_credential_issuer_profile", "credentials", "credentialIssuerProfile"),
	api("pingone_credential_type", "credentials", "credentialTypes", "credentialTypes", "title"),
	child("pingone_credential_issuance_rule", "credentials", "pingone_credential_type", "credential_type_id", "credentialTypes/{parent}/issuanceRules", "issuanceRules", true, "id"),

	// --- Platform ---
	single("pingone_administrator_security", "platform", "adminConfig"),
	with(api("pingone_alert_channel", "platform", "alertChannels", "alertChannels", "alertName"), func(d *Def) { d.EnvLocal = []string{"addresses"} }),
	with(api("pingone_certificate", "platform", "certificates", "certificates", "subjectDN"), func(d *Def) {
		d.Status, d.Reason = Limited, "certificates are environment-specific: each environment has its own; references to them become per-environment certificate ID variables unless the operator exports the certificate (PEM is a per-environment value)"
		d.EnvLocal = []string{"pem_file"}
	}),
	none("pingone_certificate_signing_response", "platform", "a CA response is issued for one environment's key; it cannot be promoted"),
	with(api("pingone_custom_domain", "platform", "customDomains", "customDomains", "domainName"), func(d *Def) {
		d.Status, d.Reason = Limited, "the domain name is per environment (variable); DNS verification and the TLS certificate are per-environment steps outside Git"
		d.EnvLocal = []string{"domain_name"}
	}),
	none("pingone_custom_domain_ssl", "platform", "the TLS private key is a per-environment secret file; install it per environment outside this tool"),
	none("pingone_custom_domain_verify", "platform", "an action that verifies DNS in one environment; not configuration"),
	with(api("pingone_custom_role", "platform", "roles", "roles", "name"), func(d *Def) { d.Builtin = "type=PLATFORM" }),
	with(api("pingone_identity_propagation_plan", "platform", "propagation/plans", "plans", "name"), func(d *Def) {
		d.Status, d.Reason = Limited, "only the plan is exported; its stores and rules reference per-environment gateways and credentials"
	}),
	with(api("pingone_key", "platform", "keys", "keys", "name", "usageType"), func(d *Def) {
		d.Status, d.Reason = Limited, "key material is never exported: a generated key is recreated in each environment from its settings; imported PKCS#12 keys are per-environment secrets; default signing keys are adopted"
		d.Builtin = "default=true"
	}),
	api("pingone_key_rotation_policy", "platform", "keyRotationPolicies", "keyRotationPolicies", "name"),
	with(single("pingone_rate_limit_configuration", "platform", "rateLimitConfigurations"), func(d *Def) {
		d.Singleton, d.Import, d.Embedded, d.NameAttrs = false, ImportID, "rateLimitConfigurations", []string{"type", "value"}
		d.EnvLocal = []string{"value"}
	}),
	with(api("pingone_trusted_email_domain", "platform", "emailDomains", "emailDomains", "domainName"), func(d *Def) {
		d.Status, d.Reason = Limited, "the domain is per environment and must be verified by DNS in each one"
		d.EnvLocal = []string{"domain_name"}
	}),
	child("pingone_trusted_email_address", "platform", "pingone_trusted_email_domain", "email_domain_id", "emailDomains/{parent}/trustedEmails", "trustedEmails", true, "emailAddress"),
	with(api("pingone_webhook", "platform", "subscriptions", "subscriptions", "name"), func(d *Def) {
		d.Status, d.Reason = Limited, "endpoint URLs are per-environment values; authorization headers are a secret map that is never exported: add them by hand from a Vault-backed variable"
		d.EnvLocal = []string{"http_endpoint_url", "connection_details_url"}
		d.Rename = map[string]string{"http_endpoint_url": "httpEndpoint.url", "http_endpoint_headers": "-", "connection_details_headers": "-"}
	}),
	none("pingone_environment", "platform", "each environment's own definition (licence, region, services) is managed with that environment's state, not promoted from another environment"),

	// --- Identity data: never exported ---
	none("pingone_user", "identity_data", userData),
	none("pingone_user_application_role_assignment", "identity_data", userData),
	none("pingone_user_group_assignment", "identity_data", userData),
	none("pingone_user_role_assignment", "identity_data", userData),
	none("pingone_role_assignment_user", "identity_data", userData),
}

//go:embed provider_schema.json
var schemaJSON []byte

// Attr is one schema attribute (see schemagen).
type Attr struct {
	Type      json.RawMessage `json:"type,omitempty"`
	Nesting   string          `json:"nesting,omitempty"`
	Block     bool            `json:"block,omitempty"`
	Attrs     map[string]Attr `json:"attrs,omitempty"`
	Required  bool            `json:"required,omitempty"`
	Optional  bool            `json:"optional,omitempty"`
	Computed  bool            `json:"computed,omitempty"`
	Sensitive bool            `json:"sensitive,omitempty"`
	WriteOnly bool            `json:"writeOnly,omitempty"`
}

// Configurable reports whether a person sets the attribute (not computed only).
func (a Attr) Configurable() bool { return a.Required || a.Optional }

// Secret reports whether the attribute holds a secret.
func (a Attr) Secret() bool { return a.Sensitive || a.WriteOnly }

type schemaDoc struct {
	Provider  string                     `json:"provider"`
	Version   string                     `json:"version"`
	Resources map[string]map[string]Attr `json:"resources"`
}

var (
	loadOnce sync.Once
	schema   schemaDoc
	byType   map[string]*Def
	loadErr  error
)

func load() {
	loadOnce.Do(func() {
		if err := json.Unmarshal(schemaJSON, &schema); err != nil {
			loadErr = fmt.Errorf("catalog: provider schema: %w", err)
			return
		}
		byType = map[string]*Def{}
		for i := range defs {
			d := &defs[i]
			if byType[d.Type] != nil {
				loadErr = fmt.Errorf("catalog: %s is defined twice", d.Type)
				return
			}
			byType[d.Type] = d
		}
	})
}

func mustLoad() {
	load()
	if loadErr != nil {
		panic(loadErr)
	}
}

// Get returns the definition of a resource type.
func Get(typ string) (*Def, bool) {
	mustLoad()
	d, ok := byType[typ]
	return d, ok
}

// All returns every definition, sorted by family order then type.
func All() []*Def {
	mustLoad()
	order := map[string]int{}
	for i, f := range Families {
		order[f] = i
	}
	out := make([]*Def, 0, len(defs))
	for i := range defs {
		out = append(out, &defs[i])
	}
	sort.Slice(out, func(i, j int) bool {
		if order[out[i].Family] != order[out[j].Family] {
			return order[out[i].Family] < order[out[j].Family]
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// Schema returns the provider schema attributes of a type.
func Schema(typ string) (map[string]Attr, bool) {
	mustLoad()
	a, ok := schema.Resources[typ]
	return a, ok
}

// SchemaTypes lists every resource type of the pinned provider.
func SchemaTypes() []string {
	mustLoad()
	out := make([]string, 0, len(schema.Resources))
	for t := range schema.Resources {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// SchemaVersion is the provider version the embedded schema came from.
func SchemaVersion() string { mustLoad(); return schema.Version }

// Exportable reports whether a type can be selected, included or excluded.
func Exportable(typ string) bool {
	d, ok := Get(typ)
	return ok && d.Exporter != ""
}

// IdentityData reports types that hold users or user data.
func IdentityData(typ string) bool {
	d, ok := Get(typ)
	return ok && d.Family == "identity_data"
}

// Children lists the owned child types of a type (exported with it).
func Children(typ string) []string {
	mustLoad()
	var out []string
	for i := range defs {
		if defs[i].Parent == typ && defs[i].Owned && defs[i].Exporter != "" {
			out = append(out, defs[i].Type)
		}
	}
	sort.Strings(out)
	return out
}

// ImportFormat returns the import_ids format of a type (ImportID when unknown).
func ImportFormat(typ string) string {
	if d, ok := Get(typ); ok && d.Import != "" {
		return d.Import
	}
	return ImportID
}

// Aliases are the short names a request may use for a type.
var Aliases = map[string]string{
	"flow": "pingone_davinci_flow", "flowPolicy": "pingone_davinci_application_flow_policy", "connection": "pingone_davinci_connector_instance",
	"variable": "pingone_davinci_variable", "davinciApplication": "pingone_davinci_application",
	"application": "pingone_application", "population": "pingone_population", "group": "pingone_group",
	"signOnPolicy": "pingone_sign_on_policy", "notificationTemplate": "pingone_notification_template_content",
	"resource": "pingone_resource", "identityProvider": "pingone_identity_provider", "passwordPolicy": "pingone_password_policy",
}

// Resolve maps an alias or a full type name to an exportable type.
func Resolve(kind string) (string, bool) {
	if t, ok := Aliases[kind]; ok {
		kind = t
	}
	if !strings.HasPrefix(kind, "pingone_") {
		return "", false
	}
	return kind, Exportable(kind)
}

// DisplayName is a plain-language name for a type ("notification template content").
func DisplayName(typ string) string {
	return strings.ReplaceAll(strings.TrimPrefix(typ, "pingone_"), "_", " ")
}
