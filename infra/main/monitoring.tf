# ---------------------------------------------------------------------------
# Pipeline-run alert: email on every PipelineJob terminal state.
#
# Vertex AI emits a Cloud Logging entry on each PipelineJob state change.
# We match the three terminal states (SUCCEEDED / FAILED / CANCELLED) and
# fire one email per match. No pipeline-code wiring required; this works
# for any PipelineJob in the project, including manual one-off runs.
#
# The email body is constrained to Cloud Monitoring's alert template
# (state + resource label + run-UI link). For richer content (eval F1s,
# promotion outcome), we'd need a separate notify component publishing a
# structured log that this policy filters on instead — deferred until
# the basic version proves insufficient.
# ---------------------------------------------------------------------------

resource "google_monitoring_notification_channel" "pipeline_email" {
  display_name = "will-it-rain pipeline notifications"
  type         = "email"
  labels = {
    email_address = var.notification_email
  }

  depends_on = [google_project_service.main]
}

resource "google_monitoring_alert_policy" "pipeline_finished" {
  display_name = "will-it-rain pipeline finished"
  # One condition; combiner is required by the API but moot here.
  combiner = "OR"

  conditions {
    display_name = "PipelineJob reached a terminal state"

    condition_matched_log {
      filter = <<-EOT
        resource.type="aiplatform.googleapis.com/PipelineJob"
        (jsonPayload.state="PIPELINE_STATE_SUCCEEDED"
         OR jsonPayload.state="PIPELINE_STATE_FAILED"
         OR jsonPayload.state="PIPELINE_STATE_CANCELLED")
      EOT
    }
  }

  notification_channels = [google_monitoring_notification_channel.pipeline_email.id]

  alert_strategy {
    # Log-based alerts open an incident on first match and don't re-fire
    # until it closes. Runs are weekly; auto-closing after an hour means
    # an ad-hoc rerun the same day still gets its own email.
    auto_close = "3600s"
    notification_rate_limit {
      period = "300s"
    }
  }

  depends_on = [google_project_service.main]
}
