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
    session = Session(retries=RetryConfiguration(total=5, backoff_factor=0.2))
    client = openmeteo_requests.Client(session=session)

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
