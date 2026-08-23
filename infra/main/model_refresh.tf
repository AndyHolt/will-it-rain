# ---------------------------------------------------------------------------
# Backend model refresh on promotion
# ---------------------------------------------------------------------------
#
# When the training pipeline moves the @production alias, the promote step
# publishes a message to the `model-promoted` Pub/Sub topic. A Cloud Function
# (gen 2) subscribed via Eventarc receives the event and bumps the
# MODEL_REFRESH_AT env var on each Cloud Run service named by
# BACKEND_SERVICES, forcing a new revision. The new revision re-resolves
# @production at startup and loads the fresh model.
#
# We can't reload in-process on the backend itself: Pub/Sub push hits exactly
# one instance, so any other warm instances would stay stale. Forcing a
# revision drains all old instances cleanly.
#
# Source-of-truth split mirrors the pipeline YAML (scheduler.tf): CI runs
# `make upload-model-refresher-source` to stage the zip at a fixed GCS path; this
# file reads its metadata via a data source and threads the object generation
# into the function's source ref, so a new upload produces a TF diff and the
# function rolls forward on the next apply. First-time bootstrap therefore
# needs a manual `make upload-model-refresher-source` before `terraform apply`.

# ---------------------------------------------------------------------------
# Topic + pipeline publisher access
# ---------------------------------------------------------------------------

resource "google_pubsub_topic" "model_promoted" {
  name = "model-promoted"

  depends_on = [google_project_service.main]
}

# Pipeline SA publishes from the publish_promotion_op component.
resource "google_pubsub_topic_iam_member" "pipeline_publisher" {
  topic  = google_pubsub_topic.model_promoted.name
  role   = "roles/pubsub.publisher"
  member = "serviceAccount:${google_service_account.pipeline.email}"
}

# ---------------------------------------------------------------------------
# Model-refresher source — staged in GCS by `make upload-model-refresher-source`
# ---------------------------------------------------------------------------
#
# The data source reads the object's current generation; threading it into
# the function's storage_source below ensures TF sees a diff when CI uploads
# a fresh zip, even though the object name is fixed.

data "google_storage_bucket_object" "model_refresher_source" {
  bucket = google_storage_bucket.model_artefacts.name
  name   = "functions/model_refresher.zip"
}

# ---------------------------------------------------------------------------
# Refresher SA — runs the function, allowed to roll the backend forward
# ---------------------------------------------------------------------------

resource "google_service_account" "refresher" {
  account_id   = "refresher"
  display_name = "Backend model refresher"
  description  = "Runtime SA for the model-promoted Cloud Function; rolls Cloud Run revisions."
}

# Update the backend service (creates new revisions).
resource "google_cloud_run_v2_service_iam_member" "refresher_backend_developer" {
  name     = google_cloud_run_v2_service.backend.name
  location = google_cloud_run_v2_service.backend.location
  role     = "roles/run.developer"
  member   = "serviceAccount:${google_service_account.refresher.email}"
}

# Act-as the backend SA, so the new revision keeps running as `backend` and
# not as `refresher`.
resource "google_service_account_iam_member" "refresher_acts_as_backend" {
  service_account_id = google_service_account.backend.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.refresher.email}"
}

# Eventarc trigger invokes the function via Cloud Run; the function's
# identity also receives the event, so it needs the receiver role.
resource "google_project_iam_member" "refresher_eventarc_receiver" {
  project = var.project_id
  role    = "roles/eventarc.eventReceiver"
  member  = "serviceAccount:${google_service_account.refresher.email}"
}

# ---------------------------------------------------------------------------
# Cloud Function (gen 2) — Pub/Sub-triggered via Eventarc
# ---------------------------------------------------------------------------

resource "google_cloudfunctions2_function" "model_refresher" {
  name        = "model-refresher"
  location    = var.region
  description = "Bumps MODEL_REFRESH_AT on the backend services when @production moves."

  build_config {
    # uv is the default dependency manager for python314+ in the Cloud
    # Functions buildpack; with pyproject.toml at the zip root and no
    # uv.lock alongside (the workspace lock isn't shippable), the buildpack
    # does a fresh uv-resolve of model-refresher's deps at build time.
    runtime     = "python314"
    entry_point = "refresh_backend"
    source {
      storage_source {
        bucket     = data.google_storage_bucket_object.model_refresher_source.bucket
        object     = data.google_storage_bucket_object.model_refresher_source.name
        generation = data.google_storage_bucket_object.model_refresher_source.generation
      }
    }
  }

  service_config {
    max_instance_count    = 1
    available_memory      = "256M"
    timeout_seconds       = 60
    service_account_email = google_service_account.refresher.email

    # BACKEND_SERVICES is comma-separated: the function refreshes every
    # service named. Both backends are listed for the length of the blue/green
    # migration, so a promotion reaches whichever one hosting points at, and
    # the green service is never validated against a model the blue one has
    # moved past. Teardown drops backend-go and leaves a single name.
    environment_variables = {
      PROJECT  = var.project_id
      LOCATION = var.region
      BACKEND_SERVICES = join(",", [
        google_cloud_run_v2_service.backend.name,
        google_cloud_run_v2_service.backend_go.name,
      ])
    }
  }

  event_trigger {
    trigger_region        = var.region
    event_type            = "google.cloud.pubsub.topic.v1.messagePublished"
    pubsub_topic          = google_pubsub_topic.model_promoted.id
    service_account_email = google_service_account.refresher.email
    retry_policy          = "RETRY_POLICY_RETRY"
  }

  depends_on = [google_project_service.main]
}
