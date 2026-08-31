"""Cloud Function (gen 2) that rolls the backend Cloud Run services forward.

Subscribed to the ``model-promoted`` Pub/Sub topic via an Eventarc trigger.
Triggered when the training pipeline moves the ``@production`` alias to a new
model version.

The function bumps the ``MODEL_REFRESH_AT`` env var on each backend service,
which forces a new Cloud Run revision. The new revision re-resolves
``@production`` at startup and loads the freshly-promoted model.

``BACKEND_SERVICES`` is a comma-separated list because the Python→Go backend
migration ran two services side by side for the length of the blue/green
window, and both had to follow a promotion. It names one again. The plural
stays because the contract is a list: adding a service is an edit to
``model_refresh.tf``, not a change here.

We can't hot-reload the model inside the existing backend instances over
Pub/Sub: a push delivery hits exactly one instance, leaving any other warm
instances on the stale model. Forcing a revision drains all old instances.

Terraform owns each backend's static config, including the initial empty
``MODEL_REFRESH_AT``. The Cloud Run services have ``ignore_changes`` on the
env block so this drift doesn't fight terraform.
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


def _parse_services(raw: str) -> tuple[str, ...]:
    """Split the comma-separated BACKEND_SERVICES value into service names."""
    services = tuple(name.strip() for name in raw.split(",") if name.strip())
    if not services:
        raise ValueError("BACKEND_SERVICES must name at least one Cloud Run service.")
    return services


PROJECT = os.environ["PROJECT"]
REGION = os.environ["REGION"]
BACKEND_SERVICES = _parse_services(os.environ.get("BACKEND_SERVICES", "backend"))
REFRESH_ENV_VAR = "MODEL_REFRESH_AT"


def _service_path(service: str) -> str:
    return f"projects/{PROJECT}/locations/{REGION}/services/{service}"


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


def _refresh_service(client: run_v2.ServicesClient, service: str, now: str) -> None:
    """Bump MODEL_REFRESH_AT on one service and wait for the rollout to be accepted."""
    resource = _service_path(service)
    current = client.get_service(name=resource)
    _set_refresh_env(current, now)
    operation = client.update_service(service=current)
    operation.result()


@functions_framework.cloud_event
def refresh_backend(event: CloudEvent) -> None:
    payload = _decode_message(event)
    version_id = payload.get("version_id", "<unknown>")
    logger.info("Promotion event received for version %s.", version_id)

    client = run_v2.ServicesClient()
    # One timestamp for every service, so a promotion is legible as one event
    # across them rather than as a spread of near-identical times.
    now = datetime.now(tz=timezone.utc).isoformat()

    failed: list[str] = []
    for service in BACKEND_SERVICES:
        try:
            _refresh_service(client, service, now)
        except Exception:
            # Keep going: one service failing to roll forward shouldn't leave
            # the others stale. The raise below still hands the event back for
            # retry, which re-bumps the services that did succeed — an extra
            # revision, which is cheaper than a backend serving a stale model.
            logger.exception("Revision rollout failed for %s.", service)
            failed.append(service)
        else:
            logger.info(
                "Revision rollout requested for %s at %s for version %s.",
                service,
                now,
                version_id,
            )

    if failed:
        raise RuntimeError(f"Revision rollout failed for: {', '.join(failed)}")
