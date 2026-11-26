resource "temps_notification_provider_telegram" "ops" {
  name              = "Ops group"
  bot_token         = var.telegram_bot_token
  chat_id           = "-1001234567890"
  message_thread_id = 42
}
