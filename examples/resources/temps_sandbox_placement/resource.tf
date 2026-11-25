# Run AI sandboxes only on node 2.
resource "temps_sandbox_placement" "this" {
  allowed_node_ids = [2]
}
