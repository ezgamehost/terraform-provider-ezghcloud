resource "ezghcloud_certificate_domain_authorization" "example" {
  domain_name = "example.com"
  dns_zone_id = ezghcloud_dns_zone.main.id # omit to add the dns_instructions records yourself
}
