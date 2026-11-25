resource "temps_workload_volume" "data" {
  environment_id = temps_environment.production.id
  name           = "data"
  size_mb        = 10240
}
