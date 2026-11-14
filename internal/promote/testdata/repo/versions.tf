terraform {
  required_version = ">= 1.16.0" # conditional imports on static addresses (ADR 0016)

  required_providers {
    pingone = {
      source  = "pingidentity/pingone"
      version = "~> 1.24.0" # the release line the definitions are written for (docs/updating.md)
    }
  }
}

# Worker credentials come from the pipeline environment:
# PINGONE_CLIENT_ID, PINGONE_CLIENT_SECRET, PINGONE_ENVIRONMENT_ID and
# PINGONE_REGION_CODE.
provider "pingone" {}
