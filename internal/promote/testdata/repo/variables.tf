variable "pingone_environment_id" {
  type        = string
  description = "The PingOne environment ID to configure DaVinci resources in"

  validation {
    condition     = can(regex("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", var.pingone_environment_id))
    error_message = "The PingOne Environment ID must be a valid PingOne resource ID (UUID format)."
  }
}

variable "davinci_application_Web_oauth_redirect_uris" {
  type        = list(string)
  description = "Environment value for oauth.redirect_uris of pingone_davinci_application.pingcli__Web"
}

variable "davinci_variable_companyName_value" {
  type        = string
  description = "Value for DaVinci variable companyName"
}

variable "import_ids" {
  description = "Existing PingOne IDs to adopt in this environment, by module address (module.<name>.<type>.<label>). The environment ID prefix is added automatically."
  type        = map(string)
  default     = {}
  nullable    = false
}
