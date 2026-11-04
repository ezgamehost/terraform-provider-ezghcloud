resource "ezghcloud_mount_target" "web" {
  project_id     = var.project_id
  file_system_id = ezghcloud_file_system.shared.id
  subnet_id      = var.subnet_id
}

# Mount with:
# sudo mount -t nfs4 -o nfsvers=4.1,xprtsec=tls,hard,noresvport,rsize=1048576,wsize=1048576,timeo=600,retrans=2 \
#   <hostname>:/<file system ID> /mnt/shared
output "mount_source" {
  value = "${ezghcloud_mount_target.web.hostname}:/${ezghcloud_file_system.shared.id}"
}
