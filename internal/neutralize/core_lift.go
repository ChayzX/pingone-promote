package neutralize

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/secretscan"
)

// These paths describe deployment bindings, rather than arbitrary strings
// that happen to look like URLs. In particular, protocol identifiers, names,
// descriptions, claims, scopes and policy behavior stay shared. Resource IDs
// are resolved by the export graph before this pass.
var coreEnvironmentPaths = map[string]string{
	"pingone_application": `login_page_url external_link_options.home_page_url
		oidc_options.redirect_uris oidc_options.post_logout_redirect_uris oidc_options.home_page_url
		oidc_options.initiate_login_uri oidc_options.jwks_url oidc_options.target_link_uri
		oidc_options.device_custom_verification_uri oidc_options.cors_settings.origins
		saml_options.acs_urls saml_options.default_target_url saml_options.home_page_url
		saml_options.slo_endpoint saml_options.slo_response_endpoint saml_options.sp_entity_id
		saml_options.cors_settings.origins saml_options.virtual_server_id_settings.virtual_server_ids.vs_id
		wsfed_options.domain_name wsfed_options.reply_url wsfed_options.slo_endpoint wsfed_options.cors_settings.origins`,
	"pingone_authorize_api_service":      "base_urls",
	"pingone_certificate":                "pem_file pkcs7_file_base64",
	"pingone_custom_domain":              "domain_name",
	"pingone_custom_domain_ssl":          "certificate_pem_file intermediate_certificates_pem_file",
	"pingone_digital_wallet_application": "app_open_url",
	"pingone_environment":                "name description type region solution license_id services.bookmarks.url services.console_url services.tags",
	"pingone_gateway": `bind_dn servers kerberos.service_account_upn radius_clients.ip
		radius_network_policy_server.ip radius_network_policy_server.port user_types.search_base_dn`,
	"pingone_identity_provider": `amazon.client_id apple.client_id apple.key_id apple.team_id facebook.app_id
		github.client_id google.client_id linkedin.client_id linkedin_oidc.client_id microsoft.client_id microsoft.tenant_id
		openid_connect.authorization_endpoint openid_connect.client_id openid_connect.discovery_endpoint
		openid_connect.issuer openid_connect.jwks_endpoint openid_connect.token_endpoint openid_connect.userinfo_endpoint
		paypal.client_environment paypal.client_id twitter.client_id yahoo.client_id
		saml.idp_entity_id saml.sp_entity_id saml.sso_endpoint saml.slo_endpoint saml.slo_response_endpoint`,
	"pingone_key":                             "custom_crl issuer_dn subject_dn",
	"pingone_mfa_fido2_policy":                "relying_party_id",
	"pingone_mfa_application_push_credential": "apns.team_id",
	"pingone_notification_template_content":   "email.from.address email.reply_to.address",
	"pingone_notification_settings_email": `auth_url client_id host port username from.email_address
		reply_to.email_address requests.url`,
	"pingone_phone_delivery_settings": `provider_custom.authentication.auth_url provider_custom.authentication.client_id
		provider_custom.authentication.username provider_custom.requests.url provider_custom.numbers.number
		provider_custom_syniverse.selected_numbers.number provider_custom_twilio.selected_numbers.number provider_custom_twilio.sid`,
	"pingone_risk_predictor": `predictor_adversary_in_the_middle.allowed_domain_list predictor_anonymous_network.allowed_cidr_list
		predictor_geovelocity.allowed_cidr_list predictor_ip_reputation.allowed_cidr_list`,
	"pingone_trusted_email_domain": "domain_name",
	"pingone_webhook":              "http_endpoint_url connection_details_url",
}

