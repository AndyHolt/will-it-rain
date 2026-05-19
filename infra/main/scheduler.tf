# ---------------------------------------------------------------------------
# Weekly training schedule
# ---------------------------------------------------------------------------
#
# Cloud Scheduler fires once a week, hitting Vertex's pipelineJobs.create
# REST endpoint directly. The pipeline spec is inlined into the request body
# (Vertex's `templateUri` path mis-parses KFP 2.x YAML, so we can't use it).
# The YAML itself lives in GCS, uploaded by `make upload-pipeline`; this file
# reads it at plan time via the storage data source and embeds the decoded
# struct into the scheduler job. So the dev flow is two steps:
#
#     make upload-pipeline        # new spec → GCS
#     terraform apply             # picks up GCS content, updates scheduler
#
# In CI those steps are adjacent in the same workflow, so this is invisible.
# Without the apply, the previously-applied body keeps firing — the GCS
# object is a staging point, not a live source.

data "google_storage_bucket_object_content" "pipeline_spec" {
  bucket = google_storage_bucket.model_artefacts.name
  name   = "pipelines/will-it-rain.yaml"
}

locals {
  pipeline_job_body = {
    displayName  = "will-it-rain-train-scheduled"
    pipelineSpec = yamldecode(data.google_storage_bucket_object_content.pipeline_spec.content)
    runtimeConfig = {
      parameterValues = {
        latitude                   = var.weather_latitude
        longitude                  = var.weather_longitude
        site_code                  = var.cosmos_uk_site_code
        training_window_start_date = var.training_window_start_date
        project                    = var.project_id
        location                   = var.region
        artefacts_bucket           = google_storage_bucket.model_artefacts.name
        model_display_name         = var.model_display_name
      }
      gcsOutputDirectory = "gs://${google_storage_bucket.model_artefacts.name}/pipeline-runs"
    }
    serviceAccount = google_service_account.pipeline.email
  }
}

resource "google_cloud_scheduler_job" "weekly_training" {
  name        = "will-it-rain-weekly-training"
  description = "Submits the weekly will-it-rain training pipeline run."
  schedule    = "0 2 * * SUN"
  time_zone   = "Etc/UTC"
  region      = var.region

  retry_config {
    retry_count = 1
  }

  http_target {
    http_method = "POST"
    uri         = "https://${var.region}-aiplatform.googleapis.com/v1/projects/${var.project_id}/locations/${var.region}/pipelineJobs"
    body        = base64encode(jsonencode(local.pipeline_job_body))

    headers = {
      "Content-Type" = "application/json; charset=utf-8"
    }

    oauth_token {
      service_account_email = google_service_account.scheduler.email
      scope                 = "https://www.googleapis.com/auth/cloud-platform"
    }
  }

  depends_on = [google_project_service.main]
}

# ---------------------------------------------------------------------------
# IAM for the scheduler SA
# ---------------------------------------------------------------------------

# Scheduler SA needs to call pipelineJobs.create on Vertex.
resource "google_project_iam_member" "scheduler_aiplatform_user" {
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_service_account.scheduler.email}"
}

# …and act-as the pipeline SA, so the submitted PipelineJob runs as it
# rather than as the scheduler SA.
resource "google_service_account_iam_member" "scheduler_acts_as_pipeline" {
  service_account_id = google_service_account.pipeline.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.scheduler.email}"
}
