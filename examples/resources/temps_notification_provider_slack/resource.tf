resource "temps_notification_provider_slack" "ops" {
  name        = "Ops channel"
  webhook_url = var.slack_webhook_url
  channel     = "#ops-alerts"
}
