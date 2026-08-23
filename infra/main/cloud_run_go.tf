# ---------------------------------------------------------------------------
# backend-go — the Go rewrite of the backend, running green beside the blue
# Python `backend` service (cloud_run.tf) for the length of the migration.
# ---------------------------------------------------------------------------
#
# Nothing routes user traffic here: Firebase Hosting still rewrites to
# `backend`. The service exists so the Go image can be validated against live
# Vertex and GCS — parity of predictions, and the cold start the rewrite is
# for — before hosting is flipped.
#
# At teardown this file is deleted and `backend` is repointed at the Go image
# in place, so no service is ever destroyed and recreated.

resource "google_cloud_run_v2_service" "backend_go" {
  name                = "backend-go"
  location            = var.region
  deletion_protection = false

  template {
    # Reuses the blue service's SA rather than adding one: the permissions the
    # Go service needs (aiplatform.user, objectViewer on the model bucket) are
    # exactly the ones `backend` already has, and sharing means teardown
    # removes this service without touching the identity that outlives it.
    service_account                  = google_service_account.backend.email
    max_instance_request_concurrency = 80

    scaling {
      min_instance_count = 0
      max_instance_count = 3
    }

    containers {
      image = local.backend_go_image

      ports {
        container_port = 8080
      }

      resources {
        limits = {
          cpu = "1"
          # Half the Python service's 1Gi. There is no interpreter, no pandas
          # and no scipy here; the resident set is the binary, a 156 KiB model
          # and one forecast response.
          memory = "512Mi"
        }
        # Same reasoning as cloud_run.tf: request-based billing, and the
        # startup boost is free and covers the concurrent model + forecast
        # fetch the service does before it serves.
        cpu_idle          = true
        startup_cpu_boost = true
      }

      # PROJECT is deliberately not set. internal/registry resolves it from the
      # ADC the metadata server hands the service, so the binary carries no
      # project and runs unmodified in any of them. LOCATION has no such route
      # — ADC carries a project and never a location — so the region is
      # injected, as it is for the Python service.
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

      # Placeholder for the initial create; bumped out-of-band by the
      # model-refresher Cloud Function once backend-go joins BACKEND_SERVICES.
      env {
        name  = "MODEL_REFRESH_AT"
        value = ""
      }
    }
  }

  # Same reason as cloud_run.tf:84 — the refresher owns MODEL_REFRESH_AT, and
  # without this every apply would reset it and tear down the freshly-rolled
  # revision. Editing the env vars above therefore needs a follow-up
  # `gcloud run services update backend-go --update-env-vars=...`.
  lifecycle {
    ignore_changes = [template[0].containers[0].env]
  }

  depends_on = [google_project_service.main]
}

# Matches the blue service's posture: public, unauthenticated. Parity checks
# hit both URLs directly, and locking one down but not the other would make
# them measure different things.
resource "google_cloud_run_v2_service_iam_member" "backend_go_public" {
  name     = google_cloud_run_v2_service.backend_go.name
  location = google_cloud_run_v2_service.backend_go.location
  role     = "roles/run.invoker"
  member   = "allUsers"
}

output "backend_go_url" {
  value       = google_cloud_run_v2_service.backend_go.uri
  description = "Public URL of the backend-go Cloud Run service (migration only)."
}
