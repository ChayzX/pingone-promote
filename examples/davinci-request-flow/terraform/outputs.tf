output "signin_app_client_id" {
  description = "Sign-in only: App ID for the flow's OIDC & OAuth IdP connection."
  value       = try(pingone_application.signin[0].oidc_options.client_id, null)
}

output "signin_app_client_secret" {
  description = "Sign-in only: client secret for the flow's OIDC & OAuth IdP connection. Read with: terraform output -raw signin_app_client_secret"
  value       = try(pingone_application_secret.signin[0].secret, null)
  sensitive   = true
}

output "oidc_issuer" {
  description = "Sign-in only: issuer for the OIDC connection; /authorize, /token and /userinfo sit under it."
  value       = var.enable_signin ? "${local.auth_origin}/${var.admin_environment_id}/as" : null
}

output "oidc_redirect_uri" {
  description = "Sign-in only: redirect URI registered on the sign-in app. It must match the Redirect URL shown on the OIDC connection."
  value       = var.enable_signin ? local.oidc_redirect_uri : null
}

output "oidc_connection_id" {
  value = var.enable_signin ? local.connection_ids.oidc : null
}

output "flow_id" {
  value = pingone_davinci_flow.request.id
}

output "davinci_application_id" {
  value = pingone_davinci_application.request.id
}

output "flow_policy_id" {
  description = "Flow policy that launches the Request flow."
  value       = pingone_davinci_application_flow_policy.request.id
}
