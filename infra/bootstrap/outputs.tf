output "state_bucket" {
  description = "Name of the GCS bucket for main Terraform state. Use in the main module's backend config."
  value       = google_storage_bucket.tf_state.name
}

output "terraform_service_account_email" {
  description = "Email of the service account that main Terraform impersonates."
  value       = google_service_account.terraform.email
}

output "workload_identity_provider" {
  description = "Full resource name of the WIF provider. Pass to google-github-actions/auth as `workload_identity_provider`."
  value       = google_iam_workload_identity_pool_provider.github.name
}
