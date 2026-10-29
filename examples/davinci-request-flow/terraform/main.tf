locals {
  api_origin  = "https://api.${var.pingone_domain}"
  auth_origin = "https://auth.${var.pingone_domain}"
  region = lookup({
    "pingone.com"    = "us", "pingone.eu" = "eu", "pingone.ca" = "ca",
    "pingone.com.au" = "au", "pingone.sg" = "sg", "pingone.asia" = "asia"
  }, var.pingone_domain)
  oidc_redirect_uri = var.oidc_redirect_uri != "" ? var.oidc_redirect_uri : "${local.auth_origin}/${var.host_environment_id}/rp/callback/genericConnector"

  # Connections the generated flow (flow.tf.json) points at.
  connection_ids = {
    oidc = var.oidc_connection_id != "" ? var.oidc_connection_id : pingone_davinci_connector_instance.oidc[0].id
    http = var.connection_ids.http != "" ? var.connection_ids.http : pingone_davinci_connector_instance.created["http"].id
    fn   = var.connection_ids.functions != "" ? var.connection_ids.functions : pingone_davinci_connector_instance.created["functions"].id
    err  = var.connection_ids.error != "" ? var.connection_ids.error : pingone_davinci_connector_instance.created["error"].id
    var  = var.connection_ids.variables != "" ? var.connection_ids.variables : pingone_davinci_connector_instance.created["variables"].id
  }
  connectors_to_create = {
    for k, c in { http = "httpConnector", functions = "functionsConnector", error = "errorConnector", variables = "variablesConnector" } :
    k => c if lookup(var.connection_ids, k, "") == ""
  }
}

# ---------------- Admin sign-in (admin environment) ----------------------------
# The flow's OIDC connection signs admins in through this app. Its sign-on
# policy sends them straight to the existing Entra external IdP, as the admin
# console does.

resource "pingone_application" "signin" {
  environment_id = var.admin_environment_id
  name           = "${var.name_prefix} - DaVinci sign-in"
  description    = "Admin sign-in for the PingOne Promote DaVinci Request flow."
  enabled        = true
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
  environment_id = var.admin_environment_id
  application_id = pingone_application.signin.id
}

resource "pingone_sign_on_policy" "signin" {
  environment_id = var.admin_environment_id
  name           = replace(lower("${var.name_prefix}-davinci-signin"), " ", "-")
  description    = "Send PingOne Promote flow sign-ins to the Entra external IdP."
}

resource "pingone_sign_on_policy_action" "entra" {
  environment_id    = var.admin_environment_id
  sign_on_policy_id = pingone_sign_on_policy.signin.id
  priority          = 1
  identity_provider {
    identity_provider_id = var.entra_identity_provider_id
  }
}

resource "pingone_application_sign_on_policy_assignment" "signin" {
  environment_id    = var.admin_environment_id
  application_id    = pingone_application.signin.id
  sign_on_policy_id = pingone_sign_on_policy.signin.id
  priority          = 1
}

# ---------------- Snapshot worker (host environment) ---------------------------
# Reads environments, creates snapshots and reads promotion variables on source
# and target, and reads role assignments in the admin environment. The flow
# never calls the promotions API; deployment stays in GitLab.

resource "pingone_application" "worker" {
  environment_id = var.host_environment_id
  name           = "${var.name_prefix} - DaVinci snapshot worker"
  description    = "Snapshot and role-check worker for the PingOne Promote DaVinci Request flow."
  enabled        = true
  oidc_options = {
    type                       = "WORKER"
    grant_types                = ["CLIENT_CREDENTIALS"]
    token_endpoint_auth_method = "CLIENT_SECRET_BASIC"
  }
}

resource "pingone_application_secret" "worker" {
  environment_id = var.host_environment_id
  application_id = pingone_application.worker.id
}

data "pingone_role" "worker" {
  for_each = toset(var.worker_role_names)
  name     = each.value
}

data "pingone_role" "identity_read" {
  name = "Identity Data Read Only"
}

resource "pingone_application_role_assignment" "worker" {
  for_each = {
    for pair in setproduct(var.worker_role_names, ["source", "target"]) : "${pair[0]}|${pair[1]}" => pair
  }
  environment_id       = var.host_environment_id
  application_id       = pingone_application.worker.id
  role_id              = data.pingone_role.worker[each.value[0]].id
  scope_environment_id = each.value[1] == "source" ? var.source_environment_id : var.target_environment_id
}

resource "pingone_application_role_assignment" "worker_admin_env" {
  environment_id       = var.host_environment_id
  application_id       = pingone_application.worker.id
  role_id              = data.pingone_role.identity_read.id
  scope_environment_id = var.admin_environment_id
}

# ---------------- DaVinci (host environment) ----------------------------------

resource "pingone_davinci_connector_instance" "created" {
  for_each       = local.connectors_to_create
  environment_id = var.host_environment_id
  name           = "${var.name_prefix} ${each.key}"
  connector = {
    id = each.value
  }
}

# Created empty: fill in its OIDC fields once in the console (RUNBOOK step 4).
resource "pingone_davinci_connector_instance" "oidc" {
  count          = var.oidc_connection_id == "" ? 1 : 0
  environment_id = var.host_environment_id
  name           = "${var.name_prefix} admin SSO"
  connector = {
    id = "genericConnector"
  }
  lifecycle {
    ignore_changes = [properties]
  }
}

# Company secrets. Names are unique to this flow; the flow's own variables are
# flow-scoped (flow.tf.json), so nothing shares a name across contexts.
resource "pingone_davinci_variable" "worker_basic_auth" {
  environment_id = var.host_environment_id
  context        = "company"
  name           = "promoteWorkerBasicAuth"
  display_name   = "PingOne Promote worker (Basic auth)"
  data_type      = "secret"
  mutable        = true
  value = {
    secret_string = base64encode("${pingone_application.worker.oidc_options.client_id}:${pingone_application_secret.worker.secret}")
  }
}

resource "pingone_davinci_variable" "gitlab_token" {
  environment_id = var.host_environment_id
  context        = "company"
  name           = "promoteGitlabToken"
  display_name   = "PingOne Promote GitLab token"
  data_type      = "secret"
  mutable        = true
  value = {
    secret_string = var.gitlab_token
  }
}

# pingone_davinci_flow.request and its flow variables live in flow.tf.json,
# generated by: python3 ../gen_flows.py --terraform .

resource "pingone_davinci_flow_deploy" "request" {
  environment_id = var.host_environment_id
  flow_id        = pingone_davinci_flow.request.id
  deploy_trigger_values = {
    graph = sha256(jsonencode(pingone_davinci_flow.request.graph_data))
  }
  depends_on = [pingone_davinci_variable.worker_basic_auth, pingone_davinci_variable.gitlab_token]
}

resource "pingone_davinci_flow_enable" "request" {
  environment_id = var.host_environment_id
  flow_id        = pingone_davinci_flow.request.id
  enabled        = true
  depends_on     = [pingone_davinci_flow_deploy.request]
}

resource "pingone_davinci_application" "request" {
  environment_id = var.host_environment_id
  name           = "${var.name_prefix} Request"
}

resource "pingone_davinci_application_flow_policy" "request" {
  environment_id         = var.host_environment_id
  davinci_application_id = pingone_davinci_application.request.id
  name                   = "${var.name_prefix} Request"
  status                 = "enabled"
  flow_distributions = [{
    id      = pingone_davinci_flow.request.id
    version = -1
    weight  = 100
  }]
  depends_on = [pingone_davinci_flow_enable.request]
  lifecycle {
    create_before_destroy = true
  }
}
