terraform {
  required_providers {
    ezghcloud = {
      source = "ezgamehost/ezghcloud"
    }
  }
}

# Set EZGHCLOUD_API_TOKEN and EZGHCLOUD_ORGANIZATION_ID in the environment.
provider "ezghcloud" {}
