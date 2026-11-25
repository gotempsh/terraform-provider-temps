resource "temps_ai_provider_model" "fine_tuned" {
  provider_id = temps_ai_provider.openai.id
  model_id    = "ft:gpt-4o-mini:support"
  is_enabled  = true
}
