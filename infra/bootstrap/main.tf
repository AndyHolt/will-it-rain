provider "google" {
  project = var.project_id
  region  = var.region
}

locals {
  state_bucket_name = "${var.project_id}-tfstate"
}

# ---------------------------------------------------------------------------
# Remote state bucket for main Terraform
# ---------------------------------------------------------------------------

resource "google_storage_bucket" "tf_state" {
  name     = local.state_bucket_name
  location = var.region

  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false

  versioning {
    enabled = true
  }

  lifecycle_rule {
    condition {
      num_newer_versions = 10
    }
    action {
      type = "Delete"
    }
  }

  lifecycle_rule {
    condition {
      days_since_noncurrent_time = 30
      with_state                 = "ARCHIVED"
    }
    action {
      type = "Delete"
    }
  }
}

# ---------------------------------------------------------------------------
# Service account used by main Terraform
# ---------------------------------------------------------------------------

resource "google_service_account" "terraform" {
  account_id   = var.tf_service_account_id
  display_name = "Terraform"
  description  = "Used by main Terraform to manage project resources."
}

# Give the SA read/write access to its own state bucket.
resource "google_storage_bucket_iam_member" "tf_state_admin" {
  bucket = google_storage_bucket.tf_state.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.terraform.email}"
}

# ---------------------------------------------------------------------------
# Workload Identity Federation for GitHub Actions
# ---------------------------------------------------------------------------

resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = var.wif_pool_id
  display_name              = "GitHub Actions"
  description               = "Pool for GitHub Actions OIDC federation."
}

resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = var.wif_provider_id
  display_name                       = "GitHub"

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }

  attribute_mapping = {
    "google.subject"             = "assertion.sub"
    "attribute.actor"            = "assertion.actor"
    "attribute.repository"       = "assertion.repository"
    "attribute.repository_owner" = "assertion.repository_owner"
    "attribute.ref"              = "assertion.ref"
  }

  # Only tokens from the configured repository may use this provider.
  attribute_condition = "assertion.repository == \"${var.github_repository}\""
}

# Allow workflows in the configured repo to impersonate the Terraform SA.
resource "google_service_account_iam_member" "github_wif_user" {
  service_account_id = google_service_account.terraform.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository/${var.github_repository}"
}
