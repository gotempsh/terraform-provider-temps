resource "temps_otel_alert_rule" "checkout_latency" {
  project_id        = temps_project.web.id
  name              = "Slow checkout"
  metric_name       = "http.server.duration"
  aggregation       = "p95"
  detection_config  = jsonencode({ kind = "static", comparator = "gt", threshold = 500 })
  window_secs       = 300
  for_duration_secs = 120
  severity          = "warning"
  label_filters     = jsonencode([["http.route", "/checkout"]])
}
