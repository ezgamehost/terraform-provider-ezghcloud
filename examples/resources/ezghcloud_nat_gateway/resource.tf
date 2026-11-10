# The gateway goes in a public subnet: its VPC has an attached Network Gateway and the subnet's
# route table routes 0.0.0.0/0 to it. Keep 4 addresses free in the subnet.
resource "ezghcloud_nat_gateway" "example" {
  project_id = ezghcloud_project.example.id
  name       = "egress"
  vpc_id     = ezghcloud_vpc.example.id
  subnet_id  = ezghcloud_subnet.public.id
}

# The address to allowlist elsewhere. It doesn't change for the life of the gateway.
output "egress_address" {
  value = ezghcloud_nat_gateway.example.public_ipv4_addresses[0].address
}
