# Admin sign-in: OFF for the proof of concept (var.enable_signin = false).
# To turn it on: set enable_signin = true and entra_identity_provider_id /
# admin_environment_id, regenerate the flow with
#   python3 ../gen_flows.py --terraform . --signin
# then apply and configure the OIDC connection (RUNBOOK: "Turning sign-in back on").

# ---------------- Admin sign-in (admin environment) ----------------------------
# The flow's OIDC connection signs admins in through this app. Its sign-on
# policy sends them straight to the existing Entra external IdP, as the admin
# console does.

resource "pingone_application" "signin" {
  count          = var.enable_signin ? 1 : 0
  environment_id = var.admin_environment_id
  lifecycle {
    precondition {
      condition     = var.admin_environment_id != "" && var.entra_identity_provider_id != ""
      error_message = "Sign-in needs admin_environment_id and entra_identity_provider_id."
    }
  }
  name        = "${var.name_prefix} - DaVinci sign-in"
  description = "Admin sign-in for the PingOne Promote DaVinci Request flow."
  enabled     = true
  oidc_options = {
    type                       = "WEB_APP"
    grant_types                = ["AUTHORIZATION_CODE"]
    response_types             = ["CODE"]
    token_endpoint_auth_method = "CLIENT_SECRET_BASIC"
    redirect_uris              = [local.oidc_redirect_uri]
    pkce_enforcement           = "OPTIONAL"
  }
}

resource "pingone_application_secret" "signin" {
  count          = var.enable_signin ? 1 : 0
  environment_id = var.admin_environment_id
  application_id = pingone_application.signin[0].id
}

resource "pingone_sign_on_policy" "signin" {
  count          = var.enable_signin ? 1 : 0
  environment_id = var.admin_environment_id
  name           = replace(lower("${var.name_prefix}-davinci-signin"), " ", "-")
  description    = "Send PingOne Promote flow sign-ins to the Entra external IdP."
}

resource "pingone_sign_on_policy_action" "entra" {
  count             = var.enable_signin ? 1 : 0
  environment_id    = var.admin_environment_id
  sign_on_policy_id = pingone_sign_on_policy.signin[0].id
  priority          = 1
  identity_provider {
    identity_provider_id = var.entra_identity_provider_id
  }
}

resource "pingone_application_sign_on_policy_assignment" "signin" {
  count             = var.enable_signin ? 1 : 0
  environment_id    = var.admin_environment_id
  application_id    = pingone_application.signin[0].id
  sign_on_policy_id = pingone_sign_on_policy.signin[0].id
  priority          = 1
}

data "pingone_role" "identity_read" {
  count = var.enable_signin ? 1 : 0
  name  = "Identity Data Read Only"
}

resource "pingone_application_role_assignment" "worker_admin_env" {
  count                = var.enable_signin ? 1 : 0
  environment_id       = var.host_environment_id
  application_id       = pingone_application.worker.id
  role_id              = data.pingone_role.identity_read[0].id
  scope_environment_id = var.admin_environment_id
}

# Created empty: fill in its OIDC fields once in the console (RUNBOOK: "Turning sign-in back on").
resource "pingone_davinci_connector_instance" "oidc" {
  count          = var.enable_signin && var.oidc_connection_id == "" ? 1 : 0
  environment_id = var.host_environment_id
  name           = "${var.name_prefix} admin SSO"
  connector = {
    id = "genericConnector"
  }
  lifecycle {
    ignore_changes = [properties]
  }
}

