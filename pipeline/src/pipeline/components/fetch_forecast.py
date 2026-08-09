"""Fetch historical forecasts from Open-Meteo's historical-forecast endpoint."""

from datetime import date

import openmeteo_requests
import pandas as pd
from niquests import RetryConfiguration, Session

from will_it_rain_shared.forecast import (
    FORECAST_MODELS,
    FORECAST_VARIABLES,
    HISTORICAL_FORECAST_BASE_URL,
    model_to_name,
)

# Loading large historical data set (~2MB of flatbuffers) from Open-Meteo takes
# some time: ~40 seconds when uncached. Once request cached in their systems,
# fetch is nearly instantaneous (<1s). So set a long read timeout: a long delay
# here is expected, not a sign of failure.
_CONNECT_TIMEOUT_SECONDS = 10
_READ_TIMEOUT_SECONDS = 180

# Status codes are listed explicitly because urllib3 retries transport errors by
# default but not HTTP failures — without this, a 503 or a rate-limit reply is a
# one-shot loss.
_RETRIES = RetryConfiguration(
    total=5,
    backoff_factor=2.0,
    backoff_jitter=1.0,
    status_forcelist=(429, 500, 502, 503, 504),
)


def fetch_forecast(
    latitude: float,
    longitude: float,
    start_date: str | date,
    end_date: str | date,
) -> pd.DataFrame:
    """Fetch historical hourly forecasts and return them as a DataFrame.

    Returns a frame indexed by hourly UTC ``date`` with columns named
    ``{model}__{variable}``. The set of models and variables is fixed by
    ``will_it_rain_shared.forecast`` to keep training and inference in lock-step.
    """
    session = Session(retries=_RETRIES)
    client = openmeteo_requests.Client(session=session)

    # `weather_api` forwards **kwargs to the underlying session.get, which is
    # the only way to reach the timeout — niquests has no per-session default.
    responses = client.weather_api(
        HISTORICAL_FORECAST_BASE_URL,
        params={
            "latitude": latitude,
            "longitude": longitude,
            "start_date": str(start_date),
            "end_date": str(end_date),
            "hourly": list(FORECAST_VARIABLES),
            "models": list(FORECAST_MODELS),
        },
        timeout=(_CONNECT_TIMEOUT_SECONDS, _READ_TIMEOUT_SECONDS),
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
