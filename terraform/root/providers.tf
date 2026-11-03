terraform {
  required_version = ">= 1.15.0" # child-module import blocks; see the module versions.tf

  required_providers {
    pingone = {
      source  = "pingidentity/pingone"
      version = "~> 1.23"
    }
  }
}

# Worker client credentials come from the pipeline environment, never from
# tfvars. Provider 1.23 reads:
#   PINGONE_CLIENT_ID, PINGONE_CLIENT_SECRET, PINGONE_ENVIRONMENT_ID
#   (the worker's authentication environment) and PINGONE_REGION_CODE
#   (AP, AU, CA, EU, NA or SG).
provider "pingone" {}
