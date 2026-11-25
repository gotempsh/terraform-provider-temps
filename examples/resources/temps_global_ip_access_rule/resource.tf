resource "temps_global_ip_access_rule" "scanner" {
  ip_address = "203.0.113.0/24"
  action     = "block"
  reason     = "Abusive scanner"
}
