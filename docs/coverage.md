# Coverage matrix

Generated from `internal/catalog` by `PINGONE_PROMOTE_WRITE_COVERAGE=1 go test ./internal/catalog`; do not edit by hand.

Every resource of the pinned `pingidentity/pingone` provider 1.24.0 is listed. **Exporter**: `pingcli-terraformer` (pinned v0.6.0, DaVinci) or `pingone-api` (the native read-only PingOne exporter, ADR 0019). **Environment-specific values** become per-environment variables in `<env>.tfvars`; the environment ID, certificate and key IDs, and IDs of resources that are not exported are always per-environment. **Built-in** objects exist in every environment and are adopted with `import_ids`, never created. **Live** is `no` until the live acceptance case in `docs/acceptance/core-export.md` has passed on a real tenant: API paths and field names are assumptions until then.

Totals: 54 covered, 32 covered with documented limits, 15 not possible (101 resources).


## davinci

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_davinci_application` | covered | pingcli-terraformer | - | - | yes (DaVinci, earlier live runs) | - |
| `pingone_davinci_application_flow_policy` | covered | pingcli-terraformer | - | - | yes (DaVinci, earlier live runs) | - |
| `pingone_davinci_application_key` | not possible | - | - | - | no | the API key is generated in each environment and read by Terraform there, never copied |
| `pingone_davinci_application_secret` | not possible | - | - | - | no | the client secret is generated in each environment and read by Terraform there, never copied |
| `pingone_davinci_connector_instance` | covered | pingcli-terraformer | - | - | yes (DaVinci, earlier live runs) | - |
| `pingone_davinci_flow` | covered | pingcli-terraformer | - | - | yes (DaVinci, earlier live runs) | - |
| `pingone_davinci_flow_deploy` | covered | pingcli-terraformer | exported with its davinci flow | - | yes (DaVinci, earlier live runs) | - |
| `pingone_davinci_flow_enable` | covered | pingcli-terraformer | exported with its davinci flow | - | yes (DaVinci, earlier live runs) | - |
| `pingone_davinci_variable` | covered | pingcli-terraformer | - | - | yes (DaVinci, earlier live runs) | - |

## applications

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_application` | covered | pingone-api | per environment: `oidc_options.redirect_uris`, `oidc_options.post_logout_redirect_uris`, `oidc_options.initiate_login_uri`, `oidc_options.target_link_uri`, `oidc_options.home_page_url`, `oidc_options.jwks_url`, `oidc_options.device_custom_verification_uri`, `saml_options.acs_urls`, `saml_options.sp_entity_id`, `saml_options.default_target_url`, `saml_options.home_page_url`, `saml_options.slo_endpoint`, `saml_options.slo_response_endpoint`, `wsfed_options.domain_name`, `wsfed_options.reply_url`, `wsfed_options.slo_endpoint`, `login_page_url`, `external_link_options.home_page_url` | - | no | - |
| `pingone_application_attribute_mapping` | covered with documented limits | pingone-api | built-in adopted when `mappingType=CORE`; exported with its application | - | no | core mappings that PingOne creates with the application (sub and similar) are adopted by import, not created |
| `pingone_application_flow_policy_assignment` | covered | pingone-api | exported with its application | - | no | - |
| `pingone_application_metadata` | covered with documented limits | pingone-api | exported with its application | - | no | metadata is read as a key/value map; nested values are exported as strings |
| `pingone_application_resource_grant` | covered | pingone-api | exported with its application | - | no | - |
| `pingone_application_role_assignment` | covered | pingone-api | exported with its application | - | no | - |
| `pingone_application_secret` | not possible | - | - | - | no | client secrets are generated per environment and read by Terraform in that environment; copying them is forbidden |
| `pingone_application_sign_on_policy_assignment` | covered | pingone-api | exported with its application | - | no | - |
| `pingone_system_application` | covered with documented limits | pingone-api | built-in adopted when `type=PING_ONE_SELF_SERVICE|PING_ONE_PORTAL` | - | no | system applications (Self-Service, Application Portal) exist in every environment; they are adopted by import and only their settings are managed |

