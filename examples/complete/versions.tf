# SPDX-FileCopyrightText: 2024-2026 Temps Contributors
# SPDX-License-Identifier: MIT OR Apache-2.0

terraform {
  required_version = ">= 1.11"
  required_providers {
    temps = {
      source = "gotempsh/temps"
    }
  }
}

# Reads TEMPS_ENDPOINT and TEMPS_API_KEY from the environment.
provider "temps" {}
