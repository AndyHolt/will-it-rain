"""Load the bundle for the model currently aliased as ``production``."""

from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Any

import joblib
from google.api_core.exceptions import NotFound
from google.cloud import aiplatform, storage


def load_champion_bundle(
    *,
    model_display_name: str,
    project: str,
    location: str,
    production_alias: str = "production",
) -> dict[str, Any] | None:
    """Return the joblib-loaded bundle for the @production version, or None.

    Returns None on the very first run when no production alias exists yet,
    so the caller can treat "no champion" as a valid state.
    """
    try:
        production = aiplatform.Model(
            model_name=f"{model_display_name}@{production_alias}",
            project=project,
            location=location,
        )
    except NotFound:
        return None

    uri = production.uri
    if uri is None or not uri.startswith("gs://"):
        return None

    bucket_name, _, prefix = uri.removeprefix("gs://").partition("/")
    bucket = storage.Client(project=project).bucket(bucket_name)
    with TemporaryDirectory() as tmp:
        for blob in bucket.list_blobs(prefix=prefix):
            if blob.name.endswith(".joblib"):
                local_path = Path(tmp) / Path(blob.name).name
                blob.download_to_filename(str(local_path))
                return joblib.load(local_path)
    return None