## resources scopes grants

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_resource` | covered | pingone-api | per environment: `audience`; built-in adopted when `type=OPENID_CONNECT|PINGONE_API` | - | no | - |
| `pingone_resource_attribute` | covered with documented limits | pingone-api | built-in adopted when `type=CORE|PREDEFINED`; exported with its resource | - | no | predefined attributes of the built-in resources are adopted by import |
| `pingone_resource_scope` | covered | pingone-api | exported with its resource | - | no | - |
| `pingone_resource_scope_openid` | covered with documented limits | pingone-api | built-in adopted when `builtin=true` | - | no | scopes of the built-in OpenID Connect resource; built-in ones (openid, profile, email, ...) are adopted by import |
| `pingone_resource_scope_pingone_api` | covered with documented limits | pingone-api | built-in adopted when `builtin=true` | - | no | scopes of the built-in PingOne API resource; predefined ones are adopted by import |
| `pingone_resource_secret` | not possible | - | - | - | no | resource secrets are generated per environment and read by Terraform there |

## sign on

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_sign_on_policy` | covered | pingone-api | built-in adopted when `default=true` | - | no | - |
| `pingone_sign_on_policy_action` | covered | pingone-api | exported with its sign on policy | - | no | - |

## directory

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_group` | covered | pingone-api | - | - | no | - |
| `pingone_group_nesting` | covered with documented limits | pingone-api | exported with its group | - | no | read from each group's memberOfGroups list; the API field names are assumptions to confirm live |
| `pingone_group_role_assignment` | covered | pingone-api | exported with its group | - | no | - |
| `pingone_password_policy` | covered | pingone-api | built-in adopted when `default=true` | - | no | - |
| `pingone_population` | covered with documented limits | pingone-api | - | - | no | the default population is managed by pingone_population_default and adopted by import |
| `pingone_population_default` | covered with documented limits | pingone-api | built-in adopted when `default=true` | - | no | adopts the environment's default population (one per environment) |
| `pingone_population_default_identity_provider` | covered | pingone-api | exported with its population | - | no | - |
| `pingone_schema_attribute` | covered with documented limits | pingone-api | built-in adopted when `schemaType=CORE|STANDARD` | - | no | the API returns the user schema's standard attributes too; only CUSTOM attributes are created, standard ones are adopted |

## mfa devices

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_mfa_application_push_credential` | covered with documented limits | pingone-api | exported with its application | - | no | FCM, APNs and HMS keys are secrets: they become customer_secrets.yml entries; the API never returns them |
| `pingone_mfa_device_policy` | covered | pingone-api | - | - | no | - |
| `pingone_mfa_device_policy_default` | covered with documented limits | pingone-api | built-in adopted when `default=true` | - | no | the default device policy exists in every environment and is adopted by import |
| `pingone_mfa_fido2_policy` | covered | pingone-api | per environment: `relying_party_id` | - | no | - |
| `pingone_mfa_settings` | covered | pingone-api | one per environment, imported by environment ID | - | no | - |

## identity providers

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_identity_provider` | covered with documented limits | pingone-api | per environment: `openid_connect.issuer`, `openid_connect.authorization_endpoint`, `openid_connect.token_endpoint`, `openid_connect.userinfo_endpoint`, `openid_connect.jwks_endpoint`, `saml.idp_entity_id`, `saml.sso_endpoint`, `saml.slo_endpoint`, `saml.idp_verification.certificates` | - | no | client secrets and signing certificates are secrets or per-environment values: secrets map to customer_secrets.yml, certificate IDs become per-environment variables |
| `pingone_identity_provider_attribute` | covered | pingone-api | exported with its identity provider | - | no | - |

## gateways

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_gateway` | covered with documented limits | pingone-api | per environment: `servers`, `bind_dn`, `kerberos.service_account_upn`, `radius_network_policy_server` | - | no | bind passwords and RADIUS shared secrets are secrets (customer_secrets.yml); all RADIUS clients of one gateway share one secret variable, so set per-client secrets by hand; LDAP servers, bind DN and the Kerberos account are per-environment values |
| `pingone_gateway_credential` | not possible | - | - | - | no | gateway credentials are generated per environment and installed on that environment's gateway; never copied |
| `pingone_gateway_role_assignment` | covered | pingone-api | exported with its gateway | - | no | - |

