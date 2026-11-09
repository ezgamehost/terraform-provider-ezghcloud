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
  description = "Existing subnet (sbn_) for the caches."
}

# A WordPress object cache: Valkey, memory only, with a standby.
resource "ezghcloud_cache" "wp" {
  project_id     = var.project_id
  name           = "wp-cache"
  engine         = "valkey"
  engine_version = "9.1"
  instance_type  = "gp-1.small"
  standby        = true
  subnet_id      = var.subnet_id

  parameters = { "maxmemory-policy" = "allkeys-lru" }
}

# A persistent Valkey cache for sessions and rate limits.
resource "ezghcloud_cache" "sessions" {
  project_id       = var.project_id
  name             = "sessions"
  engine           = "valkey"
  engine_version   = "9.1"
  mode             = "persistent"
  instance_type    = "gp-1.medium"
  storage_size_gib = 20
  subnet_id        = var.subnet_id
}

# Memcached, with port 11212 without TLS for WordPress's memcached extension.
resource "ezghcloud_cache" "wp_memcached" {
  project_id     = var.project_id
  name           = "wp-memcached"
  engine         = "memcached"
  engine_version = "1.6"
  instance_type  = "gs-1.small"
  subnet_id      = var.subnet_id
  plaintext_port = true
}

# A read-only user; its password is in the Secret secret_id names.
resource "ezghcloud_cache_user" "reports" {
  project_id = var.project_id
  cache_id   = ezghcloud_cache.sessions.id
  name       = "reports"
  preset     = "readOnly"
}

output "wp_endpoint" {
  value = "${ezghcloud_cache.wp.endpoint_hostname}:${ezghcloud_cache.wp.endpoint_port}"
}

output "reports_secret_id" {
  value = ezghcloud_cache_user.reports.secret_id
}
