# Node 0 is the control plane.
resource "temps_node_resources" "control_plane" {
  node_id                    = 0
  resource_policy            = "best_effort"
  system_reserved_cpu_millis = 1000
  system_reserved_memory_mb  = 2048
}
