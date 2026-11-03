module "davinci" {
  source = "./definitions"

  pingone_environment_id                          = var.pingone_environment_id
  PingOne_DaVinci_connection_PingOne_clientSecret = var.PingOne_DaVinci_connection_PingOne_clientSecret
  davinci_application_Web_oauth_redirect_uris     = var.davinci_application_Web_oauth_redirect_uris
  davinci_variable_apiKey_value                   = var.davinci_variable_apiKey_value
  davinci_variable_companyName_value              = var.davinci_variable_companyName_value
}
