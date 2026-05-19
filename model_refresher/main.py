"""Cloud Function (gen 2) that rolls the backend Cloud Run service forward.

Subscribed to the ``model-promoted`` Pub/Sub topic via an Eventarc trigger.
Triggered when the training pipeline moves the ``@production`` alias to a new
model version.

The function bumps the ``MODEL_REFRESH_AT`` env var on the backend service,
which forces a new Cloud Run revision. The new revision re-resolves
``@production`` in its FastAPI startup and loads the freshly-promoted model.

We can't hot-reload the model inside the existing backend instances over
Pub/Sub: a push delivery hits exactly one instance, leaving any other warm
instances on the stale model. Forcing a revision drains all old instances.

Terraform owns the backend's static config, including the initial empty
``MODEL_REFRESH_AT``. ``cloud_run.tf`` has ``ignore_changes`` on the env
block so this drift doesn't fight terraform.
"""

import base64
import json
import logging
import os
from datetime import datetime, timezone

import functions_framework
from cloudevents.http import CloudEvent
from google.cloud import run_v2

logger = logging.getLogger(__name__)

PROJECT = os.environ["PROJECT"]
LOCATION = os.environ["LOCATION"]
BACKEND_SERVICE = os.environ.get("BACKEND_SERVICE", "backend")
REFRESH_ENV_VAR = "MODEL_REFRESH_AT"


def _service_name() -> str:
    return f"projects/{PROJECT}/locations/{LOCATION}/services/{BACKEND_SERVICE}"


def _decode_message(event: CloudEvent) -> dict:
    """Pull the JSON payload out of the Pub/Sub-wrapped CloudEvent."""
    encoded = event.data["message"]["data"]
    return json.loads(base64.b64decode(encoded).decode("utf-8"))


def _set_refresh_env(service: run_v2.Service, value: str) -> None:
    """Set or replace MODEL_REFRESH_AT in the service template's first container."""
    container = service.template.containers[0]
    for env in container.env:
        if env.name == REFRESH_ENV_VAR:
            env.value = value
            return
    container.env.append(run_v2.EnvVar(name=REFRESH_ENV_VAR, value=value))


@functions_framework.cloud_event
def refresh_backend(event: CloudEvent) -> None:
    payload = _decode_message(event)
    version_id = payload.get("version_id", "<unknown>")
    logger.info("Promotion event received for version %s.", version_id)

    client = run_v2.ServicesClient()
    service = client.get_service(name=_service_name())

    now = datetime.now(tz=timezone.utc).isoformat()
    _set_refresh_env(service, now)

    operation = client.update_service(service=service)
    operation.result()
    logger.info("Backend revision rollout requested at %s for version %s.", now, version_id)
