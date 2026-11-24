data "temps_service_types" "all" {}

output "service_types" {
  value = data.temps_service_types.all.types
}
