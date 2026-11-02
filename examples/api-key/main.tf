terraform {
  required_providers {
    ezghcloud = {
      source = "ezgamehost/ezghcloud"
    }
  }
}

# Set EZGHCLOUD_API_TOKEN to a user's OAuth bearer token for key creation.
# Set EZGHCLOUD_ORGANIZATION_ID to the owning organization.
provider "ezghcloud" {}

resource "ezghcloud_bot" "automation" {
  name = "automation"
}

# Attach the appropriate policies to the bot separately; this key acts as its owner.
resource "ezghcloud_api_key" "automation" {
  name               = "automation"
  bot_id             = ezghcloud_bot.automation.id
  expires_in_seconds = 86400
}

# The key is kept in Terraform state even though this output is sensitive.
output "automation_token" {
  value     = ezghcloud_api_key.automation.token
  sensitive = true
}
