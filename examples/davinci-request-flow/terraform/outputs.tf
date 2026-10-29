output "signin_app_client_id" {
  description = "App ID for the flow's OIDC & OAuth IdP connection."
  value       = pingone_application.signin.oidc_options.client_id
}

output "signin_app_client_secret" {
  description = "Client secret for the flow's OIDC & OAuth IdP connection. Read with: terraform output -raw signin_app_client_secret"
  value       = pingone_application_secret.signin.secret
  sensitive   = true
}

output "oidc_issuer" {
  description = "Issuer for the OIDC connection; /authorize, /token and /userinfo sit under it."
  value       = "${local.auth_origin}/${var.admin_environment_id}/as"
}

output "oidc_redirect_uri" {
  description = "Redirect URI registered on the sign-in app. It must match the Redirect URL shown on the OIDC connection."
  value       = local.oidc_redirect_uri
}

output "oidc_connection_id" {
  value = local.connection_ids.oidc
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
