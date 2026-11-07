resource "ezghcloud_waf" "web" {
  project_id = var.project_id
  name       = "web"

  managed_rule_sets = [
    { name = "ezgh/core" },
    # Record what the PHP rules would block before turning them on.
    { name = "ezgh/php", mode = "log" },
  ]

  rules = [
    {
      name       = "office"
      action     = "allow"
      conditions = [{ type = "ipSet", cidrs = ["203.0.113.0/24"] }]
    },
    {
      name       = "login"
      action     = "rateLimit"
      rate_limit = { limit = 20, window_seconds = 60 }
      conditions = [
        { type = "equals", target = "path", value = "/login" },
        { type = "equals", target = "method", value = "POST" },
      ]
    },
  ]

  logging = { scope = "matched", environment = "production" }
}
