# A certificate for a domain whose DNS is hosted in EZGH DNS: one apply creates the zone, has EZGH
# write the validation records into it, and waits for the certificate to be issued. The domain must
# already be delegated to the zone's nameservers.
variable "project_id" { type = string }

resource "ezghcloud_dns_zone" "main" {
  project_id = var.project_id
  domain     = "example.com"
}

resource "ezghcloud_certificate_domain_authorization" "main" {
  domain_name = ezghcloud_dns_zone.main.domain
  dns_zone_id = ezghcloud_dns_zone.main.id
}

resource "ezghcloud_certificate" "main" {
  project_id               = var.project_id
  name                     = "example"
  dns_names                = ["example.com"]
  domain_authorization_ids = [ezghcloud_certificate_domain_authorization.main.id]
}
