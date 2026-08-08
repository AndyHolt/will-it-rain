"""GCP resource names derived from the declared project config.

Build- and deploy-time only. Every name here is a pure function of
``PROJECT_ID`` / ``REGION``, which are declared in the repo-root
``config.env`` — read directly from that file, so entry points work whether
they're invoked through ``make`` or by hand. Environment variables still take
precedence, which is how CI (where the values are loaded into the job
environment rather than the working tree) supplies them.

The file path is relative, so this resolves against the current working
directory — as the other settings classes in this repo already do. Run from
the repo root.

There are deliberately no defaults: without the config this raises rather
than silently pointing at the wrong project. That also makes it unusable from
serving code, which is intended — Cloud Run and the Vertex workers resolve
their project from ADC and have neither ``config.env`` nor ``PROJECT_ID``.
The only importers are ``pipeline.kfp_components`` and ``pipeline.trigger``,
neither of which runs inside a deployed container.
"""

from pydantic import Field, ValidationError
from pydantic_settings import BaseSettings, SettingsConfigDict


class _ProjectConfig(BaseSettings):
    # See pipeline/trigger.py for why required fields use `Field(...)`.
    PROJECT_ID: str = Field(...)
    REGION: str = Field(...)

    # `extra="ignore"` because config.env carries keys for other consumers
    # (Terraform, Firebase) that are none of this module's business.
    model_config = SettingsConfigDict(env_file="config.env", extra="ignore")


try:
    _config = _ProjectConfig()
except ValidationError as exc:
    # pydantic names the missing field but not where it should have come
    # from, and "which file, and am I in the right directory" is the whole
    # question when this fails.
    raise RuntimeError(
        "Could not resolve the GCP project config. PROJECT_ID and REGION are "
        "declared in config.env at the repo root; run from there, or export "
        f"them. Underlying error:\n{exc}"
    ) from exc

PROJECT_ID = _config.PROJECT_ID
REGION = _config.REGION

ARTEFACTS_BUCKET = f"{PROJECT_ID}-model-artefacts"
PIPELINE_SERVICE_ACCOUNT = f"pipeline@{PROJECT_ID}.iam.gserviceaccount.com"
IMAGE_REPO = f"{REGION}-docker.pkg.dev/{PROJECT_ID}/will-it-rain-images"
