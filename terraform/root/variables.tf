# Root variables pass the environment tfvars straight to the module, which
# owns the typed schema v1 definitions. Collections are typed `any` here so the
# schema lives in exactly one place (the module); the module call converts
# and validates them.

variable "tfvars_schema_version" {
  description = "tfvars schema version; validated by the module."
  type        = number
}

variable "pingone_environment_id" {
  description = "PingOne environment managed by this stack."
  type        = string
}

variable "pingone_secrets" {
  description = "Secret values by key. Supplied by the pipeline as TF_VAR_pingone_secrets (JSON); never in tfvars."
  type        = map(string)
  default     = {}
  nullable    = false
  sensitive   = true

  validation {
    # Conservative so keys can map onto enterprise pipeline variable names.
    condition     = alltrue([for k in nonsensitive(keys(var.pingone_secrets)) : can(regex("^[a-z0-9_]{1,128}$", k))])
    error_message = "pingone_secrets keys must match ^[a-z0-9_]{1,128}$ (lowercase letters, digits and underscores)."
  }
}

variable "parameters" {
  description = "Environment-local non-secret values."
  type        = map(string)
  default     = {}
  nullable    = false
}

variable "davinci_connections" {
  description = "See the module's variables.tf."
  type        = any
  default     = {}
}

variable "davinci_variables" {
  description = "See the module's variables.tf."
  type        = any
  default     = {}
}

variable "davinci_flows" {
  description = "See the module's variables.tf."
  type        = any
  default     = {}
}

variable "davinci_applications" {
  description = "See the module's variables.tf."
  type        = any
  default     = {}
}

variable "import_ids" {
  description = "Existing PingOne IDs to adopt, by logical key."
  type        = any
  default     = {}
}
