variable "pingone_domain" {
  description = "PingOne top-level domain: pingone.com, pingone.eu, pingone.ca, pingone.com.au, pingone.sg or pingone.asia."
  type        = string
  default     = "pingone.com"
  validation {
    condition     = contains(["pingone.com", "pingone.eu", "pingone.ca", "pingone.com.au", "pingone.sg", "pingone.asia"], var.pingone_domain)
    error_message = "Use one of pingone.com, pingone.eu, pingone.ca, pingone.com.au, pingone.sg or pingone.asia."
  }
}

variable "organization_id" {
  description = "PingOne organization ID."
  type        = string
}

variable "enable_signin" {
  description = "Admin SSO + DaVinci Admin check. Off for the proof of concept; turning it on also needs flow.tf.json regenerated with --signin."
  type        = bool
  default     = false
}

variable "admin_environment_id" {
  description = "Sign-in only: environment holding the admin accounts (they sign in through the Entra external IdP there)."
  type        = string
  default     = ""
}

variable "entra_identity_provider_id" {
  description = "Sign-in only: ID of the existing Entra external identity provider in the admin environment (Integrations > External IdPs)."
  type        = string
  default     = ""
}

variable "host_environment_id" {
  description = "DaVinci environment that runs the flow and holds the snapshot worker. Ideally not a promotion source or target."
  type        = string
}

variable "environments" {
  description = "Up to four environments the requester can choose as source and target, in button order."
  type = list(object({
    name              = string
    id                = string
    gitlab_project_id = optional(string, "") # project whose pipeline deploys here; empty = gitlab_project_id
  }))
  validation {
    condition     = length(var.environments) >= 2 && length(var.environments) <= 4
    error_message = "List two to four environments."
  }
  validation {
    condition     = length(distinct([for e in var.environments : e.id])) == length(var.environments)
    error_message = "Environment IDs must be unique."
  }
}

variable "davinci_admin_role_id" {
  description = "Optional ID of the DaVinci Admin role. Empty = the flow looks it up by name at run time."
  type        = string
  default     = ""
}

variable "worker_role_names" {
  description = "Built-in roles for the snapshot worker on the source and target environments. Use the same set that fixed the pipeline worker's Configuration Management 403s."
  type        = list(string)
  default     = ["Promotion Admin"]
}

variable "gitlab_url" {
  description = "GitLab base URL."
  type        = string
  default     = "https://gitlab.com"
}

variable "gitlab_project_id" {
  description = "Default GitLab project (numeric ID) for environments without their own gitlab_project_id. Each project's pipeline must have that environment as PINGONE_TARGET_ENVIRONMENT_ID."
  type        = string
}

variable "gitlab_environment" {
  description = "Protected GitLab deployment environment named in the merge request."
  type        = string
  default     = "production"
}

variable "gitlab_token" {
  description = "GitLab project access token: api scope, Developer role, promotion project only. Pass from CI or a vault, never commit it."
  type        = string
  sensitive   = true
}

variable "connection_ids" {
  description = "IDs of existing DaVinci connections to reuse (http, functions, error, variables). Leave a key empty to create a dedicated connection."
  type = object({
    http      = optional(string, "")
    functions = optional(string, "")
    error     = optional(string, "")
    variables = optional(string, "")
  })
  default = {}
}

variable "oidc_connection_id" {
  description = "Sign-in only: existing OIDC & OAuth IdP connection to reuse. Leave empty (recommended) to create one dedicated to this flow's sign-in app."
  type        = string
  default     = ""
}

variable "oidc_redirect_uri" {
  description = "Sign-in only: redirect URL shown on the flow's OIDC & OAuth IdP connection. Empty = https://auth.<domain>/<host env>/rp/callback/genericConnector; confirm it matches the connection."
  type        = string
  default     = ""
}

variable "name_prefix" {
  description = "Prefix for every created object's name, so they are easy to find and clean up."
  type        = string
  default     = "PingOne Promote"
}
