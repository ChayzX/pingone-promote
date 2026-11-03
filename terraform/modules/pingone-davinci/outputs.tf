output "connection_ids" {
  description = "Connector instance IDs by logical key."
  value       = local.connection_ids
}

output "flow_ids" {
  description = "Flow IDs by logical key (all tiers)."
  value       = local.flow_ids
}

output "variable_ids" {
  description = "Variable IDs by logical key (global and flow-scoped)."
  value = merge(
    local.global_variable_ids,
    { for k, v in pingone_davinci_variable.flow_scoped : k => v.id },
  )
}

output "application_ids" {
  description = "DaVinci application IDs by logical key."
  value       = { for k, a in pingone_davinci_application.this : k => a.id }
}

output "flow_policy_ids" {
  description = "Application flow policy IDs by \"<app_key>/<policy_key>\"."
  value       = { for k, p in pingone_davinci_application_flow_policy.this : k => p.id }
}

output "flow_deploy_digests" {
  description = "Per-flow definition digest that drives redeployment."
  value       = local.deploy_digests
}

output "required_secret_keys" {
  description = "pingone_secrets keys this configuration references (names only)."
  value       = sort(local.referenced_secrets)
}
