"""Train a LightGBM classifier with isotonic calibration."""

import json
from pathlib import Path

import joblib
import lightgbm as lgb
import numpy as np
from sklearn.isotonic import IsotonicRegression
from sklearn.metrics import f1_score

from pipeline.components.prepare import PreparedData
from will_it_rain_shared.predict import PREDICTION_WINDOW_HOURS, TrainedModel

RANDOM_SEED = 42

# Filenames of the serving contract, read by name (no listing) at serve time.
# Named here rather than at the call sites so train and register can't drift.
SERVING_MODEL_FILENAME = "model.txt"
SERVING_METADATA_FILENAME = "serving.json"


def train(
    prepared: PreparedData,
    *,
    random_seed: int = RANDOM_SEED,
    n_estimators: int = 1000,
    learning_rate: float = 0.05,
    num_leaves: int = 31,
    early_stopping_rounds: int = 50,
    threshold_grid_points: int = 91,
) -> TrainedModel:
    """Fit the classifier, calibrate on val, and pick an F1-max threshold.

    Calibration corrects the magnitude drift caused by the train regime being
    drier than val/test; with calibrated probabilities a magnitude threshold
    becomes meaningful. The threshold is then F1-maximised on the same
    calibrated val predictions.
    """
    X_train = prepared.train[prepared.feature_cols]
    y_train = prepared.train["will_rain"].astype(int)
    X_val = prepared.val[prepared.feature_cols]
    y_val = prepared.val["will_rain"].astype(int)

    model = lgb.LGBMClassifier(
        objective="binary",
        n_estimators=n_estimators,
        learning_rate=learning_rate,
        num_leaves=num_leaves,
        random_state=random_seed,
        verbose=-1,
    )
    model.fit(
        X_train,
        y_train,
        eval_set=[(X_val, y_val)],
        callbacks=[lgb.early_stopping(stopping_rounds=early_stopping_rounds, verbose=False)],
    )

    val_probs_raw = model.predict_proba(X_val)[:, 1]
    calibrator = IsotonicRegression(out_of_bounds="clip")
    calibrator.fit(val_probs_raw, y_val)
    val_probs = calibrator.transform(val_probs_raw)

    candidate_thresholds = np.linspace(0.05, 0.95, threshold_grid_points)
    val_f1s = [f1_score(y_val, val_probs >= t) for t in candidate_thresholds]
    best_threshold = float(candidate_thresholds[int(np.argmax(val_f1s))])

    return TrainedModel(
        model=model,
        calibrator=calibrator,
        threshold=best_threshold,
        feature_cols=list(prepared.feature_cols),
        lag_hours=list(prepared.lag_hours),
        sparse_columns=list(prepared.sparse_columns),
    )


def save_bundle(trained_model: TrainedModel, path: str | Path) -> None:
    """Serialise a TrainedModel to a joblib file at ``path``."""
    joblib.dump(trained_model.model_dump(), path)


def save_serving_artefacts(trained_model: TrainedModel, directory: str | Path) -> None:
    """Emit the language-neutral serving contract: native model + metadata.

    The joblib bundle written by ``save_bundle`` is only readable from Python,
    which ties serving to this interpreter. These two files carry the same
    information in formats any runtime can read: LightGBM's own text format,
    and the calibrator, threshold and feature metadata as JSON. Both are
    derived from the same ``TrainedModel``, so neither can drift from the
    bundle that champion evaluation uses.

    The isotonic calibrator is reduced to its knots (``X_thresholds_`` /
    ``y_thresholds_``); evaluating it is linear interpolation between them,
    clamped at both ends to match ``out_of_bounds="clip"``.
    """
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)

    trained_model.model.booster_.save_model(str(directory / SERVING_MODEL_FILENAME))

    calibrator = trained_model.calibrator
    metadata = {
        "feature_cols": list(trained_model.feature_cols),
        "lag_hours": [int(h) for h in trained_model.lag_hours],
        "sparse_columns": list(trained_model.sparse_columns),
        "threshold": float(trained_model.threshold),
        "prediction_window_hours": PREDICTION_WINDOW_HOURS,
        "isotonic": {
            "x": [float(v) for v in calibrator.X_thresholds_],
            "y": [float(v) for v in calibrator.y_thresholds_],
        },
    }
    (directory / SERVING_METADATA_FILENAME).write_text(json.dumps(metadata, indent=2) + "\n")
