# Managed by pingone-promote. One variable per PingOne secret; the pipeline sets TF_VAR_<name>.

variable "PingOne_DaVinci_connection_client" {
  description = "PingOne secret PingOne_DaVinci_connection_client (pipeline: TF_VAR_PingOne_DaVinci_connection_client)."
  type        = string
  sensitive   = true
  default     = null
}

variable "PingOne_DaVinci_flow_Login_n3sso_hmacKey" {
  description = "PingOne secret PingOne_DaVinci_flow_Login_n3sso_hmacKey (pipeline: TF_VAR_PingOne_DaVinci_flow_Login_n3sso_hmacKey)."
  type        = string
  sensitive   = true
  default     = null
}

locals {
  pingone_secrets = {
    "PingOne_DaVinci_connection_client"        = var.PingOne_DaVinci_connection_client
    "PingOne_DaVinci_flow_Login_n3sso_hmacKey" = var.PingOne_DaVinci_flow_Login_n3sso_hmacKey
  }
}
