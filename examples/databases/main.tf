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

variable "subnet_id" {
  type        = string
  description = "Existing subnet (sbn_) for the database's endpoint."
}

resource "ezghcloud_database" "orders" {
  project_id       = var.project_id
  name             = "orders"
  engine           = "postgresql"
  engine_version   = "18"
  instance_type    = "gs-1.large"
  storage_size_gib = 20
  subnet_id        = var.subnet_id

  backup_retention_days = 14
  parameters            = { work_mem = "8MB" }
  deletion_protection   = true
}

# A manual backup outlives the database.
resource "ezghcloud_database_backup" "before_migration" {
  project_id  = var.project_id
  database_id = ezghcloud_database.orders.id
}

# Instances behind this firewall can reach the database.
resource "ezghcloud_firewall" "app" {
  project_id = var.project_id
  name       = "app"

  rules = [
    {
      direction        = "outbound"
      protocol         = "tcp"
      peer_database_id = ezghcloud_database.orders.id
      ports_from       = ezghcloud_database.orders.endpoint_port
      ports_to         = ezghcloud_database.orders.endpoint_port
    },
  ]
}

output "endpoint" {
  value = "${ezghcloud_database.orders.endpoint_hostname}:${ezghcloud_database.orders.endpoint_port}"
}

# The admin username and password live in this Secret, never in Terraform state.
output "admin_secret_id" {
  value = ezghcloud_database.orders.admin_secret_id
}
