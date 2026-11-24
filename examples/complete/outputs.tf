# SPDX-FileCopyrightText: 2024-2026 Temps Contributors
# SPDX-License-Identifier: MIT OR Apache-2.0

output "project_id" {
  value = temps_project.web.id
}

output "production_url" {
  value = data.temps_environment.production.main_url
}

output "database_id" {
  value = temps_service.db.id
}

output "backup_schedule_id" {
  value = temps_backup_schedule.nightly.id
}

output "status_page_id" {
  value = temps_status_page.public.id
}

output "status_page_slug" {
  value = temps_status_page.public.slug
}

output "status_page_url" {
  description = "Empty until the Temps server has a public URL configured."
  value       = temps_status_page.public.public_url
}
