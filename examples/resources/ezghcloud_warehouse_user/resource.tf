# A user that reads and writes one database. Its password is in the Secret secret_id names.
resource "ezghcloud_warehouse_user" "example" {
  project_id       = ezghcloud_project.example.id
  warehouse_id     = ezghcloud_warehouse.example.id
  name             = "loader"
  preset           = "custom"
  grants           = [{ database = "events", table = "*", privileges = ["SELECT", "INSERT"] }]
  settings_profile = "heavy"
}
