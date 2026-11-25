resource "temps_global_secret" "registry" {
  name        = "REGISTRY_PASSWORD"
  value       = var.registry_password
  description = "Password of the private image registry"
}
