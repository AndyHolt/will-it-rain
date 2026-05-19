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

# Allow the SA to use the project as a quota/billing project for API requests
# (required by `user_project_override = true` in the main provider config).
resource "google_project_iam_member" "tf_service_usage_consumer" {
  project = var.project_id
  role    = "roles/serviceusage.serviceUsageConsumer"
  member  = "serviceAccount:${google_service_account.terraform.email}"
}

# Project-level roles the Terraform SA needs in order to:
#   (a) manage every resource under infra/main/ via `terraform apply`, and
#   (b) push images / upload pipeline spec / deploy Cloud Run revisions /
#       publish Firebase Hosting from the CI deploy workflow that
#       impersonates this SA via WIF.
#
# Granted here in bootstrap rather than in infra/main/ to avoid the
# chicken-and-egg of needing the role in order to apply the file that
# grants it. Bootstrap is applied manually as the human user, infrequently.
locals {
  tf_sa_project_roles = [
    "roles/aiplatform.admin",                # Vertex pipelines, Model Registry
    "roles/artifactregistry.admin",          # AR repo + push images
    "roles/cloudscheduler.admin",            # weekly training schedule
    "roles/firebase.admin",                  # Firebase project enrolment + Hosting deploy
    "roles/iam.serviceAccountAdmin",         # create pipeline/backend/scheduler SAs
    "roles/iam.serviceAccountUser",          # act-as backend SA when deploying Cloud Run revisions
    "roles/monitoring.admin",                # pipeline-finished alert policy
    "roles/resourcemanager.projectIamAdmin", # project IAM bindings for the SAs above
    "roles/run.admin",                       # Cloud Run service + revisions
    "roles/serviceusage.serviceUsageAdmin",  # enable APIs in infra/main/apis.tf
    "roles/storage.admin",                   # artefacts bucket + upload pipeline spec
  ]
}

resource "google_project_iam_member" "terraform_sa" {
  for_each = toset(local.tf_sa_project_roles)

  project = var.project_id
  role    = each.value
  member  = "serviceAccount:${google_service_account.terraform.email}"
}

# Billing-account-level read access for the SA. `google_billing_budget` in
# infra/main/ needs `billing.budgets.get` to refresh, which lives on the
# billing account, not the project — so it can't be a project IAM binding.
# Whoever applies bootstrap needs billing-account admin to grant this.
data "google_project" "this" {
  project_id = var.project_id

  depends_on = [google_project_service.bootstrap]
}

resource "google_billing_account_iam_member" "tf_billing_viewer" {
  billing_account_id = data.google_project.this.billing_account
  role               = "roles/billing.viewer"
  member             = "serviceAccount:${google_service_account.terraform.email}"
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
