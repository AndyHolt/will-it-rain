"""Pure inference function: bundle + forecast frame → prediction.

Lives in `shared` so the bundle key contract has exactly one consumer-side
definition. Drift between this function and `train.save_bundle` would
silently break predictions; that's the same hazard `features.py` and
`champion.py` are kept here to avoid.
"""

from dataclasses import dataclass

import lightgbm as lgb
import pandas as pd
from pydantic import BaseModel, ConfigDict
from sklearn.isotonic import IsotonicRegression

from will_it_rain_shared.features import build_features

# Forward window the model predicts over. Used at train time as the label
# horizon (build_labels) and at serve time as the reported window end. These
# two uses must agree or predictions become meaningless, so the constant
# lives here in shared rather than being duplicated.
PREDICTION_WINDOW_HOURS: int = 4


class Bundle(BaseModel):
    """Train/serve contract for a champion bundle.

    Validated at the load boundary so a malformed bundle fails with a clear
    schema error rather than a downstream KeyError. `arbitrary_types_allowed`
    is required for the pickled estimator fields; pydantic still isinstance-
    checks them on validate.
    """

    # `protected_namespaces=()` lifts pydantic's default ban on the `model_*`
    # prefix so the field can be called `model` to match the on-disk key.
    model_config = ConfigDict(arbitrary_types_allowed=True, protected_namespaces=())

    model: lgb.LGBMClassifier
    calibrator: IsotonicRegression
    threshold: float
    feature_cols: list[str]
    lag_hours: list[int]
    sparse_columns: list[str]


@dataclass(frozen=True)
class Prediction:
    anchor_utc: pd.Timestamp
    raw_prob: float
    calibrated_prob: float
    threshold: float
    will_rain: bool


def predict_from_bundle(
    bundle: Bundle,
    forecast: pd.DataFrame,
    anchor_utc: pd.Timestamp,
) -> Prediction:
    """Predict will_rain at ``anchor_utc`` using a champion bundle.

    ``forecast`` must contain at least ``anchor_utc`` and the preceding
    ``max(lag_hours)`` hourly rows so lagged features are populated. The
    sparse columns the bundle was trained without are dropped first.
    """
    trimmed = forecast.drop(columns=[c for c in bundle.sparse_columns if c in forecast.columns])
    features = build_features(trimmed, bundle.lag_hours)

    if anchor_utc not in features.index:
        raise KeyError(
            f"anchor_utc {anchor_utc} not present in forecast index "
            f"(range: {features.index.min()} → {features.index.max()})."
        )
    row = features.loc[[anchor_utc], bundle.feature_cols]

    raw = float(bundle.model.predict_proba(row)[:, 1][0])
    calibrated = float(bundle.calibrator.transform([raw])[0])
    return Prediction(
        anchor_utc=anchor_utc,
        raw_prob=raw,
        calibrated_prob=calibrated,
        threshold=bundle.threshold,
        will_rain=calibrated >= bundle.threshold,
    )
