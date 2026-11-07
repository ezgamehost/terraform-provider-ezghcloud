# Inspect requests to the load balancer's http and https listeners.
resource "ezghcloud_load_balancer_waf" "web" {
  project_id       = var.project_id
  load_balancer_id = var.load_balancer_id
  waf_id           = ezghcloud_waf.web.id
}
