terraform {
  required_providers {
    ezghcloud = {
      source = "ezgamehost/ezghcloud"
    }
  }
}

# Use a development override until the provider has a Registry release.
# Set EZGHCLOUD_API_TOKEN and EZGHCLOUD_ORGANIZATION_ID in the environment.
provider "ezghcloud" {}

variable "ssh_public_key" {
  type        = string
  description = "One OpenSSH public key line. Keep the private key outside Terraform."
}

variable "instance_type" {
  type        = string
  description = "An available instance type in your organization."
}

variable "volume_type" {
  type        = string
  description = "An available block storage volume type compatible with the instance."
}

resource "ezghcloud_project" "app" {
  name = "terraform-app"
}

resource "ezghcloud_vpc" "app" {
  project_id = ezghcloud_project.app.id
  name       = "app"
  ipv4_cidr  = "10.40.0.0/16"
}

resource "ezghcloud_subnet" "app" {
  project_id = ezghcloud_project.app.id
  vpc_id     = ezghcloud_vpc.app.id
  name       = "app"
  ipv4_cidr  = "10.40.1.0/24"
}

resource "ezghcloud_firewall" "app" {
  project_id = ezghcloud_project.app.id
  name       = "app"
  rules = [
    {
      direction  = "inbound"
      protocol   = "tcp"
      peer_cidr  = "10.40.0.0/16"
      ports_from = 22
      ports_to   = 22
    },
    {
      direction = "outbound"
      protocol  = "all"
      peer_cidr = "0.0.0.0/0"
    },
  ]
}

resource "ezghcloud_ssh_key" "app" {
  project_id = ezghcloud_project.app.id
  name       = "app"
  public_key = var.ssh_public_key
}

resource "ezghcloud_volume" "data" {
  project_id  = ezghcloud_project.app.id
  name        = "app-data"
  volume_type = var.volume_type
  size_bytes  = "10737418240"
}

resource "ezghcloud_instance" "app" {
  project_id    = ezghcloud_project.app.id
  name          = "app"
  instance_type = var.instance_type
  image         = "linux/ubuntu:24.04"
  ssh_key_ids   = [ezghcloud_ssh_key.app.id]
  volume_ids    = [ezghcloud_volume.data.id]
  root_volume = {
    volume_type = var.volume_type
  }
  network_interfaces = [{
    subnet_id    = ezghcloud_subnet.app.id
    firewall_ids = [ezghcloud_firewall.app.id]
    ipv4         = false
    ipv6         = false
  }]
}

output "instance_id" {
  value = ezghcloud_instance.app.id
}
