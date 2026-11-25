# Let the worker project call the API project over the internal network.
resource "temps_project_internal_access" "worker_to_api" {
  project_id        = temps_project.api.id
  caller_project_id = temps_project.worker.id
  description       = "Background jobs call the API"
}
