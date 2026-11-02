terraform {
  required_providers {
    ezghcloud = {
      source = "ezgamehost/ezghcloud"
    }
  }
}

# Set EZGHCLOUD_API_TOKEN and EZGHCLOUD_ORGANIZATION_ID in the environment.
provider "ezghcloud" {}

resource "ezghcloud_bot" "deployer" {
  name = "terraform-deployer"
}

resource "ezghcloud_group" "deployers" {
  name        = "deployers"
  description = "Infrastructure deployment bots"
}

resource "ezghcloud_group_membership" "deployer" {
  group_id       = ezghcloud_group.deployers.id
  principal_type = "bot"
  principal_id   = ezghcloud_bot.deployer.id
}

resource "ezghcloud_policy" "read" {
  name        = "DeploymentReadAccess"
  description = "Read organization projects"
  statements = [{
    effect    = "allow"
    actions   = ["projects.list", "projects.get"]
    resources = ["*"]
  }]
}

resource "ezghcloud_policy_attachment" "read" {
  principal_type = "group"
  principal_id   = ezghcloud_group.deployers.id
  policy_id      = ezghcloud_policy.read.id
}

resource "ezghcloud_oidc_provider" "github" {
  name      = "GitHub Actions"
  issuer    = "https://token.actions.githubusercontent.com"
  audiences = ["ezghcloud"]
}

variable "github_repository" {
  description = "GitHub owner/repository permitted to assume the deployment bot"
  type        = string
}

resource "ezghcloud_bot_oidc_trust" "github" {
  bot_id      = ezghcloud_bot.deployer.id
  provider_id = ezghcloud_oidc_provider.github.id
  subject     = "repo:${var.github_repository}:ref:refs/heads/main"
  claims      = { repository = var.github_repository }
}
