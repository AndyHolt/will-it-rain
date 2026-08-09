"""Upload the trained bundle to GCS and register it in the Vertex Model Registry."""

from datetime import datetime, timezone
from pathlib import Path

from google.cloud import aiplatform, storage

from pipeline.components.evaluate import EvaluationResult
from pipeline.components.train import SERVING_METADATA_FILENAME, SERVING_MODEL_FILENAME

DEFAULT_MODEL_DISPLAY_NAME = "will-it-rain"

# Pre-built Vertex prediction container. We never serve from it (backend uses
# its own Cloud Run container) — but Model.upload requires a serving container
# URI, and the sklearn one is the closest match for a joblib LightGBM bundle.
DEFAULT_SERVING_CONTAINER = "us-docker.pkg.dev/vertex-ai/prediction/sklearn-cpu.1-5:latest"


def _build_description(evaluation: EvaluationResult) -> str:
    """Human-readable summary of how this version was evaluated."""
    lines = [
        f"Challenger F1:               {evaluation.challenger.f1:.3f}",
        f"Persistence baseline F1:     {evaluation.baselines.persistence.f1:.3f}",
        f"Precipitation baseline F1:   {evaluation.baselines.precipitation_threshold.f1:.3f}",
    ]
    if evaluation.champion is not None:
        lines.append(f"Champion F1 (same test set): {evaluation.champion.f1:.3f}")
    lines.append(
        f"Test window: {evaluation.test_start.date()} to {evaluation.test_end.date()} "
        f"({evaluation.n_test_rows} rows)"
    )
    return "\n".join(lines)


def _resolve_parent_model(model_display_name: str, project: str, location: str) -> str | None:
    """Return the resource name of the existing parent model, or None on first run."""
    existing = aiplatform.Model.list(
        filter=f'display_name="{model_display_name}"',
        project=project,
        location=location,
    )
    return existing[0].resource_name if existing else None


def register(
    bundle_path: str | Path,
    serving_dir: str | Path,
    evaluation: EvaluationResult,
    *,
    project: str,
    location: str,
    artefacts_bucket: str,
    model_display_name: str = DEFAULT_MODEL_DISPLAY_NAME,
    serving_container_image_uri: str = DEFAULT_SERVING_CONTAINER,
) -> aiplatform.Model:
    """Upload the bundle to GCS and register it as a new Model Registry version.

    On the very first run no parent model exists; ``parent_model`` is omitted
    and the upload creates the parent. On subsequent runs the parent is
    resolved by display name and the upload registers a new version under it.

    Returns the freshly-registered ``aiplatform.Model`` (a specific version).
    """
    aiplatform.init(project=project, location=location)

    # Vertex's sklearn prediction container requires the model directory to
    # contain exactly one of `model.pkl` or `model.joblib`. The KFP Output[Model]
    # artifact has no extension (`bundle`), so we rename on upload — the bundle
    # is a joblib dump, hence `.joblib`.
    bundle_path = Path(bundle_path)
    serving_dir = Path(serving_dir)
    timestamp = datetime.now(tz=timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    prefix = f"models/{timestamp}"
    artifact_uri = f"gs://{artefacts_bucket}/{prefix}"

    # The serving artefacts go in the same prefix as the bundle, because that
    # prefix *is* the Vertex `artifact_uri` — so resolving @production yields
    # the location of both. The sklearn container tolerates the extra files:
    # it looks for `model.joblib` by name, and we never serve from it anyway.
    bucket = storage.Client(project=project).bucket(artefacts_bucket)
    uploads = {
        f"{prefix}/model.joblib": bundle_path,
        f"{prefix}/{SERVING_MODEL_FILENAME}": serving_dir / SERVING_MODEL_FILENAME,
        f"{prefix}/{SERVING_METADATA_FILENAME}": serving_dir / SERVING_METADATA_FILENAME,
    }
    for blob_path, source in uploads.items():
        bucket.blob(blob_path).upload_from_filename(str(source))

    parent_model = _resolve_parent_model(model_display_name, project, location)
    return aiplatform.Model.upload(
        display_name=model_display_name,
        artifact_uri=artifact_uri,
        serving_container_image_uri=serving_container_image_uri,
        parent_model=parent_model,
        description=_build_description(evaluation),
    )
