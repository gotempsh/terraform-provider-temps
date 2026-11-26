resource "temps_service_alert_rule" "connections" {
  service_id        = temps_service.db.id
  name              = "Connections near the limit"
  metric_name       = "postgres.connections"
  threshold         = 180
  comparator        = ">="
  severity          = "critical"
  for_duration_secs = 120
}
