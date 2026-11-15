# A single-node RabbitMQ cluster. mode = "replicated" runs three nodes; a single cluster grows to
# replicated in place.
resource "ezghcloud_hosted_queue_cluster" "example" {
  project_id       = ezghcloud_project.example.id
  name             = "tasks"
  engine           = "rabbitmq"
  engine_version   = "4.4"
  instance_type    = "gp-1.large"
  storage_size_gib = 20
  subnet_id        = ezghcloud_subnet.example.id
}
