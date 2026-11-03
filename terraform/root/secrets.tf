# Managed by pingone-promote. One variable per PingOne secret; the pipeline sets TF_VAR_<name>.

variable "PingOne_DaVinci_connection_PingOne_clientSecret" {
  description = "PingOne secret PingOne_DaVinci_connection_PingOne_clientSecret (pipeline: TF_VAR_PingOne_DaVinci_connection_PingOne_clientSecret)."
  type        = string
  sensitive   = true
  default     = null
}

variable "davinci_variable_apiToken_value" {
  description = "PingOne secret davinci_variable_apiToken_value (pipeline: TF_VAR_davinci_variable_apiToken_value)."
  type        = string
  sensitive   = true
  default     = null
}

variable "login_hmac_key" {
  description = "PingOne secret login_hmac_key (pipeline: TF_VAR_login_hmac_key)."
  type        = string
  sensitive   = true
  default     = null
}

locals {
  pingone_secrets = {
    "PingOne_DaVinci_connection_PingOne_clientSecret" = var.PingOne_DaVinci_connection_PingOne_clientSecret
    "davinci_variable_apiToken_value"                 = var.davinci_variable_apiToken_value
    "login_hmac_key"                                  = var.login_hmac_key
  }
}