// Schema-sensitive writable inputs from the pinned provider. The state renderer
// normally supplies variable references already; this also protects direct HCL
// inputs. No literal from these paths enters SourceValues, including masks.
var coreSecretPaths = map[string]string{
	"pingone_application": `oidc_options.mobile_app.integrity_detection.google_play.decryption_key
		oidc_options.mobile_app.integrity_detection.google_play.service_account_credentials_json
		oidc_options.mobile_app.integrity_detection.google_play.verification_key`,
	"pingone_custom_domain_ssl":  "private_key_pem_file",
	"pingone_forms_recaptcha_v2": "secret_key",
	"pingone_gateway":            "bind_password kerberos.service_account_password radius_clients.shared_secret radius_default_shared_secret",
	"pingone_gateway_credential": "credential",
	"pingone_identity_provider": `amazon.client_secret apple.client_secret_signing_key facebook.app_secret github.client_secret
		google.client_secret linkedin.client_secret linkedin_oidc.client_secret microsoft.client_secret openid_connect.client_secret
		paypal.client_secret twitter.client_secret yahoo.client_secret`,
	"pingone_key": "pkcs12_file_base64 pkcs12_file_password",
	"pingone_mfa_application_push_credential": "apns.key apns.token_signing_key fcm.google_service_account_credentials hms.client_id hms.client_secret",
	"pingone_notification_settings_email":     "assertion auth_token client_secret header_value password",
	"pingone_phone_delivery_settings": `provider_custom.authentication.assertion provider_custom.authentication.auth_token
		provider_custom.authentication.client_secret provider_custom.authentication.header_value provider_custom.authentication.password
		provider_custom_syniverse.auth_token provider_custom_twilio.auth_token`,
	"pingone_user":    "password.initial_value",
	"pingone_webhook": "connection_details_headers http_endpoint_headers",
}

func corePathKind(resourceType, path string) Kind {
	for _, candidate := range strings.Fields(coreSecretPaths[resourceType]) {
		if candidate == path {
			return KindSecret
		}
	}
	for _, candidate := range strings.Fields(coreEnvironmentPaths[resourceType]) {
		if candidate == path {
			return KindEnvLocal
		}
	}
	return ""
}

func (s *state) liftCoreAttributes() error {
	for _, rb := range s.raw {
		if !strings.HasPrefix(rb.typ, "pingone_") || strings.HasPrefix(rb.typ, "pingone_davinci_") {
			continue
		}
		if err := s.liftCoreBody(rb, rb.body, "", ""); err != nil {
			return err
		}
	}
	return nil
}

func coreChildPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func (s *state) liftCoreBody(rb *rawBlock, body *hclsyntax.Body, schemaPath, valuePath string) error {
	attrs := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		attrs = append(attrs, name)
	}
	sort.Strings(attrs)
	for _, name := range attrs {
		if err := s.liftCoreExpr(rb, body.Attributes[name].Expr, coreChildPath(schemaPath, name), coreChildPath(valuePath, name)); err != nil {
			return err
		}
	}
	counts, seen := map[string]int{}, map[string]int{}
	for _, block := range body.Blocks {
		counts[block.Type]++
	}
	for _, block := range body.Blocks {
		child := block.Type
		if counts[block.Type] > 1 {
			child += "." + strconv.Itoa(seen[block.Type])
		}
		seen[block.Type]++
		if err := s.liftCoreBody(rb, block.Body, coreChildPath(schemaPath, block.Type), coreChildPath(valuePath, child)); err != nil {
			return err
		}
	}
	return nil
}

