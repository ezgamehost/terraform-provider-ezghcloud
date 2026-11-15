# SQL reads the bucket as s3(lake, filename = 'events/*.parquet').
resource "ezghcloud_warehouse_connection" "lake" {
  project_id   = ezghcloud_project.example.id
  warehouse_id = ezghcloud_warehouse.example.id
  name         = "lake"
  type         = "bucket"
  target_id    = "bkt_7hq2m0x9ka3c"
  access       = "read"
}

# A Kafka table engine reads topics starting clicks. as ENGINE = Kafka(clicks).
resource "ezghcloud_warehouse_connection" "clicks" {
  project_id   = ezghcloud_project.example.id
  warehouse_id = ezghcloud_warehouse.example.id
  name         = "clicks"
  type         = "kafka"
  target_id    = "hqc_4k2m9x0a7qpe"
  topic_prefix = "clicks."
}
