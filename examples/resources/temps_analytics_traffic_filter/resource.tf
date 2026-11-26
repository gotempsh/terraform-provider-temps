resource "temps_analytics_traffic_filter" "spam" {
  project_id = temps_project.web.id
  kind       = "referrer"
  value      = "spam-site.example"
  note       = "Referral spam"
}

resource "temps_analytics_traffic_filter" "office" {
  project_id = temps_project.web.id
  kind       = "ip"
  value      = "203.0.113.0/24"
  note       = "Our own office"
}
