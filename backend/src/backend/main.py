"""FastAPI app: serves predictions from the @production model."""

import logging
import sys
from contextlib import asynccontextmanager
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Annotated

from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel, Field
from pydantic_settings import BaseSettings, SettingsConfigDict

from backend.forecast_fetch import fetch_live_forecast, pick_anchor
from will_it_rain_shared.champion import load_champion
from will_it_rain_shared.predict import PREDICTION_WINDOW_HOURS, TrainedModel, predict_from_model

# Uvicorn only configures its own loggers; the root logger is left bare, so
# app-module INFO logs would otherwise be dropped by Python's lastResort.
# Stream to stdout so Cloud Run maps these as DEFAULT severity rather than
# ERROR (its stderr default).
logging.basicConfig(
    level=logging.INFO,
    stream=sys.stdout,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
)

logger = logging.getLogger(__name__)


class Settings(BaseSettings):
    # See pipeline/trigger.py for why required fields use `Field(...)`.
    LATITUDE: float = Field(...)
    LONGITUDE: float = Field(...)

    PROJECT: str = "will-it-rain-496215"
    LOCATION: str = "europe-west2"
    MODEL_DISPLAY_NAME: str = "will-it-rain"

    model_config = SettingsConfigDict(env_file=".env", extra="ignore")


class HealthResponse(BaseModel):
    status: str
    model_version: str | None
    model_resource: str | None
    loaded_at_utc: datetime | None

    # Same rationale as TrainedModel in shared/predict.py: pydantic reserves
    # the `model_*` prefix by default, which would warn on the two fields
    # above.
    model_config = {"protected_namespaces": ()}


class PredictResponse(BaseModel):
    anchor_utc: datetime
    window_end_utc: datetime
    raw_prob: float
    calibrated_prob: float
    threshold: float
    will_rain: bool
    model_version: str

    model_config = {"protected_namespaces": ()}


@dataclass(frozen=True)
class CurrentModel:
    """The model currently being served and when this process loaded it.

    Flattens shared.champion.Champion (which carries pipeline-side
    champion-vs-challenger semantics) into the serving-side view: the
    trained model itself plus the registry metadata the /health endpoint
    surfaces.
    """

    trained_model: TrainedModel
    version_id: str
    resource_name: str
    loaded_at: datetime


# Module-scope state, written exactly once by the lifespan and read via the
# Depends providers below. Tests should override the providers via
# `app.dependency_overrides[...]` rather than reassigning these.
_current: CurrentModel | None = None
_settings: Settings | None = None


@asynccontextmanager
async def lifespan(app: FastAPI):
    global _current, _settings
    _settings = Settings()
    champion = load_champion(
        model_display_name=_settings.MODEL_DISPLAY_NAME,
        project=_settings.PROJECT,
        location=_settings.LOCATION,
    )
    if champion is None:
        logger.warning("No @production model found at startup — /api/predict will 503.")
    else:
        _current = CurrentModel(
            trained_model=champion.trained_model,
            version_id=champion.version_id,
            resource_name=champion.resource_name,
            loaded_at=datetime.now(tz=timezone.utc),
        )
        logger.info("Loaded model version %s at %s.", _current.version_id, _current.loaded_at)
    yield


def get_settings() -> Settings:
    # Lifespan always assigns _settings before requests are served; the
    # assert narrows the type for downstream handlers.
    assert _settings is not None, "Settings accessed before lifespan startup completed."
    return _settings


def get_current_model() -> CurrentModel:
    if _current is None:
        raise HTTPException(status_code=503, detail="Model not loaded.")
    return _current


SettingsDep = Annotated[Settings, Depends(get_settings)]
CurrentModelDep = Annotated[CurrentModel, Depends(get_current_model)]


app = FastAPI(title="will-it-rain", lifespan=lifespan)


@app.get("/api/health")
def health() -> HealthResponse:
    # Health reads module state directly rather than via Depends: it must
    # report the "no model loaded" state without 503'ing, which is what
    # get_current_model is for.
    return HealthResponse(
        status="ok",
        model_version=_current.version_id if _current else None,
        model_resource=_current.resource_name if _current else None,
        loaded_at_utc=_current.loaded_at if _current else None,
    )


@app.get("/api/predict")
def predict(current: CurrentModelDep, settings: SettingsDep) -> PredictResponse:
    forecast = fetch_live_forecast(settings.LATITUDE, settings.LONGITUDE)
    anchor = pick_anchor(forecast)
    prediction = predict_from_model(current.trained_model, forecast, anchor)

    anchor_dt = prediction.anchor_utc.to_pydatetime()
    return PredictResponse(
        anchor_utc=anchor_dt,
        window_end_utc=anchor_dt + timedelta(hours=PREDICTION_WINDOW_HOURS),
        raw_prob=prediction.raw_prob,
        calibrated_prob=prediction.calibrated_prob,
        threshold=prediction.threshold,
        will_rain=prediction.will_rain,
        model_version=current.version_id,
    )
