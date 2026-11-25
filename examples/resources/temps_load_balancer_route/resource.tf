# Sends traffic for a hostname to a service outside Temps.
resource "temps_load_balancer_route" "legacy" {
  domain = "legacy.example.com"
  host   = "legacy-backend.example.com"
  port   = 8080
}
