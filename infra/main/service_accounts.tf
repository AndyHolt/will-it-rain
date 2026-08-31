# ---------------------------------------------------------------------------
# Vertex AI Pipeline runtime SA
# ---------------------------------------------------------------------------

resource "google_service_account" "pipeline" {
  account_id   = "pipeline"
  display_name = "Vertex AI Pipeline"
  description  = "Runtime SA for the training pipeline; reads/writes model artefacts."
}

resource "google_project_iam_member" "pipeline_aiplatform_user" {
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_service_account.pipeline.email}"
}

resource "google_project_iam_member" "pipeline_artifactregistry_reader" {
  project = var.project_id
  role    = "roles/artifactregistry.reader"
  member  = "serviceAccount:${google_service_account.pipeline.email}"
}

resource "google_project_iam_member" "pipeline_service_usage_consumer" {
  project = var.project_id
  role    = "roles/serviceusage.serviceUsageConsumer"
  member  = "serviceAccount:${google_service_account.pipeline.email}"
}

resource "google_storage_bucket_iam_member" "pipeline_artefacts_admin" {
  bucket = google_storage_bucket.model_artefacts.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.pipeline.email}"
}

# Vertex worker containers ship their stdout/stderr to Cloud Logging as the
# job's SA, not as the Vertex service agent. Without this, a failing component
# is undebuggable: the agent's own "Job is running" / "exited with a non-zero
# status" messages still land, so the logs *look* present, but every line the
# component printed — including the traceback — is dropped silently.
resource "google_project_iam_member" "pipeline_log_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.pipeline.email}"
}

# ---------------------------------------------------------------------------
# Cloud Run backend SA
# ---------------------------------------------------------------------------

resource "google_service_account" "backend" {
  account_id   = "backend"
  display_name = "Cloud Run backend"
  description  = "Runtime SA for the backend; reads model registry + artefacts."
}

resource "google_project_iam_member" "backend_aiplatform_user" {
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_service_account.backend.email}"
}

resource "google_storage_bucket_iam_member" "backend_artefacts_viewer" {
  bucket = google_storage_bucket.model_artefacts.name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.backend.email}"
}

# ---------------------------------------------------------------------------
# Cloud Scheduler SA (invokes the pipeline trigger function/service)
# ---------------------------------------------------------------------------

resource "google_service_account" "scheduler" {
  account_id   = "scheduler"
  display_name = "Cloud Scheduler"
  description  = "Used by Cloud Scheduler to invoke the pipeline trigger."
}

# Invoker role on the trigger target is added when the trigger is created in
# Stage 2; nothing to grant on this SA at the project level yet.
