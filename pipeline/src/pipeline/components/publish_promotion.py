"""Publish a Pub/Sub event announcing that the @production alias has moved.

Downstream, a Cloud Function subscribed to this topic bumps an env var on the
backend Cloud Run service, forcing a new revision. The new revision re-resolves
@production at startup and loads the freshly-promoted model.

Only invoked from the DAG when ``promote_op`` returns True (i.e. the alias was
actually moved). The version_id is included in the payload so the receiver can
short-circuit duplicate Pub/Sub deliveries.
"""

import json
from datetime import datetime, timezone

from google.cloud import pubsub_v1

DEFAULT_TOPIC_ID = "model-promoted"


def publish_promotion(
    *,
    project: str,
    model_display_name: str,
    version_id: str,
    resource_name: str,
    topic_id: str = DEFAULT_TOPIC_ID,
) -> str:
    """Publish a single message announcing the new production version.

    Returns the published message ID (useful for log inspection).
    """
    publisher = pubsub_v1.PublisherClient()
    topic_path = publisher.topic_path(project, topic_id)

    payload = {
        "model_display_name": model_display_name,
        "version_id": version_id,
        "resource_name": resource_name,
        "promoted_at": datetime.now(tz=timezone.utc).isoformat(),
    }
    future = publisher.publish(topic_path, json.dumps(payload).encode("utf-8"))
    return future.result()
