terraform {
  required_providers {
    ezghcloud = {
      source = "ezgamehost/ezghcloud"
    }
  }
}

# Supply EZGHCLOUD_API_TOKEN and EZGHCLOUD_ORGANIZATION_ID in the environment.
provider "ezghcloud" {}

variable "project_id" {
  type        = string
  description = "Existing project ID."
}

variable "subnet_id" {
  type        = string
  description = "Existing subnet (sbn_) for the warehouse."
}

variable "bucket_id" {
  type        = string
  description = "Existing bucket (bkt_) to load from."
}

# Two shards, each on three replicas.
resource "ezghcloud_warehouse" "analytics" {
  project_id       = var.project_id
  name             = "analytics"
  engine_version   = "26.8"
  shards           = 2
  replicas         = 3
  instance_type    = "gp-1.large"
  storage_size_gib = 200
  subnet_id        = var.subnet_id
}

resource "ezghcloud_warehouse_database" "events" {
  project_id   = var.project_id
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "events"
}

# Its password is generated and kept in the Secret named by secret_id.
resource "ezghcloud_warehouse_user" "loader" {
  project_id   = var.project_id
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "loader"
  preset       = "readWrite"
}

# SQL reads the bucket through the connection's name.
resource "ezghcloud_warehouse_connection" "raw" {
  project_id   = var.project_id
  warehouse_id = ezghcloud_warehouse.analytics.id
  name         = "raw"
  type         = "bucket"
  target_id    = var.bucket_id
  access       = "read"
}

output "loader_secret_id" {
  value = ezghcloud_warehouse_user.loader.secret_id
}