## branding

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_branding_settings` | covered | pingone-api | one per environment, imported by environment ID | - | no | - |
| `pingone_branding_theme` | covered with documented limits | pingone-api | - | - | no | logo and background images are referenced by image ID and URL; the image itself is a separate pingone_image (not exportable) |
| `pingone_branding_theme_default` | covered with documented limits | pingone-api | exported with its branding theme | - | no | records which theme is the default; exported with the default theme and imported by environment ID |
| `pingone_image` | not possible | - | - | - | no | image bytes are uploaded, not readable as configuration; manage images by hand and reference their IDs per environment |

## notifications

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_notification_policy` | covered | pingone-api | - | - | no | - |
| `pingone_notification_settings` | covered | pingone-api | per environment: `from.email_address`, `reply_to.email_address`; one per environment, imported by environment ID | - | no | - |
| `pingone_notification_settings_email` | covered with documented limits | pingone-api | per environment: `host`, `port`, `username`, `from.email_address`, `reply_to.email_address`; one per environment, imported by environment ID | - | no | SMTP host, port and user are per-environment values; the SMTP password and OAuth client secret are secrets (customer_secrets.yml) |
| `pingone_notification_template_content` | covered | pingone-api | per environment: `email.from.address`, `email.reply_to.address` | - | no | - |
| `pingone_phone_delivery_settings` | covered with documented limits | pingone-api | one per environment, imported by environment ID | - | no | custom provider auth tokens are secrets (customer_secrets.yml); request headers of custom providers are never exported (they can carry credentials) and are set by hand |

## forms

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_form` | covered | pingone-api | - | - | no | - |
| `pingone_forms_recaptcha_v2` | covered with documented limits | pingone-api | per environment: `site_key`; one per environment, imported by environment ID | - | no | the reCAPTCHA secret key is a secret (customer_secrets.yml); the site key is a per-environment value |

## agreements

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_agreement` | covered | pingone-api | - | - | no | - |
| `pingone_agreement_enable` | covered | pingone-api | exported with its agreement | - | no | derived from the agreement's enabled flag |
| `pingone_agreement_localization` | covered | pingone-api | exported with its agreement | - | no | - |
| `pingone_agreement_localization_enable` | covered | pingone-api | exported with its agreement localization | - | no | derived from the localization's enabled flag |
| `pingone_agreement_localization_revision` | covered | pingone-api | exported with its agreement localization | - | no | - |

## languages

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_language` | covered with documented limits | pingone-api | built-in adopted when `customerAdded=false` | - | no | the predefined languages exist in every environment and are adopted; only custom locales are created |
| `pingone_language_translation` | covered with documented limits | pingone-api | - | - | no | translations are exported as the full key/value set of the locale; partial edits are not merged |
| `pingone_language_update` | covered | pingone-api | exported with its language | - | no | derived from the language's enabled and default flags |

## protect

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_risk_policy` | covered | pingone-api | - | - | no | - |
| `pingone_risk_predictor` | covered with documented limits | pingone-api | built-in adopted when `default=true` | - | no | predefined predictors exist in every environment and are adopted by import |

## authorize

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_application_resource` | covered | pingone-api | - | - | no | - |
| `pingone_application_resource_permission` | covered | pingone-api | exported with its application resource | - | no | - |
| `pingone_authorize_api_service` | covered | pingone-api | per environment: `base_urls` | - | no | - |
| `pingone_authorize_api_service_deployment` | covered with documented limits | pingone-api | exported with its authorize api service | - | no | a deployment is an action; it is written so the pipeline deploys the API service after changes |
| `pingone_authorize_api_service_operation` | covered | pingone-api | exported with its authorize api service | - | no | - |
| `pingone_authorize_application_role` | covered | pingone-api | - | - | no | - |
| `pingone_authorize_application_role_permission` | covered with documented limits | pingone-api | exported with its authorize application role | - | no | labels end with the permission ID (the API object has no name), so they differ between source environments |
| `pingone_authorize_decision_endpoint` | covered with documented limits | pingone-api | per environment: `authorization_version_id` | - | no | the policy version (authorization_version_id) is per environment and becomes a per-environment variable |

## verify

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_verify_policy` | covered | pingone-api | - | - | no | - |
| `pingone_verify_voice_phrase` | covered | pingone-api | - | - | no | - |
| `pingone_verify_voice_phrase_content` | covered | pingone-api | exported with its verify voice phrase | - | no | - |

