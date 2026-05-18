"""Feature building for will-it-rain.

The single source of truth for feature construction. Imported by both the
training pipeline and the serving backend; drift here would silently break
predictions.
"""

from collections.abc import Sequence

import numpy as np
import pandas as pd

DEFAULT_LAG_HOURS: tuple[int, ...] = (1, 2, 3)


def build_features(
    forecast: pd.DataFrame,
    lag_hours: Sequence[int] = DEFAULT_LAG_HOURS,
) -> pd.DataFrame:
    """Build training/inference features from a forecast DataFrame.

    The input is expected to have a hourly DatetimeIndex of UTC anchors and
    forecast variable columns (typically named ``model__variable``). Sparse
    columns whose presence varies over the historical window should be dropped
    by the caller before calling this function — they are not handled here.

    Returns the original columns plus lagged copies (suffixed ``__lagNh``) and
    cyclical encodings of hour-of-day and month. Cyclical (sin/cos) rather
    than integer encoding because hour 23 is adjacent to hour 0 and month 12
    is adjacent to month 1, but an integer feature gives LightGBM a cliff at
    the wrap-around point.
    """
    index = forecast.index
    if not isinstance(index, pd.DatetimeIndex):
        raise TypeError("forecast must have a DatetimeIndex.")
    features = forecast.copy()
    for lag in lag_hours:
        lagged = forecast.shift(lag).add_suffix(f"__lag{lag}h")
        features = features.join(lagged)
    # `DatetimeIndex.hour` / `.month` exist at runtime but aren't in
    # pandas-stubs. Going via `to_series().dt` uses the typed Series accessor.
    timestamps = index.to_series()
    hour_angle = 2 * np.pi * timestamps.dt.hour / 24
    month_angle = 2 * np.pi * (timestamps.dt.month - 1) / 12
    features["hour_of_day_sin"] = np.sin(hour_angle)
    features["hour_of_day_cos"] = np.cos(hour_angle)
    features["month_sin"] = np.sin(month_angle)
    features["month_cos"] = np.cos(month_angle)
    return features
