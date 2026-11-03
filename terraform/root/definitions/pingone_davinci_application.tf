resource "pingone_davinci_application" "pingcli__Web" {
  environment_id = var.pingone_environment_id
  name           = "Web"

  api_key = {
    enabled = true
  }

  oauth = {
    grant_types   = ["authorizationCode"]
    redirect_uris = var.davinci_application_Web_oauth_redirect_uris
    scopes        = ["openid", "profile"]
  }
}
