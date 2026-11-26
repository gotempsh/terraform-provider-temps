resource "temps_node_alert_rule" "worker_memory" {
  node_id           = 7
  name              = "Worker memory high"
  metric_name       = "node.memory_percent"
  threshold         = 90
  comparator        = ">"
  severity          = "warning"
  for_duration_secs = 300
}
