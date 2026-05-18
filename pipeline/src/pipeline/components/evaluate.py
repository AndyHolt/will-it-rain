"""Evaluate the challenger against baselines and (when available) the champion."""

from dataclasses import dataclass

import numpy as np
import pandas as pd
from sklearn.metrics import f1_score, precision_score, recall_score

from pipeline.components.prepare import PreparedData
from will_it_rain_shared.predict import Bundle

PERSISTENCE_WINDOW_HOURS = 4
PRECIP_BASELINE_COLUMN = "ukmo_uk_deterministic_2km__precipitation"
PRECIP_BASELINE_THRESHOLD_MM = 0.1


class _BundleFeatureMismatch(RuntimeError):
    """Internal: signal from ``_score_bundle`` that its bundle's feature_cols
    don't match the test set. Translated at the ``evaluate`` boundary into a
    role-specific public subclass of ``FeatureSchemaMismatchError``. Not part
    of the public API — callers should never catch this directly.
    """

    def __init__(self, missing: list[str], extra: list[str]) -> None:
        self.missing = missing
        self.extra = extra
        super().__init__(f"missing={missing}, extra={extra}")


class FeatureSchemaMismatchError(RuntimeError):
    """Abstract base for feature-schema mismatch errors raised by ``evaluate``.

    Never raised directly — always one of the concrete subclasses below.
    Catch this class to handle any feature mismatch regardless of role; catch
    a subclass to handle one role specifically. Subclasses set ``description``
    and ``remediation`` to identify the role and the fix.
    """

    description = ""
    remediation = ""

    def __init__(self, missing: list[str], extra: list[str]) -> None:
        self.missing = missing
        self.extra = extra
        super().__init__(
            " ".join(
                [
                    self.description,
                    f"Missing (expected, not in test set): {missing}.",
                    f"Extra (in test set, not expected): {extra}.",
                    self.remediation,
                ]
            ).strip()
        )


class ChallengerFeatureMismatchError(FeatureSchemaMismatchError):
    description = "Challenger bundle's features don't match the test set it was trained against."
    remediation = (
        "This indicates a bug in the train/prepare components, not feature drift between runs."
    )


class ChampionFeatureMismatchError(FeatureSchemaMismatchError):
    description = "Champion bundle's features don't match the current test set."
    remediation = (
        "Feature engineering has changed since the champion was registered. "
        "Re-train and re-promote, or clear the @production alias to drop "
        "the incumbent, before running again."
    )


@dataclass(frozen=True)
class EvalMetrics:
    f1: float
    precision: float
    recall: float
    predicted_positive_rate: float
    actual_positive_rate: float


@dataclass(frozen=True)
class BaselineMetrics:
    persistence: EvalMetrics
    precipitation_threshold: EvalMetrics


@dataclass(frozen=True)
class EvaluationResult:
    challenger: EvalMetrics
    baselines: BaselineMetrics
    champion: EvalMetrics | None
    test_start: pd.Timestamp
    test_end: pd.Timestamp
    n_test_rows: int


def _metrics(y_true: np.ndarray, y_pred: np.ndarray) -> EvalMetrics:
    y_true_arr = np.asarray(y_true).astype(bool)
    y_pred_arr = np.asarray(y_pred).astype(bool)
    return EvalMetrics(
        f1=float(f1_score(y_true_arr, y_pred_arr, zero_division=0.0)),
        precision=float(precision_score(y_true_arr, y_pred_arr, zero_division=0.0)),
        recall=float(recall_score(y_true_arr, y_pred_arr, zero_division=0.0)),
        predicted_positive_rate=float(y_pred_arr.mean()) if len(y_pred_arr) else 0.0,
        actual_positive_rate=float(y_true_arr.mean()) if len(y_true_arr) else 0.0,
    )


def _score_bundle(test_df: pd.DataFrame, bundle: Bundle) -> EvalMetrics:
    """Score a model bundle on the test frame.

    Raises ``_BundleFeatureMismatch`` when the bundle's expected features
    don't match the test set. The caller is responsible for translating that
    into a role-specific public exception.
    """
    expected = list(bundle.feature_cols)
    missing = [c for c in expected if c not in test_df.columns]
    extra = [c for c in test_df.columns if c not in expected and c != "will_rain"]
    # Raise on `extra` too, not just `missing`: extra columns wouldn't break
    # scoring (they'd just be ignored), but they signal that the test set has
    # features the bundle was never trained on — usually because feature
    # engineering added new columns since the bundle was registered. Surface
    # the drift rather than silently scoring on a stale feature set.
    if missing or extra:
        raise _BundleFeatureMismatch(missing=missing, extra=extra)
    X = test_df[expected]
    y_true = test_df["will_rain"].astype(int).to_numpy()
    raw = bundle.model.predict_proba(X)[:, 1]
    calibrated = bundle.calibrator.transform(raw)
    y_pred = calibrated >= bundle.threshold
    return _metrics(y_true, y_pred)


def _evaluate_persistence(test_df: pd.DataFrame) -> EvalMetrics:
    """Persistence baseline: predict rain in [T, T+4h) by what happened in
    [T-4h, T). Implemented as label-shift-forward by ``PERSISTENCE_WINDOW_HOURS``.

    The first few test rows have no in-sample history to look back on (the
    shift produces NaN), and are excluded from the metric — a small loss on
    a large test set.
    """
    actual = test_df["will_rain"].astype(bool)
    predicted = test_df["will_rain"].shift(PERSISTENCE_WINDOW_HOURS)
    eligible = predicted.notna()
    return _metrics(actual[eligible].to_numpy(), predicted[eligible].astype(bool).to_numpy())


def _evaluate_precip_threshold(test_df: pd.DataFrame) -> EvalMetrics:
    """Naive forecast baseline: rain iff ``ukmo_uk_deterministic_2km__precipitation`` ≥ 0.1mm at T."""
    y_true = test_df["will_rain"].astype(bool).to_numpy()
    y_pred = (test_df[PRECIP_BASELINE_COLUMN] >= PRECIP_BASELINE_THRESHOLD_MM).to_numpy()
    return _metrics(y_true, y_pred)


def evaluate(
    prepared: PreparedData,
    challenger: Bundle,
    champion: Bundle | None = None,
) -> EvaluationResult:
    """Score challenger, baselines, and (if provided) champion on the same test set.

    ``champion`` is the validated bundle from the current ``production``
    Model Registry entry, or ``None`` on the very first run when no production
    alias exists. The champion is scored on the same test rows as the challenger
    for a fair head-to-head comparison.
    """
    test = prepared.test

    try:
        challenger_metrics = _score_bundle(test, challenger)
    except _BundleFeatureMismatch as exc:
        raise ChallengerFeatureMismatchError(missing=exc.missing, extra=exc.extra) from exc
    persistence = _evaluate_persistence(test)
    precipitation_threshold = _evaluate_precip_threshold(test)
    # Fail loudly on champion schema drift rather than silently skip: a
    # regression caused by new features would otherwise promote with no
    # comparison against the incumbent.
    champion_metrics: EvalMetrics | None = None
    if champion is not None:
        try:
            champion_metrics = _score_bundle(test, champion)
        except _BundleFeatureMismatch as exc:
            raise ChampionFeatureMismatchError(missing=exc.missing, extra=exc.extra) from exc

    return EvaluationResult(
        challenger=challenger_metrics,
        baselines=BaselineMetrics(
            persistence=persistence,
            precipitation_threshold=precipitation_threshold,
        ),
        champion=champion_metrics,
        test_start=test.index[0],
        test_end=test.index[-1],
        n_test_rows=len(test),
    )
