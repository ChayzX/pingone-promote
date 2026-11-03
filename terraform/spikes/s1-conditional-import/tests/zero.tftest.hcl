# Zero entries: no import happens, the resource is created normally.
variables {
  pingone_environment_id = "env-a"
  import_ids             = {}
}

run "zero_entries_creates" {
  assert {
    condition     = !startswith(module.davinci.login_id, "env-a/")
    error_message = "no import expected without an entry"
  }
}

run "unrelated_key_is_silently_ignored" {
  # This is why `pingone-promote check` validates every import_ids key.
  variables {
    import_ids = { "module.davinci.terraform_data.typo" = "abc" }
  }
  assert {
    condition     = !startswith(module.davinci.login_id, "env-a/")
    error_message = "an unknown key must not import anything"
  }
}
