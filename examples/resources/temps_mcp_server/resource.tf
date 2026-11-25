resource "temps_mcp_server" "docs" {
  slug   = "docs"
  name   = "Docs search"
  config = jsonencode({ url = "https://mcp.example.com/sse" })
}
