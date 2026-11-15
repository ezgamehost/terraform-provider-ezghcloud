# A database on every shard and replica. Tables in it are your SQL's.
resource "ezghcloud_warehouse_database" "example" {
  project_id   = ezghcloud_project.example.id
  warehouse_id = ezghcloud_warehouse.example.id
  name         = "events"
}
