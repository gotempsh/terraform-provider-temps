# Workloads of service 12 may not send mail directly.
resource "temps_network_policy_rule" "no_smtp" {
  subject_kind = "service"
  subject_id   = 12
  direction    = "egress"
  action       = "deny"
  peer_kind    = "cidr"
  peer_ref     = "0.0.0.0/0"
  protocol     = "tcp"
  port_start   = 25
  port_end     = 25
}
