resource "ezghcloud_access_point" "wp_content" {
  project_id     = var.project_id
  file_system_id = ezghcloud_file_system.shared.id
  name           = "wp-content"

  root_directory = {
    path = "/wordpress/wp-content"
    creation_info = {
      owner_uid   = 33
      owner_gid   = 33
      permissions = "755"
    }
  }

  posix_user = {
    uid = 33
    gid = 33
  }
}
