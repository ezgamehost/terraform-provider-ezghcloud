# Serve www.example.com over HTTPS from locations near its visitors, in front of a load balancer.
# Then CNAME www.example.com to ezghcloud_cdn_distribution.web.default_hostname.
resource "ezghcloud_cdn_distribution" "web" {
  project_id = var.project_id
  name       = "web"

  origin = {
    host = "shop-lb.example.com"
  }

  domain {
    hostname       = "www.example.com"
    certificate_id = ezghcloud_certificate.web.id
  }
}