func (s *state) liftCoreExpr(rb *rawBlock, expr hclsyntax.Expression, schemaPath, valuePath string) error {
	rng := expr.Range()
	kind := corePathKind(rb.typ, schemaPath)
	if kind == KindSecret {
		if err := s.protectCoreSecretRefs(expr); err != nil {
			return err
		}
	}
	covered := false
	for _, previous := range s.lifted[rb] {
		if rng.Start.Byte >= previous.rng.Start.Byte && rng.End.Byte <= previous.rng.End.Byte {
			covered = true
			// An explicit LiftRule cannot downgrade a credential, including
			// one inside an object lifted by that rule.
			if kind == KindSecret {
				variable := s.res.Variables[previous.name]
				variable.Kind = KindSecret
				if value, ok := s.res.SourceValues[previous.name]; ok {
					variable.Type = coreValueType(value)
				}
				delete(s.res.SourceValues, previous.name)
			} else if kind == KindEnvLocal {
				if value, diags := expr.Value(nil); !diags.HasErrors() && coreContainsSecret(value) {
					return fmt.Errorf("neutralize: %s.%s contains a suspected secret; supply a sensitive variable", rb.addr(), valuePath)
				}
			}
			break
		}
	}
	if covered && kind != "" {
		return nil
	}
	if !covered && kind != "" && len(expr.Variables()) == 0 {
		value, diags := expr.Value(nil)
		if !diags.HasErrors() && value.IsWhollyKnown() && !value.IsNull() {
			if kind != KindSecret && coreContainsSecret(value) {
				return fmt.Errorf("neutralize: %s.%s contains a suspected secret; supply a sensitive variable", rb.addr(), valuePath)
			}
			label := rb.label
			if renamed, ok := s.relabel[rb]; ok {
				label = renamed
			}
			name := LiftedVariableName(rb.typ, label, valuePath)
			origin := "lift:" + rb.addr() + "." + valuePath
			if kind == KindSecret {
				origin = name
				if mapped := s.opts.SecretNames[name]; mapped != "" {
					name = mapped
				}
				if !secretNameRE.MatchString(name) || name == EnvironmentIDVariable || name == "import_ids" {
					return fmt.Errorf("neutralize: invalid core credential mapping")
				}
			}
			if _, exists := s.res.Variables[name]; exists {
				return &CollisionError{What: "variable", Name: name, Addresses: []string{rb.addr()}, Hint: "a core lifted attribute's variable name is already taken"}
			}
			s.res.Variables[name] = &Variable{Name: name, Type: coreValueType(value), Kind: kind,
				Origin: origin, Description: "Environment value for " + valuePath + " of " + rb.typ + "." + label}
			if kind != KindSecret {
				s.res.SourceValues[name] = value
			}
			s.lifted[rb] = append(s.lifted[rb], lift{rng: rng, name: name})
			return nil
		}
	}
	// A mixed list/object may contain existing references alongside literals.
	// Descend into just its literal children rather than replacing references.
	switch e := expr.(type) {
	case *hclsyntax.ObjectConsExpr:
		for _, item := range e.Items {
			if key, ok := objectKey(item.KeyExpr); ok {
				childSchemaPath := coreChildPath(schemaPath, key)
				if kind != "" {
					childSchemaPath = schemaPath
				}
				if err := s.liftCoreExpr(rb, item.ValueExpr, childSchemaPath, coreChildPath(valuePath, key)); err != nil {
					return err
				}
			}
		}
	case *hclsyntax.TupleConsExpr:
		for i, item := range e.Exprs {
			if err := s.liftCoreExpr(rb, item, schemaPath, coreChildPath(valuePath, strconv.Itoa(i))); err != nil {
				return err
			}
		}
	case *hclsyntax.ParenthesesExpr:
		return s.liftCoreExpr(rb, e.Expression, schemaPath, valuePath)
	case *hclsyntax.ScopeTraversalExpr:
		// Referenced inputs were made sensitive above; preserve their expression.
	default:
		if kind == KindSecret {
			return fmt.Errorf("neutralize: %s.%s uses an unsupported credential expression; supply a sensitive variable", rb.addr(), valuePath)
		}
	}
	return nil
}

func coreContainsSecret(value cty.Value) bool {
	if value.IsNull() || !value.IsWhollyKnown() {
		return false
	}
	if value.Type() == cty.String {
		literal := value.AsString()
		if len(secretscan.ScanText("", []byte(literal))) > 0 {
			return true
		}
		if parsed, err := url.Parse(literal); err == nil {
			if parsed.User != nil {
				if _, password := parsed.User.Password(); password {
					return true
				}
			}
			for key, values := range parsed.Query() {
				for _, value := range values {
					if secretscan.SuspectLiteral(key, value) {
						return true
					}
				}
			}
		}
		return false
	}
	if value.CanIterateElements() {
		for it := value.ElementIterator(); it.Next(); {
			key, child := it.Element()
			if key.Type() == cty.String && child.Type() == cty.String && !child.IsNull() && secretscan.SuspectLiteral(key.AsString(), child.AsString()) {
				return true
			}
			if coreContainsSecret(child) {
				return true
			}
		}
	}
	return false
}

