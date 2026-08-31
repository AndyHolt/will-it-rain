"""Feature building for will-it-rain.

The single source of truth for feature construction on the Python side. The
serving backend is Go and rebuilds these features in ``internal/features``, so
the drift that would silently break predictions is now cross-language.
``golden_fixtures/`` is what pins the two together: it computes expected
outputs through this module for the Go tests to assert against.
"""

from collections.abc import Sequence

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
    two seasonal/diurnal features: ``hour_of_day`` and ``month``.
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
    features["hour_of_day"] = timestamps.dt.hour
    features["month"] = timestamps.dt.month
    return features
