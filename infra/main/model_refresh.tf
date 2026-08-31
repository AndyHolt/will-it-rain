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

# ...and receiving the event is only half of it. Eventarc delivers *as*
# `refresher`, and a gen 2 function is invoked through the Cloud Run service
# underneath it, so the same SA needs run.invoker on that service. Without
# this the delivery 403s with "The IAM principal lacks {run.routes.invoke}"
# and Pub/Sub retries until the message ages out: the alias moves, the
# function never runs, and the backend serves the old model with nothing
# failing loudly. The backing service takes the function's name.
resource "google_cloud_run_v2_service_iam_member" "refresher_invoker" {
  name     = google_cloudfunctions2_function.model_refresher.name
  location = var.region
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.refresher.email}"
}

# Read the image the rolled revision will run. Surprising, because nothing
# here pulls it: the Cloud Run service agent does that at runtime, as itself.
# But `services.update` validates the image against the *calling* principal
# before it will create a revision, so without this the API call 403s on
# `artifactregistry.repositories.downloadArtifacts` and no revision is
# created at all — the same silent outcome as the invoker role above, with
# the alias moved and the backend still serving the old model.
#
# Scoped to the one repository rather than the project (which is how the
# pipeline SA holds the role, in service_accounts.tf): every image the
# refresher ever rolls is in here.
resource "google_artifact_registry_repository_iam_member" "refresher_image_reader" {
  project    = google_artifact_registry_repository.images.project
  location   = google_artifact_registry_repository.images.location
  repository = google_artifact_registry_repository.images.name
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:${google_service_account.refresher.email}"
}

# Run viewer required to check status after rollout
resource "google_project_iam_member" "refresher_run_viewer" {
  project = var.project_id
  role    = "roles/run.viewer"
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
    max_instance_count = 1
    available_memory   = "256M"

    # The function waits for the rollout to *complete*, not merely to be
    # accepted, so its runtime is the service's startup time. That is ~0.45s of
    # container start now `backend` serves the Go image (docs/cold-start.md),
    # but a revision rollout is dominated by Cloud Run's own admission and
    # health-check path, not by the binary. 300s is headroom for a slow image
    # pull, and is left where the two-service migration set it.
    timeout_seconds       = 300
    service_account_email = google_service_account.refresher.email

    # BACKEND_SERVICES is comma-separated: the function refreshes every
    # service named. The blue/green migration listed both backends here; with
    # the green service destroyed there is one again. The plural stays because
    # the function's contract is a list — adding a second service is an edit
    # here, not a change to the function.
    environment_variables = {
      PROJECT          = var.project_id
      REGION           = var.region
      BACKEND_SERVICES = google_cloud_run_v2_service.backend.name
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