// Retain a concrete type even for map/object credentials. Terraform accepts a
// list expression for a homogeneous list type; no source defaults are emitted.
func coreValueType(value cty.Value) string {
	if !value.IsNull() {
		if primitive := typeOf(value); primitive != "any" {
			return primitive
		}
	}
	typ := value.Type()
	if typ == cty.String || typ == cty.Number || typ == cty.Bool {
		return typ.FriendlyName()
	}
	if typ.IsObjectType() {
		var attrs []string
		for name, childType := range typ.AttributeTypes() {
			if !hclsyntax.ValidIdentifier(name) {
				// HCL object type constraints require bare attribute names. Preserve
				// arbitrary homogeneous map keys without inventing renamed attributes.
				common := cty.NilType
				for _, child := range typ.AttributeTypes() {
					if common == cty.NilType {
						common = child
					} else if !common.Equals(child) {
						return "any"
					}
				}
				if common != cty.NilType {
					return "map(" + coreValueType(cty.NullVal(common)) + ")"
				}
				return "any"
			}
			attrs = append(attrs, name+" = "+coreValueType(cty.NullVal(childType)))
		}
		sort.Strings(attrs)
		return "object({" + strings.Join(attrs, ", ") + "})"
	}
	if typ.IsMapType() {
		return "map(" + coreValueType(cty.NullVal(typ.ElementType())) + ")"
	}
	if typ.IsListType() {
		return "list(" + coreValueType(cty.NullVal(typ.ElementType())) + ")"
	}
	if typ.IsSetType() {
		return "set(" + coreValueType(cty.NullVal(typ.ElementType())) + ")"
	}
	if typ.IsTupleType() {
		var elements []string
		for _, child := range typ.TupleElementTypes() {
			elements = append(elements, coreValueType(cty.NullVal(child)))
		}
		return "tuple([" + strings.Join(elements, ", ") + "])"
	}
	return "any"
}

// Treat the schema's sensitivity as authoritative even if a hand-made export
// forgot sensitive=true on a variable already used in a credential field.
func (s *state) protectCoreSecretRefs(expr hclsyntax.Expression) error {
	for _, tr := range expr.Variables() {
		if len(tr) < 2 || tr.RootName() != "var" {
			continue
		}
		attr, ok := tr[1].(hcl.TraverseAttr)
		if !ok {
			continue
		}
		origin := attr.Name
		name := origin
		if n := s.rename[origin]; n != "" {
			name = n
		}
		if origin == EnvironmentIDVariable || name == EnvironmentIDVariable || origin == "import_ids" {
			return fmt.Errorf("neutralize: reserved environment input used as a credential")
		}
		variable := s.res.Variables[name]
		if variable == nil {
			return fmt.Errorf("neutralize: credential variable is unavailable")
		}
		delete(s.res.SourceValues, origin)
		delete(s.res.SourceValues, name)
		if variable.Kind == KindSecret {
			continue
		}
		mapped := name
		if n := s.opts.SecretNames[origin]; n != "" {
			mapped = n
		}
		if !secretNameRE.MatchString(mapped) || mapped == EnvironmentIDVariable || mapped == "import_ids" {
			return fmt.Errorf("neutralize: invalid credential variable mapping")
		}
		if mapped != name {
			if _, exists := s.res.Variables[mapped]; exists {
				return &CollisionError{What: "variable", Name: mapped, Hint: "credential mapping collides with another input"}
			}
			delete(s.res.Variables, name)
			variable.Name = mapped
			s.res.Variables[mapped] = variable
			s.rename[origin] = mapped
		}
		variable.Kind = KindSecret
	}
	return nil
}
