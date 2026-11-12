package catalog

// Corrections from checking every API-exported type against the PingOne Go
// SDK (github.com/patrickcping/pingone-go-sdk-v2, request paths, JSON tags,
// _embedded keys) and the provider's ImportState functions, 2026-11-11.
// They are kept together so the evidence for each is easy to audit; the
// live acceptance run (docs/acceptance/core-export.md) is still the proof.

type fix func(*Def)

func renames(extra map[string]string) fix {
	return func(d *Def) {
		if d.Rename == nil {
			d.Rename = map[string]string{}
		}
		for k, v := range extra {
			d.Rename[k] = v
		}
	}
}

var sdkFixes = map[string][]fix{
	"pingone_application": {renames(map[string]string{
		"saml_options.assertion_signed_enabled": "assertionSigned", "saml_options.response_is_signed": "responseSigned",
		"oidc_options.mobile_app": "mobile"}),
		func(d *Def) { d.Match = "type!=PING_ONE_SELF_SERVICE|PING_ONE_PORTAL|PING_ONE_ADMIN_CONSOLE" }},
	"pingone_application_attribute_mapping": {renames(map[string]string{
		"oidc_id_token_enabled": "idToken", "oidc_userinfo_enabled": "userInfo", "saml_subject_nameformat": "nameFormat"})},
	// GET applications/{id}/metadata is one object per application.
	"pingone_application_metadata": {func(d *Def) {
		d.Embedded, d.NameAttrs, d.Import = "", nil, ImportParent
	}},
	"pingone_digital_wallet_application": {func(d *Def) { d.Import = ImportID }},
	"pingone_resource_attribute":         {renames(map[string]string{"id_token_enabled": "idToken", "userinfo_enabled": "userInfo"})},
	// The API has no flag for predefined scopes; the OpenID Connect
	// standard scopes are known by name.
	"pingone_resource_scope_openid": {func(d *Def) { d.Builtin = "name=openid|profile|email|address|phone|offline_access" }},
	"pingone_resource_scope_pingone_api": {func(d *Def) {
		d.Builtin = ""
		d.Reason = "custom scopes of the PingOne API resource are exported; the API does not mark its predefined scopes, so do not select those"
	}},
	"pingone_sign_on_policy_action": {renames(map[string]string{"registration_external_href": "registration.external.href"})},
	// Both default objects are imported by the environment ID alone.
	"pingone_population_default":                   {func(d *Def) { d.Import = ImportEnvironment }},
	"pingone_mfa_device_policy_default":            {func(d *Def) { d.Import = ImportEnvironment }},
	"pingone_population_default_identity_provider": {func(d *Def) { d.Import = ImportParent }},
	"pingone_schema_attribute":                     {renames(map[string]string{"multivalued": "multiValued"})},
	"pingone_identity_provider": {renames(map[string]string{
		"openid_connect.userinfo_endpoint": "userInfoEndpoint", "saml.authentication_request_signed": "authnRequestSigned",
		"registration_population_id": "registration.population.id"})},
	"pingone_gateway": {renames(map[string]string{
		"servers": "serversHostAndPort", "bind_dn": "bindDN", "kerberos.service_account_upn": "serviceAccountUserPrincipalName",
		"radius_default_shared_secret": "defaultSharedSecret", "radius_network_policy_server": "networkPolicyServer"})},
	"pingone_branding_settings": {func(d *Def) { d.Path = "brandingSettings" }},
	"pingone_notification_settings": {renames(map[string]string{
		"from.email_address": "address", "reply_to.email_address": "address"})},
	"pingone_notification_settings_email": {renames(map[string]string{
		"from.email_address": "address", "reply_to.email_address": "address"})},
	// A content object has no template field: the template name is in the
	// URL (the parent), and becomes template_name literally.
	"pingone_notification_template_content": {func(d *Def) {
		d.NameAttrs = []string{"locale", "deliveryMethod", "variant"}
		delete(d.Rename, "template_name")
		d.ParentValue = "template_name"
	}, renames(map[string]string{"email.character_set": "charset", "email.content_type": "emailContentType",
		"push.category": "pushCategory", "voice.type": "voice"})},
	// A collection of providers, imported by their own ID.
	"pingone_phone_delivery_settings": {func(d *Def) {
		d.Singleton, d.Embedded, d.Import, d.NameAttrs = false, "phoneDeliverySettings", ImportID, []string{"provider", "name"}
	}},
	"pingone_forms_recaptcha_v2": {func(d *Def) { d.Path = "recaptchaV2Config" }},
	// GET translations/{locale} returns one item per string, while the
	// provider manages all of a locale's strings as one resource.
	"pingone_language_translation": {func(d *Def) {
		*d = none(d.Type, d.Family, "not yet: the API returns one item per translated string and the provider manages a locale's strings as one resource; this aggregation is not implemented")
	}},
	// default is an object (weight, score, ...), not a flag; predefined
	// predictors cannot be deleted (unverified meaning of deletable).
	"pingone_risk_predictor":                   {func(d *Def) { d.Builtin = "deletable=false" }},
	"pingone_authorize_api_service_deployment": {func(d *Def) { d.Import = ImportParent }},
	"pingone_authorize_application_role":       {func(d *Def) { d.Embedded = "roles" }},
	"pingone_authorize_application_role_permission": {func(d *Def) {
		d.NameAttrs, d.Status, d.Reason = []string{"key"}, Covered, ""
	}},
	// Application resources live under a custom resource and take its name.
	"pingone_application_resource": {func(d *Def) {
		d.Family, d.Parent, d.Path, d.Embedded, d.Import = "resources_scopes_grants", "pingone_resource", "resources/{parent}/applicationResources", "resources", ImportParentID
		d.ParentMatch, d.ParentName, d.Owned = "type=CUSTOM", "resource_name", true
	}},
	// The issuer profile has its own ID; it exists once per environment and
	// is adopted.
	"pingone_credential_issuer_profile": {func(d *Def) { d.Import, d.Builtin = ImportID, "id!=" }},
	"pingone_credential_type":           {func(d *Def) { d.Embedded = "items" }},
	"pingone_rate_limit_configuration":  {func(d *Def) { d.Path, d.Embedded = "rateLimitIpConfigs", "rateLimitIpConfigs" }},
	"pingone_webhook": {renames(map[string]string{
		"connection_details_url": "connectionDetails.url", "filter_options.useragent_exposed": "userAgentExposed",
		"filter_options.ip_address_exposed": "ipAddressExposed", "filter_options.included_application_ids": "includedApplications",
		"filter_options.included_population_ids": "includedPopulations", "tls_client_auth_key_pair_id": "tlsClientAuthKeyPair.id"})},
}

func applySDKFixes() {
	for i := range defs {
		for _, f := range sdkFixes[defs[i].Type] {
			f(&defs[i])
		}
	}
}
