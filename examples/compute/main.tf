terraform {
  required_providers {
    ezghcloud = {
      source = "ezgamehost/ezghcloud"
    }
  }
}

provider "ezghcloud" {
  organization_id = var.organization_id
  # Supply EZGHCLOUD_API_TOKEN through the environment.
}

variable "organization_id" {
  type = string
}

variable "project_id" {
  type = string
}

variable "subnet_id" {
  type = string
}

variable "ssh_public_key" {
  type = string
}

variable "instance_type" {
  type = string
}

variable "volume_type" {
  type = string
}

resource "ezghcloud_ssh_key" "main" {
  project_id = var.project_id
  name       = "terraform"
  public_key = var.ssh_public_key
}

resource "ezghcloud_volume" "data" {
  project_id  = var.project_id
  name        = "data"
  volume_type = var.volume_type
  size_bytes  = "10737418240"
}

resource "ezghcloud_instance" "main" {
  project_id    = var.project_id
  name          = "server"
  instance_type = var.instance_type
  image         = "linux/ubuntu:24.04"
  ssh_key_ids   = [ezghcloud_ssh_key.main.id]
  volume_ids    = [ezghcloud_volume.data.id]

  network_interfaces = [{
    subnet_id = var.subnet_id
  }]
}

output "instance_id" {
  value = ezghcloud_instance.main.id
}
