# ---------------------------------------------------------------------------
# backend — the prediction service. It now serves the Go image built from
# `backend-go/` rather than the Python one this file was written for.
#
# The repoint was an in-place update of this resource, not a new service, so the
# name, URL, service account, IAM and model-refresher wiring never changed.
# `backend-go`, the green service traffic crossed on, is gone; hosting rewrites
# here again and this is once more the only prediction service.
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
      # Derived in variables.tf, or pinned to a per-commit tag by CI.
      image = local.backend_image

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

      # REGION is the one piece of deployment config the service cannot
      # discover for itself: the ADC the metadata server hands it carries a
      # project but never a region. PROJECT is deliberately absent for the
      # opposite reason — internal/registry reads it only as an override, and
      # on Cloud Run ADC already answers it.
      #
      # The lifecycle block below ignores the whole env block, so an edit here
      # changes the declared state and nothing on the running service. Adding,
      # renaming or removing a variable needs a matching `gcloud run services
      # update backend --update-env-vars=... / --remove-env-vars=...`.
      env {
        name  = "REGION"
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
