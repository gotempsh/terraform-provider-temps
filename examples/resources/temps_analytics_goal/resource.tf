resource "temps_analytics_goal" "upgrade" {
  project_id       = temps_project.web.id
  name             = "Upgrade to Pro"
  kind             = "event"
  event_name       = "plan_upgraded"
  event_filter     = jsonencode([{ type = "custom_data", value = { path = "plan", value = "pro" } }])
  revenue_property = "amount"
  revenue_currency = "EUR"
}

resource "temps_analytics_goal" "thank_you" {
  project_id = temps_project.web.id
  name       = "Reached thank-you page"
  kind       = "page"
  page_path  = "/thank-you"
}