## credentials

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_credential_issuance_rule` | covered with documented limits | pingone-api | exported with its credential type | - | no | labels end with the rule ID (the API object has no name) |
| `pingone_credential_issuer_profile` | covered | pingone-api | one per environment, imported by environment ID | - | no | - |
| `pingone_credential_type` | covered | pingone-api | - | - | no | - |
| `pingone_digital_wallet_application` | covered | pingone-api | - | - | no | - |

## platform

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_administrator_security` | covered | pingone-api | one per environment, imported by environment ID | - | no | - |
| `pingone_alert_channel` | covered | pingone-api | per environment: `addresses` | - | no | - |
| `pingone_certificate` | covered with documented limits | pingone-api | per environment: `pem_file` | - | no | certificates are environment-specific: each environment has its own; references to them become per-environment certificate ID variables unless the operator exports the certificate (PEM is a per-environment value) |
| `pingone_certificate_signing_response` | not possible | - | - | - | no | a CA response is issued for one environment's key; it cannot be promoted |
| `pingone_custom_domain` | covered with documented limits | pingone-api | per environment: `domain_name` | - | no | the domain name is per environment (variable); DNS verification and the TLS certificate are per-environment steps outside Git |
| `pingone_custom_domain_ssl` | not possible | - | - | - | no | the TLS private key is a per-environment secret file; install it per environment outside this tool |
| `pingone_custom_domain_verify` | not possible | - | - | - | no | an action that verifies DNS in one environment; not configuration |
| `pingone_custom_role` | covered | pingone-api | - | - | no | - |
| `pingone_environment` | not possible | - | - | - | no | each environment's own definition (licence, region, services) is managed with that environment's state, not promoted from another environment |
| `pingone_identity_propagation_plan` | covered with documented limits | pingone-api | - | - | no | only the plan is exported; its stores and rules reference per-environment gateways and credentials |
| `pingone_key` | covered with documented limits | pingone-api | built-in adopted when `default=true` | - | no | key material is never exported: a generated key is recreated in each environment from its settings; imported PKCS#12 keys are per-environment secrets; default signing keys are adopted |
| `pingone_key_rotation_policy` | covered | pingone-api | - | - | no | - |
| `pingone_rate_limit_configuration` | covered | pingone-api | per environment: `value` | - | no | - |
| `pingone_trusted_email_address` | covered | pingone-api | exported with its trusted email domain | - | no | - |
| `pingone_trusted_email_domain` | covered with documented limits | pingone-api | per environment: `domain_name` | - | no | the domain is per environment and must be verified by DNS in each one |
| `pingone_webhook` | covered with documented limits | pingone-api | per environment: `http_endpoint_url`, `connection_details_url` | - | no | endpoint URLs are per-environment values; authorization headers are a secret map that is never exported: add them by hand from a Vault-backed variable |

## identity data

| Resource type | Status | Exporter | Environment-specific values / built-in | Tested | Live | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| `pingone_role_assignment_user` | not possible | - | - | - | no | holds users, user data or user assignments; pingone-promote never exports identity data |
| `pingone_user` | not possible | - | - | - | no | holds users, user data or user assignments; pingone-promote never exports identity data |
| `pingone_user_application_role_assignment` | not possible | - | - | - | no | holds users, user data or user assignments; pingone-promote never exports identity data |
| `pingone_user_group_assignment` | not possible | - | - | - | no | holds users, user data or user assignments; pingone-promote never exports identity data |
| `pingone_user_role_assignment` | not possible | - | - | - | no | holds users, user data or user assignments; pingone-promote never exports identity data |
