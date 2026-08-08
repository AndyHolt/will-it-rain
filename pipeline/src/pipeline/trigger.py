"""Submit a one-off run of the training pipeline to Vertex AI.

Manual trigger for now — scheduling will be wired up later (likely via Cloud
Scheduler → Cloud Run Job calling this same SDK path). Using the aiplatform
SDK rather than a raw HTTP call to pipelineJobs.create because the SDK
correctly inlines the compiled KFP 2.x spec; Vertex's own templateUri code
path mis-parses it.

Run from the repo root via ``make trigger-pipeline-from-local``, which exports
the config.env values this module's ``will_it_rain_shared.gcp`` import needs.
Submitting a training run to whichever project gcloud happens to point at is
not a useful default, so the project is declared rather than discovered.

Private location/site values come from .env (see .env-example). Auth uses
your local ADC; the submitted job runs as the `pipeline` SA, so ADC needs
``roles/iam.serviceAccountUser`` on that SA.
"""

from google.cloud import aiplatform
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

from will_it_rain_shared.gcp import (
    ARTEFACTS_BUCKET,
    PIPELINE_SERVICE_ACCOUNT,
    PROJECT_ID,
    REGION,
)


class Settings(BaseSettings):
    # `Field(...)` is pydantic's "required, no default" sentinel: at runtime
    # pydantic still raises ValidationError if the value is missing from env
    # / .env, but the explicit assignment on the class attribute stops static
    # type checkers (ty, mypy, pyright without the pydantic plugin) from
    # flagging `Settings()` as missing required arguments. Without this they
    # can't see that BaseSettings populates these fields at construction time
    # from external sources. See:
    # https://github.com/pydantic/pydantic-settings/issues/201#issuecomment-2495858009
    LATITUDE: float = Field(...)
    LONGITUDE: float = Field(...)
    COSMOS_UK_SITE_CODE: str = Field(...)

    PIPELINE_TEMPLATE: str = "build/pipeline.yaml"
    TRAINING_WINDOW_START_DATE: str = "2022-03-01"
    MODEL_DISPLAY_NAME: str = "will-it-rain"

    model_config = SettingsConfigDict(env_file=".env", extra="ignore")


def main() -> None:
    s = Settings()
    aiplatform.init(project=PROJECT_ID, location=REGION)

    job = aiplatform.PipelineJob(
        display_name="will-it-rain-train",
        template_path=s.PIPELINE_TEMPLATE,
        pipeline_root=f"gs://{ARTEFACTS_BUCKET}/pipeline-runs",
        parameter_values={
            "latitude": s.LATITUDE,
            "longitude": s.LONGITUDE,
            "site_code": s.COSMOS_UK_SITE_CODE,
            "training_window_start_date": s.TRAINING_WINDOW_START_DATE,
            "project": PROJECT_ID,
            "location": REGION,
            "artefacts_bucket": ARTEFACTS_BUCKET,
            "model_display_name": s.MODEL_DISPLAY_NAME,
        },
        enable_caching=False,
    )
    job.submit(service_account=PIPELINE_SERVICE_ACCOUNT)
    print(f"Submitted: {job.resource_name}")


if __name__ == "__main__":
    main()
