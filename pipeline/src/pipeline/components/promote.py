"""Move the @production alias to the freshly-registered version when warranted."""

from google.cloud import aiplatform

from pipeline.components.evaluate import EvaluationResult

DEFAULT_PROMOTION_EPSILON = 0.01
DEFAULT_PRODUCTION_ALIAS = "production"


def promote(
    new_version: aiplatform.Model,
    evaluation: EvaluationResult,
    *,
    project: str,
    location: str,
    promotion_epsilon: float = DEFAULT_PROMOTION_EPSILON,
    production_alias: str = DEFAULT_PRODUCTION_ALIAS,
) -> bool:
    """Set or move the production alias to ``new_version``.

    Two cases promote:
      - First run: ``evaluation.champion`` is ``None`` (no production alias
        exists yet) — the first registered version becomes production.
      - Subsequent run: challenger F1 > champion F1 + ε on the same test set.

    The Vertex Model Registry guarantees aliases are unique per parent model,
    so ``add_version_aliases`` automatically removes the alias from whichever
    version currently holds it.

    Returns ``True`` if the alias was set or moved, ``False`` otherwise.
    """
    aiplatform.init(project=project, location=location)

    should_promote = (
        evaluation.champion is None
        or evaluation.challenger.f1 > evaluation.champion.f1 + promotion_epsilon
    )
    if should_promote:
        new_version.versioning_registry.add_version_aliases(
            new_aliases=[production_alias],
            version=new_version.version_id,
        )
    return should_promote
