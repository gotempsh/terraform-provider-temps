resource "temps_project" "web" {
  name = "web"
}

# Drop events reported from local development machines.
resource "temps_error_inbound_filter" "localhost" {
  project_id = temps_project.web.id
  kind       = "localhost"
}

# Drop a known, harmless browser error.
resource "temps_error_inbound_filter" "resize_observer" {
  project_id = temps_project.web.id
  kind       = "error_message"
  pattern    = "*ResizeObserver loop*"
}
