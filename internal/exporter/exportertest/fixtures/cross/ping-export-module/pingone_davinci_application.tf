
resource "pingone_davinci_application" "pingcli__Web" {
  environment_id = var.pingone_environment_id
  name           = "Web"

  api_key = {
    enabled = true
  }

  oauth = {
    grant_types   = ["authorizationCode"]
    redirect_uris = ["https://auth.pingone.com/11111111-1111-4111-8111-111111111111/rp/callback/openid_connect"]
    scopes        = ["openid", "profile"]
  }
}
