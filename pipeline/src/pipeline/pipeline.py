"""Will-it-rain training pipeline DAG.

Composed of the @dsl.component wrappers in ``pipeline.kfp_components``.
Compile with ``kfp.compiler.Compiler().compile(will_it_rain_pipeline, "pipeline.json")``
or submit directly via ``aiplatform.PipelineJob``.
"""

from kfp import dsl

from pipeline.kfp_components import (
    evaluate_op,
    fetch_forecast_op,
    fetch_observations_op,
    prepare_op,
    promote_op,
    register_op,
    train_op,
)

DEFAULT_MODEL_DISPLAY_NAME = "will-it-rain"


@dsl.pipeline(
    name="will-it-rain-train",
    description="fetch → prepare → train → evaluate → register/promote.",
)
def will_it_rain_pipeline(
    latitude: float,
    longitude: float,
    site_code: str,
    training_window_start_date: str,
    project: str,
    location: str,
    artefacts_bucket: str,
    model_display_name: str = DEFAULT_MODEL_DISPLAY_NAME,
) -> None:
    fetch_forecast = fetch_forecast_op(
        latitude=latitude,
        longitude=longitude,
        start_date=training_window_start_date,
    )
    fetch_observations = fetch_observations_op(
        site_code=site_code,
        start_date=training_window_start_date,
    )
    prepare = prepare_op(
        forecast=fetch_forecast.outputs["forecast"],
        observations=fetch_observations.outputs["observations"],
    )
    train = train_op(
        prepared=prepare.outputs["prepared"],
    )
    evaluate = evaluate_op(
        prepared=prepare.outputs["prepared"],
        challenger_bundle=train.outputs["bundle"],
        project=project,
        location=location,
        model_display_name=model_display_name,
    )

    # Gate: only register if the challenger beats the persistence baseline.
    # Persistence is the cheapest non-trivial baseline — a model that can't
    # outperform "assume the next 4h is like the last 4h" isn't worth shipping.
    with dsl.If(
        evaluate.outputs["challenger_f1"] >= evaluate.outputs["baseline_persistence_f1"],
        name="register-and-promote",
    ):
        register = register_op(
            bundle=train.outputs["bundle"],
            evaluation=evaluate.outputs["evaluation"],
            project=project,
            location=location,
            artefacts_bucket=artefacts_bucket,
            model_display_name=model_display_name,
        )
        promote_op(
            registered_model_resource_name=register.output,
            evaluation=evaluate.outputs["evaluation"],
            project=project,
            location=location,
        )
