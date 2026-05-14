"""Fetch precipitation observations from the COSMOS UK weather-station API."""

from datetime import date, datetime

import pandas as pd
from niquests import RetryConfiguration, Session

_BASE_URL = "https://cosmos-api.ceh.ac.uk"
_COLLECTION = "30M"  # half-hourly readings
_PARAMETER = "precip"


def _to_datetime(value: date | datetime | None) -> datetime | None:
    """Promote a bare ``date`` to a midnight ``datetime``; pass through datetime/None."""
    if value is None or isinstance(value, datetime):
        return value
    return datetime(value.year, value.month, value.day)


def _format_datetime(dt: datetime) -> str:
    """Format a datetime in the ``YYYY-MM-DDTHH:MM:SSZ`` form the COSMOS UK API expects."""
    return dt.strftime("%Y-%m-%dT%H:%M:%SZ")


def _format_datetime_range(start: datetime | None, end: datetime | None) -> str:
    """Format a datetime range as the OGC ``datetime`` parameter value.

    Produces one of three formats:
      - ``<start>..``        (open-ended forward)
      - ``..<end>``          (open-ended backward)
      - ``<start>/<end>``    (closed range)

    At least one bound must be provided; both-``None`` raises.
    """
    match start, end:
        case datetime() as s, None:
            return f"{_format_datetime(s)}.."
        case None, datetime() as e:
            return f"..{_format_datetime(e)}"
        case datetime() as s, datetime() as e:
            return f"{_format_datetime(s)}/{_format_datetime(e)}"
        case _:
            raise TypeError("At least one of start or end must be provided.")


def fetch_observations(
    site_code: str,
    start_date: date | datetime | None,
    end_date: date | datetime | None = None,
) -> pd.DataFrame:
    """Fetch half-hourly pluvio precipitation readings from a COSMOS UK station.

    Returns a DataFrame with a ``time`` DatetimeIndex (UTC) and a single
    ``pluvio`` column of millimetres per half hour.
    """
    datetime_range = _format_datetime_range(_to_datetime(start_date), _to_datetime(end_date))
    url = (
        f"{_BASE_URL}/collections/{_COLLECTION}/locations/{site_code}"
        f"?datetime={datetime_range}&parameter-name={_PARAMETER}"
    )
    session = Session(retries=RetryConfiguration(total=5, backoff_factor=0.2))
    response = session.get(url)
    response.raise_for_status()
    payload = response.json()

    site_data = payload["coverages"][0]
    times = site_data["domain"]["axes"]["t"]["values"]
    readings = site_data["ranges"][_PARAMETER]["values"]

    return pd.DataFrame(
        {"pluvio": readings},
        index=pd.DatetimeIndex(pd.to_datetime(times, utc=True), name="time"),
    )
