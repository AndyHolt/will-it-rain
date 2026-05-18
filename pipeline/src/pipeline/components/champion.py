"""Load the bundle for the model currently aliased as ``production``."""

import logging
from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Any

import joblib
from google.api_core.exceptions import NotFound
from google.cloud import aiplatform, storage

logger = logging.getLogger(__name__)


def load_champion_bundle(
    *,
    model_display_name: str,
    project: str,
    location: str,
    production_alias: str = "production",
) -> dict[str, Any] | None:
    """Return the joblib-loaded bundle for the @production version, or None.

    Returns None on the very first run when no production alias exists yet,
    so the caller can treat "no champion" as a valid state. All "should have
    found something but didn't" paths log at WARNING so they can be told apart
    from the legitimate first-run case (logged at INFO).
    """
    # `aiplatform.Model("<display_name>@<alias>")` does NOT work — the SDK
    # treats the part before @ as a resource ID, not a display name. So we
    # resolve display_name → resource_name first, then look up the alias
    # against the full resource name.
    parents = aiplatform.Model.list(
        filter=f'display_name="{model_display_name}"',
        project=project,
        location=location,
    )
    if not parents:
        logger.info(
            "No model registered under display_name=%r — treating as first run.",
            model_display_name,
        )
        return None
    parent_resource = parents[0].resource_name
    try:
        production = aiplatform.Model(
            model_name=f"{parent_resource}@{production_alias}",
            project=project,
            location=location,
        )
    except NotFound:
        logger.warning(
            "Parent model %s exists but has no @%s alias — promote_op may not have run.",
            parent_resource,
            production_alias,
        )
        return None

    uri = production.uri
    if uri is None or not uri.startswith("gs://"):
        logger.warning(
            "Production version %s has unexpected artifact URI %r — cannot load bundle.",
            production.version_id,
            uri,
        )
        return None

    bucket_name, _, prefix = uri.removeprefix("gs://").partition("/")
    bucket = storage.Client(project=project).bucket(bucket_name)
    with TemporaryDirectory() as tmp:
        for blob in bucket.list_blobs(prefix=prefix):
            if blob.name.endswith(".joblib"):
                local_path = Path(tmp) / Path(blob.name).name
                blob.download_to_filename(str(local_path))
                logger.info(
                    "Loaded champion bundle from %s (version %s).",
                    uri,
                    production.version_id,
                )
                return joblib.load(local_path)
    logger.warning(
        "No .joblib blob found at %s for production version %s.",
        uri,
        production.version_id,
    )
    return None
