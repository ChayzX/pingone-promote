variable "pingone_environment_id" {
  type        = string
  description = "The PingOne environment ID to configure DaVinci resources in"

  validation {
    condition     = can(regex("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", var.pingone_environment_id))
    error_message = "The PingOne Environment ID must be a valid PingOne resource ID (UUID format)."
  }
}

variable "PingOne_DaVinci_connection_PingOne_clientSecret" {
  type        = string
  description = "PingOne secret PingOne_DaVinci_connection_PingOne_clientSecret (pipeline: TF_VAR_PingOne_DaVinci_connection_PingOne_clientSecret)."
  sensitive   = true
}

variable "davinci_application_Web_oauth_redirect_uris" {
  type        = list(string)
  description = "Environment value for oauth.redirect_uris of pingone_davinci_application.pingcli__Web"
}

variable "davinci_variable_apiKey_value" {
  type        = string
  description = "PingOne secret davinci_variable_apiKey_value (pipeline: TF_VAR_davinci_variable_apiKey_value)."
  sensitive   = true
}

variable "davinci_variable_companyName_value" {
  type        = string
  description = "Value for DaVinci variable companyName"
}
