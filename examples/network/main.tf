terraform {
  required_providers {
    ezghcloud = {
      source = "ezgamehost/ezghcloud"
    }
  }
}

# Supply EZGHCLOUD_API_TOKEN and EZGHCLOUD_ORGANIZATION_ID in the environment.
# Until a Registry release exists, configure a local provider dev override.
provider "ezghcloud" {}

variable "project_id" {
  type        = string
  description = "Existing project ID."
}

resource "ezghcloud_vpc" "app" {
  project_id = var.project_id
  name       = "app"
  ipv4_cidr  = "10.40.0.0/16"
}

resource "ezghcloud_subnet" "app" {
  project_id = var.project_id
  vpc_id     = ezghcloud_vpc.app.id
  name       = "app"
  ipv4_cidr  = "10.40.1.0/24"
}

resource "ezghcloud_firewall" "app" {
  project_id = var.project_id
  name       = "app"

  rules = [
    {
      direction  = "inbound"
      protocol   = "tcp"
      peer_cidr  = "10.40.0.0/16"
      ports_from = 22
      ports_to   = 22
    },
    {
      direction = "outbound"
      protocol  = "all"
      peer_cidr = "0.0.0.0/0"
    },
  ]
}

output "subnet_id" {
  value = ezghcloud_subnet.app.id
}

output "firewall_id" {
  value = ezghcloud_firewall.app.id
}
