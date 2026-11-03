resource "pingone_davinci_application" "this" {
  for_each   = var.davinci_applications
  depends_on = [terraform_data.contract]

  environment_id = var.pingone_environment_id
  name           = each.value.name
  api_key = each.value.api_key_enabled == null ? null : {
    enabled = each.value.api_key_enabled
  }
  oauth = each.value.oauth == null ? null : {
    enforce_signed_request_openid = each.value.oauth.enforce_signed_request_openid
    grant_types                   = each.value.oauth.grant_types
    logout_uris                   = each.value.oauth.logout_uris
    redirect_uris                 = each.value.oauth.redirect_uris
    scopes                        = each.value.oauth.scopes
    sp_jwks_openid                = each.value.oauth.sp_jwks_openid
    sp_jwks_url                   = each.value.oauth.sp_jwks_url
  }
}

locals {
  # Flow policies flattened to "<app_key>/<policy_key>".
  flow_policies = merge([
    for ak, a in var.davinci_applications : {
      for pk, p in a.flow_policies : "${ak}/${pk}" => merge(p, { application = ak })
    }
  ]...)
}

resource "pingone_davinci_application_flow_policy" "this" {
  for_each   = local.flow_policies
  depends_on = [terraform_data.contract]

  environment_id         = var.pingone_environment_id
  davinci_application_id = pingone_davinci_application.this[each.value.application].id
  name                   = each.value.name
  status                 = each.value.status
  trigger                = each.value.trigger
  flow_distributions = [
    for d in each.value.distributions : {
      id            = lookup(local.flow_ids, d.flow, "")
      version       = d.version
      weight        = d.weight
      ip            = d.ip
      success_nodes = d.success_nodes == null ? null : [for s in d.success_nodes : { id = s }]
    }
  ]

  # A policy must stop pointing at a flow before that flow is destroyed in
  # the same apply (provider guidance).
  lifecycle {
    create_before_destroy = true
  }
}
