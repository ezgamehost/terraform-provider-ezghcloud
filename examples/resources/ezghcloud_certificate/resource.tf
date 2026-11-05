resource "ezghcloud_certificate" "example" {
  project_id               = var.project_id
  name                     = "example"
  dns_names                = ["example.com", "*.example.com"]
  domain_authorization_ids = [ezghcloud_certificate_domain_authorization.example.id]
}
