resource "ezghcloud_dns_record_set" "www" {
  project_id = var.project_id
  zone_id    = ezghcloud_dns_zone.main.id
  name       = "www"
  type       = "A"
  ttl        = 300
  values     = ["192.0.2.1"]
}
