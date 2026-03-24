"""Gold dataset evaluation harness.

Evaluates linking quality against the gold dataset.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path

from workers.linking.types import Link


@dataclass
class GoldLink:
    """A link from the gold dataset."""

    requirement: str
    entity: str  # file_path:symbol_name
    link_type: str
    source: str


@dataclass
class EvalResult:
    """Evaluation metrics."""

    precision: float = 0.0
    recall: float = 0.0
    f1: float = 0.0
    true_positives: int = 0
    false_positives: int = 0
    false_negatives: int = 0
    total_gold: int = 0
    total_predicted: int = 0
    tp_links: list[str] = field(default_factory=list)
    fp_links: list[str] = field(default_factory=list)
    fn_links: list[str] = field(default_factory=list)


def load_gold_dataset(path: str | Path) -> list[GoldLink]:
    """Load gold dataset from JSON file."""
    data = json.loads(Path(path).read_text())
    return [
        GoldLink(
            requirement=link["requirement"],
            entity=link["entity"],
            link_type=link["type"],
            source=link["source"],
        )
        for link in data["links"]
    ]


def evaluate(predicted: list[Link], gold: list[GoldLink]) -> EvalResult:
    """Evaluate predicted links against the gold dataset.

    A predicted link is a true positive if it matches a gold link
    on (requirement_id, entity_key).

    Args:
        predicted: Links produced by the linker.
        gold: Gold standard links.

    Returns:
        EvalResult with precision, recall, F1, and details.
    """
    # Build sets of (req_id, entity_key) for comparison
    gold_set = {(g.requirement, g.entity) for g in gold}
    pred_set: set[tuple[str, str]] = set()
    for link in predicted:
        key = f"{link.entity.file_path}:{link.entity.name}"
        pred_set.add((link.requirement_id, key))

    tp = gold_set & pred_set
    fp = pred_set - gold_set
    fn = gold_set - pred_set

    result = EvalResult(
        true_positives=len(tp),
        false_positives=len(fp),
        false_negatives=len(fn),
        total_gold=len(gold_set),
        total_predicted=len(pred_set),
        tp_links=[f"{r}→{e}" for r, e in sorted(tp)],
        fp_links=[f"{r}→{e}" for r, e in sorted(fp)],
        fn_links=[f"{r}→{e}" for r, e in sorted(fn)],
    )

    if result.total_predicted > 0:
        result.precision = result.true_positives / result.total_predicted
    if result.total_gold > 0:
        result.recall = result.true_positives / result.total_gold
    if result.precision + result.recall > 0:
        result.f1 = 2 * result.precision * result.recall / (result.precision + result.recall)

    return result


def evaluate_confidence_calibration(
    predicted: list[Link], gold: list[GoldLink], threshold: float = 0.9
) -> float:
    """Evaluate confidence calibration.

    For links above the threshold, what fraction are correct?

    Returns:
        Fraction of high-confidence links that are true positives.
    """
    gold_set = {(g.requirement, g.entity) for g in gold}

    high_conf = [lnk for lnk in predicted if lnk.confidence >= threshold]
    if not high_conf:
        return 1.0  # No high-confidence links, vacuously true

    correct = 0
    for link in high_conf:
        key = f"{link.entity.file_path}:{link.entity.name}"
        if (link.requirement_id, key) in gold_set:
            correct += 1

    return correct / len(high_conf)
