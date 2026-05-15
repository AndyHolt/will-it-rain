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

    # Gate: evaluate_op returns True iff the challenger should ship. Policy
    # (which baseline must be beaten) is owned by the component, not the DAG.
    # `== True` is deliberate: evaluate.output is a PipelineParameterChannel,
    # not a bool, and KFP overloads __eq__ to build a pipeline-time comparison.
    # A bare `evaluate.output` would be truthy at compile time and the gate
    # would always fire. Hence the E712 suppression.
    with dsl.If(evaluate.output == True, name="register-and-promote"):  # noqa: E712
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
