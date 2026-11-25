resource "temps_workload_volume_attachment" "data" {
  environment_id = temps_environment.production.id
  volume_id      = temps_workload_volume.data.volume_id
  mount_path     = "/var/lib/app"
}
