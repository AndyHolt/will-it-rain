locals {
  main_apis = [
    "cloudbilling.googleapis.com",   # read billing account info
    "billingbudgets.googleapis.com", # budget resource
  ]
}

resource "google_project_service" "main" {
  for_each = toset(local.main_apis)

  service            = each.value
  disable_on_destroy = false
}
