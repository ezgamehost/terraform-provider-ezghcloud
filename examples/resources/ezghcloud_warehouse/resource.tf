# Two shards with two replicas each. Shards and replicas grow in place.
resource "ezghcloud_warehouse" "example" {
  project_id       = ezghcloud_project.example.id
  name             = "analytics"
  engine_version   = "26.8"
  shards           = 2
  replicas         = 2
  instance_type    = "gp-1.large"
  storage_size_gib = 500
  subnet_id        = ezghcloud_subnet.example.id
}
