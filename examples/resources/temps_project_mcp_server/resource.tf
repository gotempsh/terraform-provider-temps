resource "temps_project_mcp_server" "docs" {
  project_id = temps_project.web.id
  slug       = "docs"
  name       = "Docs search"
  config     = jsonencode({ url = "https://mcp.example.com/sse" })
}
