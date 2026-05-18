"""Fetch the live hourly forecast from Open-Meteo's `forecast` endpoint.

Backend-only: the pipeline trains on the `historical-forecast` endpoint
(retrospective best-known forecasts for past dates); this endpoint serves
the current forecast for inference. Column shape (`{model}__{variable}`)
matches the historical fetcher so the same feature builder works on both.
"""

import openmeteo_requests
import pandas as pd
from niquests import RetryConfiguration, Session

from will_it_rain_shared.forecast import (
    CURRENT_FORECAST_BASE_URL,
    FORECAST_MODELS,
    FORECAST_VARIABLES,
    model_to_name,
)


def fetch_live_forecast(
    latitude: float,
    longitude: float,
    *,
    past_hours: int = 24,
    forecast_hours: int = 24,
) -> pd.DataFrame:
    """Fetch the live hourly forecast and return it as a DataFrame.

    Returns a frame indexed by hourly UTC ``date`` with columns named
    ``{model}__{variable}``. ``past_hours`` must cover the largest lag the
    model uses; the default 24h is comfortably above the configured lags.
    """
    session = Session(retries=RetryConfiguration(total=5, backoff_factor=0.2))
    client = openmeteo_requests.Client(session=session)

    responses = client.weather_api(
        CURRENT_FORECAST_BASE_URL,
        params={
            "latitude": latitude,
            "longitude": longitude,
            "past_hours": past_hours,
            "forecast_hours": forecast_hours,
            "hourly": list(FORECAST_VARIABLES),
            "models": list(FORECAST_MODELS),
        },
    )

    combined: pd.DataFrame | None = None
    for response in responses:
        model_name = model_to_name(response.Model())
        hourly = response.Hourly()
        if hourly is None:
            raise RuntimeError(f"Open-Meteo response for {model_name} had no hourly data.")
        data: dict[str, object] = {
            "date": pd.date_range(
                start=pd.to_datetime(hourly.Time(), unit="s", utc=True),
                end=pd.to_datetime(hourly.TimeEnd(), unit="s", utc=True),
                freq=pd.Timedelta(seconds=hourly.Interval()),
                inclusive="left",
            )
        }
        for i, variable_name in enumerate(FORECAST_VARIABLES):
            variable = hourly.Variables(i)
            if variable is None:
                raise RuntimeError(
                    f"Open-Meteo response for {model_name} missing variable {variable_name}."
                )
            data[f"{model_name}__{variable_name}"] = variable.ValuesAsNumpy()
        frame = pd.DataFrame(data)
        combined = frame if combined is None else combined.merge(frame, on="date", how="outer")

    if combined is None:
        raise RuntimeError("Open-Meteo returned no responses for the given parameters.")

    return combined.set_index("date").sort_index()


def pick_anchor(forecast: pd.DataFrame, now_utc: pd.Timestamp | None = None) -> pd.Timestamp:
    """Pick the anchor hour: the most recent hour at or before ``now_utc``.

    The label horizon is forward-looking (next 4h), so anchoring at the
    last *completed* hour means the prediction window begins immediately
    and ends about 4h in the future. Defaults ``now_utc`` to wall-clock
    UTC if unset (so the function is testable).
    """
    if now_utc is None:
        now_utc = pd.Timestamp.now(tz="UTC")
    floor = now_utc.floor("h")
    candidates = forecast.index[forecast.index <= floor]
    if len(candidates) == 0:
        raise ValueError(f"Forecast has no rows at or before {floor}.")
    # Index.max() is typed `Timestamp | NaTType`; the len() guard above rules
    # out NaT, but ty can't see that, so narrow explicitly.
    latest = candidates.max()
    if not isinstance(latest, pd.Timestamp):
        raise ValueError(f"Latest candidate is not a Timestamp: {latest!r}.")
    return latest
