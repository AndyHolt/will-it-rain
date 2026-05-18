"""KFP DSL wrappers around the plain-Python components.

Each ``@dsl.component`` runs in a container built from ``pipeline/Dockerfile``,
which has the ``pipeline`` and ``will_it_rain_shared`` packages installed.
The wrapper bodies do the artifact (de)serialisation; the actual logic lives
in the unwrapped functions in this same package.
"""

from kfp import dsl
from kfp.dsl import Artifact, Dataset, Input, Metrics, Model, Output


# Tag is set at submit time by the schedule resource — this is a placeholder.
PIPELINE_IMAGE = (
    "europe-west2-docker.pkg.dev/will-it-rain-496215/will-it-rain-images/pipeline:latest"
)

# `install_kfp_package=False` on every component: kfp is already installed in
# the base image via uv, and the uv-managed venv has no `pip`, so KFP's
# launcher-time `python -m pip install kfp==...` step fails with
# "No module named pip". Skip it.


@dsl.component(base_image=PIPELINE_IMAGE, install_kfp_package=False)
def fetch_forecast_op(
    latitude: float,
    longitude: float,
    start_date: str,
    forecast: Output[Dataset],
) -> None:
    from datetime import date

    from pipeline.components.fetch_forecast import fetch_forecast

    # End at today's UTC date — keeps the schedule resource's pipeline params
    # static while the training window grows each week.
    df = fetch_forecast(latitude, longitude, start_date, date.today().isoformat())
    df.to_parquet(forecast.path)


@dsl.component(base_image=PIPELINE_IMAGE, install_kfp_package=False)
def fetch_observations_op(
    site_code: str,
    start_date: str,
    observations: Output[Dataset],
) -> None:
    from datetime import date

    from pipeline.components.fetch_observations import fetch_observations

    df = fetch_observations(site_code, date.fromisoformat(start_date))
    df.to_parquet(observations.path)


@dsl.component(base_image=PIPELINE_IMAGE, install_kfp_package=False)
def prepare_op(
    forecast: Input[Dataset],
    observations: Input[Dataset],
    prepared: Output[Artifact],
) -> None:
    import joblib
    import pandas as pd

    from pipeline.components.prepare import prepare

    forecast_df = pd.read_parquet(forecast.path)
    observations_df = pd.read_parquet(observations.path)
    result = prepare(forecast_df, observations_df)
    joblib.dump(result, prepared.path)


@dsl.component(base_image=PIPELINE_IMAGE, install_kfp_package=False)
def train_op(
    prepared: Input[Artifact],
    bundle: Output[Model],
) -> None:
    import joblib

    from pipeline.components.train import save_bundle, train

    prepared_data = joblib.load(prepared.path)
    trained = train(prepared_data)
    save_bundle(trained, bundle.path)


@dsl.component(base_image=PIPELINE_IMAGE, install_kfp_package=False)
def evaluate_op(
    prepared: Input[Artifact],
    challenger_bundle: Input[Model],
    project: str,
    location: str,
    model_display_name: str,
    evaluation: Output[Artifact],
    metrics: Output[Metrics],
) -> bool:
    """Returns True iff the challenger should be registered.

    Gating policy lives here (not in the DAG) so that the per-baseline F1
    values don't need to be plumbed as separate component outputs — they're
    still visible via `metrics` and the serialised `evaluation` artifact.
    """
    import joblib

    from pipeline.components.evaluate import evaluate
    from will_it_rain_shared.champion import load_champion_bundle
    from will_it_rain_shared.predict import Bundle

    prepared_data = joblib.load(prepared.path)
    challenger = Bundle.model_validate(joblib.load(challenger_bundle.path))

    champion = load_champion_bundle(
        model_display_name=model_display_name,
        project=project,
        location=location,
    )
    result = evaluate(prepared_data, challenger, champion.bundle if champion else None)
    joblib.dump(result, evaluation.path)

    metrics.log_metric("challenger_f1", result.challenger.f1)
    metrics.log_metric("baseline_persistence_f1", result.baselines.persistence.f1)
    metrics.log_metric("baseline_precipitation_f1", result.baselines.precipitation_threshold.f1)
    if result.champion is not None:
        metrics.log_metric("champion_f1", result.champion.f1)

    # Persistence is the cheapest non-trivial baseline — a model that can't
    # outperform "assume the next 4h is like the last 4h" isn't worth shipping.
    return result.challenger.f1 >= result.baselines.persistence.f1


@dsl.component(base_image=PIPELINE_IMAGE, install_kfp_package=False)
def register_op(
    bundle: Input[Model],
    evaluation: Input[Artifact],
    project: str,
    location: str,
    artefacts_bucket: str,
    model_display_name: str,
) -> str:
    import joblib

    from pipeline.components.register import register

    evaluation_obj = joblib.load(evaluation.path)
    model = register(
        bundle_path=bundle.path,
        evaluation=evaluation_obj,
        project=project,
        location=location,
        artefacts_bucket=artefacts_bucket,
        model_display_name=model_display_name,
    )
    return model.versioned_resource_name


@dsl.component(base_image=PIPELINE_IMAGE, install_kfp_package=False)
def promote_op(
    registered_model_resource_name: str,
    evaluation: Input[Artifact],
    project: str,
    location: str,
) -> bool:
    import joblib
    from google.cloud import aiplatform

    from pipeline.components.promote import promote

    aiplatform.init(project=project, location=location)
    new_version = aiplatform.Model(model_name=registered_model_resource_name)
    evaluation_obj = joblib.load(evaluation.path)
    return promote(new_version, evaluation_obj, project=project, location=location)
