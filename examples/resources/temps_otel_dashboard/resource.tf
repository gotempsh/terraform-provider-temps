resource "temps_otel_dashboard" "service" {
  project_id = temps_project.web.id
  name       = "Service health"
  layout = jsonencode({
    sections = [{
      id    = "http"
      title = "HTTP"
      tiles = [{
        id          = "latency"
        title       = "p95 latency"
        metric_name = "http.server.duration"
        aggregation = "p95"
      }]
    }]
  })
}
