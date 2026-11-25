# Check allowed_ips includes the address you manage Temps from:
# a wrong value locks you out of the console and the API.
resource "temps_admin_gate_settings" "this" {
  allowed_hosts       = ["temps.example.com"]
  allowed_ips         = ["10.0.0.0/8"]
  trust_forwarded_for = true
}
