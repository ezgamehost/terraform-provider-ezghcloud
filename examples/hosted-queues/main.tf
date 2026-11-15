terraform {
  required_providers {
    ezghcloud = {
      source = "ezgamehost/ezghcloud"
    }
  }
}

# Supply EZGHCLOUD_API_TOKEN and EZGHCLOUD_ORGANIZATION_ID in the environment.
provider "ezghcloud" {}

variable "project_id" {
  type        = string
  description = "Existing project ID."
}

variable "subnet_id" {
  type        = string
  description = "Existing subnet (sbn_) for the clusters."
}

# Kafka on three nodes, surviving the loss of any one.
resource "ezghcloud_hosted_queue_cluster" "events" {
  project_id       = var.project_id
  name             = "events"
  engine           = "kafka"
  engine_version   = "4.4"
  mode             = "replicated"
  instance_type    = "gp-1.large"
  storage_size_gib = 100
  subnet_id        = var.subnet_id
}

resource "ezghcloud_hosted_queue_kafka_topic" "orders" {
  project_id = var.project_id
  cluster_id = ezghcloud_hosted_queue_cluster.events.id
  name       = "orders"
  partitions = 6
  config     = { "retention.ms" = "604800000" }
}

# A consumer that reads orders in its own consumer groups; its password is in the Secret secret_id names.
resource "ezghcloud_hosted_queue_user" "billing" {
  project_id = var.project_id
  cluster_id = ezghcloud_hosted_queue_cluster.events.id
  name       = "billing"
  preset     = "custom"
  topics     = [{ topic = ezghcloud_hosted_queue_kafka_topic.orders.name, access = "consume" }]
  groups     = ["billing-*"]
}

# RabbitMQ for a Celery worker, with AMQP on 5672 inside the VPC as well as AMQPS on 5671.
resource "ezghcloud_hosted_queue_cluster" "tasks" {
  project_id       = var.project_id
  name             = "tasks"
  engine           = "rabbitmq"
  engine_version   = "4.4"
  instance_type    = "gp-1.large"
  storage_size_gib = 20
  subnet_id        = var.subnet_id
  plaintext_port   = true
}

resource "ezghcloud_hosted_queue_rabbitmq_vhost" "celery" {
  project_id = var.project_id
  cluster_id = ezghcloud_hosted_queue_cluster.tasks.id
  name       = "celery"
}

resource "ezghcloud_hosted_queue_user" "worker" {
  project_id = var.project_id
  cluster_id = ezghcloud_hosted_queue_cluster.tasks.id
  name       = "worker"
  vhosts     = [{ vhost = ezghcloud_hosted_queue_rabbitmq_vhost.celery.name, preset = "readWrite" }]
}

output "kafka_bootstrap" {
  value = "${ezghcloud_hosted_queue_cluster.events.endpoint_hostname}:${ezghcloud_hosted_queue_cluster.events.endpoint_port}"
}

output "worker_secret_id" {
  value = ezghcloud_hosted_queue_user.worker.secret_id
}
