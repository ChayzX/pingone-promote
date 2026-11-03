# One entry imports; after adoption a stale entry is ignored. Runs offline:
# terraform_data is built in. Run blocks share one state, in order.
variables {
  pingone_environment_id = "env-a"
}

run "one_entry_imports" {
  variables {
    import_ids = { "module.davinci.terraform_data.pingcli__Login-0020-Flow" = "abc" }
  }
  assert {
    condition     = module.davinci.login_id == "env-a/abc"
    error_message = "the resource must be imported with the <env>/<id> form"
  }
}

run "stale_entry_after_adoption_is_ignored" {
  variables {
    import_ids = { "module.davinci.terraform_data.pingcli__Login-0020-Flow" = "a-different-id" }
  }
  assert {
    condition     = module.davinci.login_id == "env-a/abc"
    error_message = "a stale entry must not re-import once the address is in state"
  }
}

run "entry_removed_after_adoption_keeps_resource" {
  variables {
    import_ids = {}
  }
  assert {
    condition     = module.davinci.login_id == "env-a/abc"
    error_message = "removing the entry after adoption must not change anything"
  }
}
