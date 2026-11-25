resource "temps_ai_provider" "openai" {
  provider_type = "openai"
  display_name  = "OpenAI"
  api_key       = var.openai_api_key
  default_model = "gpt-4o-mini"
}
