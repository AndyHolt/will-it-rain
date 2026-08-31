# ---------------------------------------------------------------------------
# backend — the prediction service. It now serves the Go image built from
# `backend-go/` rather than the Python one this file was written for.
#
# The repoint is an in-place update of this resource, not a new service, so the
# name, URL, service account, IAM and model-refresher wiring are all unchanged.
# Hosting still rewrites to `backend-go` at this point; it flips back here once
# this service has been validated directly, and `backend-go` is destroyed after
# that. Rollback until then is one line: `local.backend_image`.
# ---------------------------------------------------------------------------

resource "google_cloud_run_v2_service" "backend" {
  name                = "backend"
  location            = var.region
  deletion_protection = false

  template {
    service_account                  = google_service_account.backend.email
    max_instance_request_concurrency = 80

    scaling {
      # min=0: traffic is sporadic enough that a pinned warm instance was the
      # single biggest line on the bill. That used to cost a ~20s cold start on
      # the first request after an idle period, which is what the Go rewrite
      # was for — the measured startup is now ~0.45s (docs/cold-start.md), so
      # scale-to-zero no longer trades latency for cost.
      min_instance_count = 0
      max_instance_count = 3
    }

    containers {
      # The Go image. var.backend_image and the Python image it pointed at are
      # removed with the rest of the Python backend; until then this is the one
      # place that decides which of the two this service serves.
      image = local.backend_go_image

      ports {
        container_port = 8080
      }

      resources {
        limits = {
          cpu = "1"
          # Halved from the Python service's 1Gi along with the image. There is
          # no interpreter, no pandas and no scipy here; the resident set is the
          # binary, a 156 KiB model and one forecast response.
          memory = "512Mi"
        }
        # cpu_idle = true selects request-based billing: CPU and memory are
        # billed only while a request is in flight (plus startup), not for the
        # instance's whole life including the ~15min idle keep-alive. CPU is
        # throttled between requests, which is fine — the service does no
        # background work outside request handling.
        cpu_idle = true
        # Free during the startup window; doubles available CPU while the
        # concurrent champion fetch and forecast fetch run.
        startup_cpu_boost = true
      }

      # PROJECT is inert for the Go binary, which resolves the project from the
      # ADC the metadata server hands it (internal/registry) and only consults
      # this as an override. Left set rather than deleted because the lifecycle
      # block below means removing it here would not remove it from the running
      # service — that needs a `gcloud run services update --remove-env-vars`,
      # and it rides along with the LOCATION -> REGION rename that does the same.
      env {
        name  = "PROJECT"
        value = var.project_id
      }
      env {
        name  = "LOCATION"
        value = var.region
      }
      env {
        name  = "MODEL_DISPLAY_NAME"
        value = var.model_display_name
      }
      env {
        name  = "LATITUDE"
        value = var.weather_latitude
      }
      env {
        name  = "LONGITUDE"
        value = var.weather_longitude
      }

      # Bumped out-of-band by the model-refresher Cloud Function when the
      # @production alias moves (see model_refresh.tf). The value here is a
      # placeholder for the initial create; once the function has run, terraform
      # ignores changes (see lifecycle below) so the two don't fight.
      env {
        name  = "MODEL_REFRESH_AT"
        value = ""
      }
    }
  }

  # MODEL_REFRESH_AT is bumped by the model-refresher Cloud Function on every
  # alias move. Without this, every subsequent `terraform apply` would try to
  # reset it back to "" and tear down the freshly-rolled revision. Ignoring
  # the whole env block is overly broad — but per-env-var ignore is awkward in
  # the v2 schema (env is a list, not a map) and the env block is otherwise
  # short and rarely changes. Manual env-var updates need a code-side change
  # plus a follow-up `gcloud run services update --update-env-vars=...` (or
  # accept the next refresher run resetting them).
  lifecycle {
    ignore_changes = [template[0].containers[0].env]
  }

  depends_on = [google_project_service.main]
}

# Plan calls for fully-public access (no auth). The frontend will hit this
# via Firebase Hosting rewrites, but the URL itself is reachable directly.
# TODO once frontend deployed, lock down backend so only reachable via frontend
resource "google_cloud_run_v2_service_iam_member" "backend_public" {
  name     = google_cloud_run_v2_service.backend.name
  location = google_cloud_run_v2_service.backend.location
  role     = "roles/run.invoker"
  member   = "allUsers"
}

output "backend_url" {
  value       = google_cloud_run_v2_service.backend.uri
  description = "Public URL of the backend Cloud Run service."
}
