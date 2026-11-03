# Stand-ins for non-for_each resources in a terraformer child module.
resource "terraform_data" "pingcli__Login-0020-Flow" {
  input = "shared-definition"
}

resource "terraform_data" "pingcli__other" {
  input = "other"
}

output "login_id" {
  value = terraform_data.pingcli__Login-0020-Flow.id
}
