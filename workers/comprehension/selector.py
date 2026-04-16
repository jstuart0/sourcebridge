# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Strategy selector with capability gating and fallback chains.

Given:
  - a preference chain of strategy names (the operator's ordered
    preference, e.g. ``["hierarchical", "long_context_direct", "single_shot"]``)
  - a mapping from strategy name -> strategy instance
  - the currently configured model id
  - the model capability registry

the selector walks the chain in order, checks each strategy's
:class:`CapabilityRequirements` against the model's profile, and
returns the first strategy that passes.

Every skip is recorded in a :class:`SelectionTrace` so the Monitor
page can show operators exactly why a particular strategy was or
wasn't used. The trace is meant to be plain-English and copy-pasteable
into a support ticket.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import structlog

from workers.comprehension.capabilities import (
    ModelCapabilities,
    ModelCapabilityRegistry,
)
from workers.comprehension.strategy import ComprehensionStrategy

log = structlog.get_logger()


@dataclass
class SelectionStep:
    """One strategy considered during selection.

    The ``status`` field is one of:
      - ``"selected"``: this strategy will run
      - ``"skipped_capability"``: the model doesn't meet the strategy's requirements
      - ``"skipped_unknown"``: the name isn't registered in the strategies map
    """

    strategy: str
    status: str
    reason: str = ""


@dataclass
class SelectionTrace:
    """Record of a selector walk, returned alongside the chosen strategy."""

    model_id: str
    preference_chain: list[str]
    selected: str = ""
    steps: list[SelectionStep] = field(default_factory=list)

    def add(self, step: SelectionStep) -> None:
        self.steps.append(step)

    def summary(self) -> str:
        """Human-readable one-liner used in logs and Monitor UI."""
        if not self.steps:
            return f"no strategies considered for model {self.model_id}"
        chosen = self.selected or "none"
        return f"selected={chosen} · model={self.model_id} · chain={','.join(self.preference_chain)}"


@dataclass
class SelectionResult:
    """What the selector returns to the caller.

    ``strategy`` is None when no entry in the preference chain was
    viable on the current model. In that case the caller should
    surface an actionable error to the user.
    """

    strategy: ComprehensionStrategy | None
    strategy_name: str
    trace: SelectionTrace


class StrategySelector:
    """Picks the first viable strategy from a preference chain."""

    def __init__(
        self,
        *,
        registry: ModelCapabilityRegistry | None = None,
    ) -> None:
        self._registry = registry or ModelCapabilityRegistry()

    def select(
        self,
        *,
        strategies: dict[str, ComprehensionStrategy],
        preference_chain: list[str],
        model_id: str,
    ) -> SelectionResult:
        """Walk the preference chain and return the first viable strategy.

        The returned :class:`SelectionTrace` always contains one step
        per chain entry, with a plain-English reason for every skip.
        """
        capabilities = self._registry.lookup(model_id)
        trace = SelectionTrace(model_id=model_id, preference_chain=list(preference_chain))

        for name in preference_chain:
            strategy = strategies.get(name)
            if strategy is None:
                trace.add(
                    SelectionStep(
                        strategy=name,
                        status="skipped_unknown",
                        reason=f"strategy {name!r} is not registered",
                    )
                )
                continue

            ok, reason = _check_capabilities(strategy, capabilities)
            if not ok:
                trace.add(
                    SelectionStep(
                        strategy=name,
                        status="skipped_capability",
                        reason=reason,
                    )
                )
                log.info(
                    "strategy_skipped_capability",
                    strategy=name,
                    model=model_id,
                    reason=reason,
                )
                continue

            trace.add(
                SelectionStep(
                    strategy=name,
                    status="selected",
                    reason=f"model {model_id!r} satisfies requirements",
                )
            )
            trace.selected = name
            log.info(
                "strategy_selected",
                strategy=name,
                model=model_id,
                trace=trace.summary(),
            )
            return SelectionResult(
                strategy=strategy,
                strategy_name=name,
                trace=trace,
            )

        log.warning(
            "strategy_selector_no_viable",
            model=model_id,
            trace=trace.summary(),
        )
        return SelectionResult(strategy=None, strategy_name="", trace=trace)


def _check_capabilities(
    strategy: ComprehensionStrategy,
    capabilities: ModelCapabilities,
) -> tuple[bool, str]:
    """Return (ok, reason) for a strategy / model pair.

    ``reason`` is always populated — on failure it explains the
    specific shortfall, on success it confirms the match.
    """
    reqs = strategy.capability_requirements()

    if not capabilities.meets_context(reqs.min_context_tokens):
        return False, (
            f"model context {capabilities.effective_context_tokens} tokens < required {reqs.min_context_tokens}"
        )
    if not capabilities.meets_instruction_following(reqs.min_instruction_following):
        return False, (
            f"model instruction_following={capabilities.instruction_following} "
            f"< required {reqs.min_instruction_following}"
        )
    if reqs.requires_json_mode and not capabilities.meets_json_mode(required_native=True):
        return False, f"model json_mode={capabilities.json_mode} does not meet requirement"
    if reqs.requires_tool_use and not capabilities.meets_tool_use(required_native=True):
        return False, f"model tool_use={capabilities.tool_use} does not meet requirement"
    if reqs.min_extraction_grade is not None and not _grade_ok(
        capabilities.extraction_grade, reqs.min_extraction_grade
    ):
        return False, (f"model extraction_grade={capabilities.extraction_grade} < required {reqs.min_extraction_grade}")
    if reqs.min_creative_grade is not None and not _grade_ok(capabilities.creative_grade, reqs.min_creative_grade):
        return False, (f"model creative_grade={capabilities.creative_grade} < required {reqs.min_creative_grade}")
    return True, "all requirements met"


_GRADE_ORDER = {"low": 0, "medium": 1, "high": 2}


def _grade_ok(actual: str, required: str) -> bool:
    return _GRADE_ORDER.get(actual.lower(), 0) >= _GRADE_ORDER.get(required.lower(), 0)
