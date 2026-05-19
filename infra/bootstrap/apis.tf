locals {
  bootstrap_apis = [
    "cloudresourcemanager.googleapis.com", # data.google_project, project metadata
    "serviceusage.googleapis.com",         # required to enable other APIs
    "iam.googleapis.com",                  # service accounts, IAM
    "iamcredentials.googleapis.com",       # SA impersonation, WIF token minting
    "sts.googleapis.com",                  # WIF token exchange
    "storage.googleapis.com",              # state bucket
    "cloudbilling.googleapis.com",         # billing-account IAM binding for TF SA
  ]
}

resource "google_project_service" "bootstrap" {
  for_each = toset(local.bootstrap_apis)

  service            = each.value
  disable_on_destroy = false
}
