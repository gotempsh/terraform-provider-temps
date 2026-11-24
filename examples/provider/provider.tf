terraform {
  required_providers {
    temps = {
      source = "gotempsh/temps"
    }
  }
}

# The endpoint and API key can also come from TEMPS_ENDPOINT and TEMPS_API_KEY.
provider "temps" {
  endpoint = "https://temps.example.com"
  api_key  = var.temps_api_key
}

variable "temps_api_key" {
  type      = string
  sensitive = true
}
