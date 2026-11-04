resource "ezghcloud_file_system" "shared" {
  project_id  = var.project_id
  name        = "shared"
  vpc_id      = var.vpc_id
  require_tls = true
}
