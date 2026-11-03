terraform {
  # 1.15.0 is the first release that accepts import blocks inside a child
  # module (verified: 1.7.0 through 1.14.0 reject them at init with "Import
  # blocks are only allowed in the root module"; 1.15.0, 1.16.0, 1.17.0 and
  # 1.17.1 accept them).
  required_version = ">= 1.15.0"

  required_providers {
    pingone = {
      source  = "pingidentity/pingone"
      version = "~> 1.23"
    }
  }
}
