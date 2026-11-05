resource "ezghcloud_dns_zone" "main" {
  project_id = var.project_id
  domain     = "example.com"
}
