from openmeteo_sdk import Model

HISTORICAL_FORECAST_BASE_URL = "https://historical-forecast-api.open-meteo.com/v1/forecast"
CURRENT_FORECAST_BASE_URL = "https://api.open-meteo.com/v1/forecast"

FORECAST_VARIABLES = [
    "temperature_2m",
    "relative_humidity_2m",
    "apparent_temperature",
    "precipitation",
    "rain",
    "showers",
    "cloud_cover",
    "wind_speed_10m",
    "wind_direction_10m",
]

FORECAST_MODELS = [
    "ukmo_uk_deterministic_2km",
    "ecmwf_ifs",
]


def model_to_name(code: int) -> str | None:
    """Translate an Open-Meteo ``Model`` enum code back to its string name."""
    for name, value in Model.Model.__dict__.items():
        if value == code:
            return name
    return None
