# Inspect every request to the distribution before it reaches the origin.
resource "ezghcloud_cdn_distribution_waf" "web" {
  project_id      = var.project_id
  distribution_id = ezghcloud_cdn_distribution.web.id
  waf_id          = ezghcloud_waf.web.id
}
