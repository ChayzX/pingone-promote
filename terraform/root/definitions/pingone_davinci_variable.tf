resource "pingone_davinci_variable" "pingcli__apiKey_company" {
  environment_id = var.pingone_environment_id
  name           = "apiKey"
  context        = "company"
  data_type      = "secret"
  mutable        = true

  value = {
    secret_string = var.davinci_variable_apiKey_value
  }
}

resource "pingone_davinci_variable" "pingcli__attempts_flowInstance" {
  environment_id = var.pingone_environment_id
  name           = "attempts"
  context        = "flowInstance"
  data_type      = "number"
  mutable        = true
  min            = 0
  max            = 2000
}

resource "pingone_davinci_variable" "pingcli__companyName_company" {
  environment_id = var.pingone_environment_id
  name           = "companyName"
  context        = "company"
  data_type      = "string"
  mutable        = true
  min            = 0
  max            = 2000

  value = {
    string = var.davinci_variable_companyName_value
  }
}
