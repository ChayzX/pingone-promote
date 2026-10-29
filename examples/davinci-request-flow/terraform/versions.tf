terraform {
  required_version = ">= 1.5"
  required_providers {
    pingone = {
      source  = "pingidentity/pingone"
      version = "~> 1.23"
    }
  }
}

# Credentials come from the environment, as in your existing PingOne Terraform:
# PINGONE_CLIENT_ID, PINGONE_CLIENT_SECRET, PINGONE_ENVIRONMENT_ID (the
# environment that holds that admin worker) and PINGONE_REGION_CODE.
provider "pingone" {}
