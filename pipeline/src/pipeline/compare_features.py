"""Local A/B for linear vs cyclical hour/month features.

This branch changes the feature builder, so a normal pipeline run can't
compare against the registered champion — its bundle was trained on the
old (linear) feature set and would hit ``ChampionFeatureMismatchError``.
This script side-steps that by training and evaluating both variants
locally on the same fetched data, same splits, same seed.

Run from the repo root:

    uv run --package pipeline python -m pipeline.compare_features

The forecast/observations fetch is cached to ``build/compare_features_cache/``
because Open-Meteo is slow and rate-limited. Pass ``--refresh`` to re-fetch.
"""

import argparse
from collections.abc import Callable, Sequence
from pathlib import Path

import pandas as pd
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

from pipeline.components import prepare as prepare_module
from pipeline.components.evaluate import evaluate
from pipeline.components.fetch_forecast import fetch_forecast
from pipeline.components.fetch_observations import fetch_observations
from pipeline.components.prepare import prepare
from pipeline.components.train import train
from will_it_rain_shared.features import build_features as cyclical_build_features

FeatureBuilder = Callable[[pd.DataFrame, Sequence[int]], pd.DataFrame]

CACHE_DIR = Path("build/compare_features_cache")


class Settings(BaseSettings):
    # See pipeline.trigger for the rationale on `= Field(...)`.
    LATITUDE: float = Field(...)
    LONGITUDE: float = Field(...)
    COSMOS_UK_SITE_CODE: str = Field(...)
    TRAINING_WINDOW_START_DATE: str = "2022-03-01"

    model_config = SettingsConfigDict(env_file=".env", extra="ignore")


def _linear_build_features(
    forecast: pd.DataFrame,
    lag_hours: Sequence[int],
) -> pd.DataFrame:
    """Pre-cyclical feature builder: integer ``hour_of_day`` and ``month``.

    Mirrors the implementation on ``main`` prior to commit d41f713 so the
    A/B is against the exact feature shape the champion was trained on.
    """
    index = forecast.index
    if not isinstance(index, pd.DatetimeIndex):
        raise TypeError("forecast must have a DatetimeIndex.")
    features = forecast.copy()
    for lag in lag_hours:
        lagged = forecast.shift(lag).add_suffix(f"__lag{lag}h")
        features = features.join(lagged)
    timestamps = index.to_series()
    features["hour_of_day"] = timestamps.dt.hour
    features["month"] = timestamps.dt.month
    return features


def _load_data(settings: Settings, refresh: bool) -> tuple[pd.DataFrame, pd.DataFrame]:
    """Fetch forecast + observations, cached to local parquet between runs."""
    from datetime import date

    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    forecast_path = CACHE_DIR / "forecast.parquet"
    observations_path = CACHE_DIR / "observations.parquet"

    if refresh or not forecast_path.exists():
        print(f"Fetching forecast from {settings.TRAINING_WINDOW_START_DATE} → today …")
        forecast_df = fetch_forecast(
            settings.LATITUDE,
            settings.LONGITUDE,
            settings.TRAINING_WINDOW_START_DATE,
            date.today().isoformat(),
        )
        forecast_df.to_parquet(forecast_path)
    else:
        print(f"Reusing cached forecast at {forecast_path}.")
        forecast_df = pd.read_parquet(forecast_path)

    if refresh or not observations_path.exists():
        print(f"Fetching observations from {settings.TRAINING_WINDOW_START_DATE} …")
        observations_df = fetch_observations(
            settings.COSMOS_UK_SITE_CODE,
            date.fromisoformat(settings.TRAINING_WINDOW_START_DATE),
        )
        observations_df.to_parquet(observations_path)
    else:
        print(f"Reusing cached observations at {observations_path}.")
        observations_df = pd.read_parquet(observations_path)

    return forecast_df, observations_df


def _run_variant(
    name: str,
    builder: FeatureBuilder,
    forecast_df: pd.DataFrame,
    observations_df: pd.DataFrame,
) -> None:
    """Swap in ``builder`` as the feature builder, then prepare → train → evaluate."""
    # ``prepare`` imports ``build_features`` by name, so module-attribute
    # replacement is enough to redirect it. ``setattr`` (over an =) because the
    # callable signatures don't match exactly (default arg vs no default) and
    # ty rightly flags the direct assignment.
    original = prepare_module.build_features
    setattr(prepare_module, "build_features", builder)
    try:
        prepared = prepare(forecast_df, observations_df)
        trained = train(prepared)
        result = evaluate(prepared, trained, champion_bundle=None)
    finally:
        setattr(prepare_module, "build_features", original)

    # Calendar columns are the ones added by the builder on top of forecast +
    # lagged copies. Diff against the forecast schema (lags share its names).
    forecast_cols = set(forecast_df.columns)
    calendar_cols = [
        c for c in prepared.feature_cols if c not in forecast_cols and "__lag" not in c
    ]
    print(f"\n=== {name} ===")
    print(f"  calendar features: {calendar_cols}")
    print(f"  test rows: {result.n_test_rows} ({result.test_start} → {result.test_end})")
    print(f"  challenger F1:                {result.challenger.f1:.4f}")
    print(
        f"  challenger precision/recall:  {result.challenger.precision:.4f} / {result.challenger.recall:.4f}"
    )
    print(f"  baseline persistence F1:      {result.baselines.persistence.f1:.4f}")
    print(f"  baseline precip-threshold F1: {result.baselines.precipitation_threshold.f1:.4f}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--refresh",
        action="store_true",
        help="Re-fetch forecast/observations even if a local cache exists.",
    )
    args = parser.parse_args()

    settings = Settings()
    forecast_df, observations_df = _load_data(settings, refresh=args.refresh)

    _run_variant("linear", _linear_build_features, forecast_df, observations_df)
    _run_variant("cyclical", cyclical_build_features, forecast_df, observations_df)


if __name__ == "__main__":
    main()
