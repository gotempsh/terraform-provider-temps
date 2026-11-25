resource "temps_node_build_limits" "builder" {
  node_id               = 2
  concurrency           = 2
  queue_wait_seconds    = 600
  build_timeout_seconds = 3600
}
