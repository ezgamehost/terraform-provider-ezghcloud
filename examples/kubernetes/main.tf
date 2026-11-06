terraform {
  required_providers {
    ezghcloud = {
      source = "ezgamehost/ezghcloud"
    }
    kubernetes = {
      source = "hashicorp/kubernetes"
    }
  }
}

# Supply EZGHCLOUD_API_TOKEN and EZGHCLOUD_ORGANIZATION_ID in the environment, and
# EZGHCLOUD_PREVIEW_KUBERNETES=1 while EZGH Kubernetes is in preview.
provider "ezghcloud" {}

variable "project_id" {
  type        = string
  description = "Existing project ID."
}

variable "subnet_id" {
  type        = string
  description = "Existing private subnet (sbn_) whose route table sends 0.0.0.0/0 to a Network Gateway."
}

variable "cluster_bot_id" {
  type        = string
  description = "Bot (bot_) with KubernetesClusterAccess the cluster's controllers act as."
}

variable "node_bot_id" {
  type        = string
  description = "Bot (bot_) with KubernetesNodeAccess the nodes run as; the cluster's Bot needs bots.pass on it."
}

resource "ezghcloud_kubernetes_cluster" "prod" {
  project_id = var.project_id
  name       = "prod"
  subnet_id  = var.subnet_id
  bot_id     = var.cluster_bot_id
}

resource "ezghcloud_kubernetes_node_group" "general" {
  project_id    = var.project_id
  cluster_id    = ezghcloud_kubernetes_cluster.prod.id
  name          = "general"
  instance_type = "gp-1.large"
  subnet_id     = var.subnet_id
  bot_id        = var.node_bot_id
  min_size      = 1
  max_size      = 10
}

resource "ezghcloud_kubernetes_addon" "metrics_server" {
  project_id = var.project_id
  cluster_id = ezghcloud_kubernetes_cluster.prod.id
  name       = "metrics-server"
}

# kubectl access is checked against EZGH IAM on each request: the token comes from the ezgh CLI,
# never from Terraform state.
provider "kubernetes" {
  host                   = "https://${ezghcloud_kubernetes_cluster.prod.endpoint_hostname}"
  cluster_ca_certificate = ezghcloud_kubernetes_cluster.prod.certificate_authority
  exec {
    api_version = "client.authentication.k8s.io/v1"
    command     = "ezgh"
    args        = ["kubernetes", "token", "--cluster", ezghcloud_kubernetes_cluster.prod.id]
  }
}

resource "kubernetes_namespace" "shop" {
  metadata {
    name = "shop"
  }
  depends_on = [ezghcloud_kubernetes_node_group.general]
}
