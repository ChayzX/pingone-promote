# Root variables pass the environment tfvars straight to the module, which
# owns the typed schema v1 definitions. Secrets are declared one variable per
# secret in secrets.tf (the pipeline sets TF_VAR_<name> for each). Collections are typed `any` here so the
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
