# Outbound IPv4 for a private subnet: the NAT gateway sits in a public subnet, and the private
# subnet uses a route table whose default route targets it.
resource "ezghcloud_nat_gateway" "example" {
  project_id = ezghcloud_project.example.id
  name       = "egress"
  vpc_id     = ezghcloud_vpc.example.id
  subnet_id  = ezghcloud_subnet.public.id
}

resource "ezghcloud_route_table" "private" {
  project_id = ezghcloud_project.example.id
  vpc_id     = ezghcloud_vpc.example.id
  name       = "private"

  routes = [{
    destination = "0.0.0.0/0"
    target_type = "natGateway"
    target_id   = ezghcloud_nat_gateway.example.id
  }]
}

resource "ezghcloud_subnet" "private" {
  project_id     = ezghcloud_project.example.id
  vpc_id         = ezghcloud_vpc.example.id
  name           = "private"
  ipv4_cidr      = "10.0.2.0/24"
  route_table_id = ezghcloud_route_table.private.id
}
