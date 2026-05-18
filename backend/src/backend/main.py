"""FastAPI app: serves predictions from the @production model."""

import logging
from contextlib import asynccontextmanager
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Annotated

from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel, Field
from pydantic_settings import BaseSettings, SettingsConfigDict

from backend.forecast_fetch import fetch_live_forecast, pick_anchor
from will_it_rain_shared.champion import Champion, load_champion_bundle
from will_it_rain_shared.predict import PREDICTION_WINDOW_HOURS, predict_from_bundle

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

    # Same rationale as Bundle in shared/predict.py: pydantic reserves the
    # `model_*` prefix by default, which would warn on the two fields above.
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
class LoadedChampion:
    """The currently-serving champion bundle and when this process loaded it."""

    champion: Champion
    loaded_at: datetime


# Module-scope state, written exactly once by the lifespan and read via the
# Depends providers below. Tests should override the providers via
# `app.dependency_overrides[...]` rather than reassigning these.
_loaded: LoadedChampion | None = None
_settings: Settings | None = None


@asynccontextmanager
async def lifespan(app: FastAPI):
    global _loaded, _settings
    _settings = Settings()
    champion = load_champion_bundle(
        model_display_name=_settings.MODEL_DISPLAY_NAME,
        project=_settings.PROJECT,
        location=_settings.LOCATION,
    )
    if champion is None:
        logger.warning("No @production model found at startup — /api/predict will 503.")
    else:
        _loaded = LoadedChampion(champion=champion, loaded_at=datetime.now(tz=timezone.utc))
        logger.info("Loaded model version %s at %s.", champion.version_id, _loaded.loaded_at)
    yield


def get_settings() -> Settings:
    # Lifespan always assigns _settings before requests are served; the
    # assert narrows the type for downstream handlers.
    assert _settings is not None, "Settings accessed before lifespan startup completed."
    return _settings


def get_champion() -> Champion:
    if _loaded is None:
        raise HTTPException(status_code=503, detail="Model not loaded.")
    return _loaded.champion


SettingsDep = Annotated[Settings, Depends(get_settings)]
ChampionDep = Annotated[Champion, Depends(get_champion)]


app = FastAPI(title="will-it-rain", lifespan=lifespan)


@app.get("/api/health")
def health() -> HealthResponse:
    # Health reads module state directly rather than via Depends: it must
    # report the "no champion loaded" state without 503'ing, which is what
    # get_champion is for.
    return HealthResponse(
        status="ok",
        model_version=_loaded.champion.version_id if _loaded else None,
        model_resource=_loaded.champion.resource_name if _loaded else None,
        loaded_at_utc=_loaded.loaded_at if _loaded else None,
    )


@app.get("/api/predict")
def predict(champion: ChampionDep, settings: SettingsDep) -> PredictResponse:
    forecast = fetch_live_forecast(settings.LATITUDE, settings.LONGITUDE)
    anchor = pick_anchor(forecast)
    prediction = predict_from_bundle(champion.bundle, forecast, anchor)

    anchor_dt = prediction.anchor_utc.to_pydatetime()
    return PredictResponse(
        anchor_utc=anchor_dt,
        window_end_utc=anchor_dt + timedelta(hours=PREDICTION_WINDOW_HOURS),
        raw_prob=prediction.raw_prob,
        calibrated_prob=prediction.calibrated_prob,
        threshold=prediction.threshold,
        will_rain=prediction.will_rain,
        model_version=champion.version_id,
    )
