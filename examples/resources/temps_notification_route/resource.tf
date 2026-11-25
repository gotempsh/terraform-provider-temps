resource "temps_notification_provider" "pager" {
  name = "on-call pager"
  url  = "https://hooks.example.com/pager"
}

# Page the on-call engineer for critical and emergency alerts only.
resource "temps_notification_route" "page" {
  name         = "page on-call"
  min_severity = "critical"
  max_severity = "emergency"
  provider_ids = [temps_notification_provider.pager.id]
}
