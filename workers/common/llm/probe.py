# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Model capability probe.

Runs a small capability-test prompt against a model and grades the
response to populate a ModelCapabilities profile. The probe is
intentionally lightweight — it issues a single LLM call with a
structured prompt that tests several capabilities simultaneously:

1. **Instruction following**: Can the model follow a specific output format?
2. **JSON fidelity**: Can the model produce valid JSON when asked?
3. **Context echo**: Does the model acknowledge a synthetic context window marker?

The probe does NOT test:
- Actual context window limits (too expensive for a quick probe)
- Streaming support (tested at the provider level)
- Tool use (requires provider-specific tool schema)

These are left to manual overrides or future extended probes.
"""

from __future__ import annotations

import json
import logging
import time
from dataclasses import dataclass

from workers.common.llm.provider import LLMProvider, LLMResponse
from workers.comprehension.capabilities import ModelCapabilities

logger = logging.getLogger(__name__)

_PROBE_SYSTEM = """\
You are a capability test agent. Follow every instruction precisely.
Respond ONLY with the JSON object requested — no markdown fences,
no explanation, no preamble."""

_PROBE_PROMPT = """\
Return a JSON object with exactly these keys (no extra keys):

{
  "echo": "<copy the value of MARKER below exactly>",
  "count_to_five": [1, 2, 3, 4, 5],
  "reverse": "edcba",
  "classify": "positive"
}

Instructions:
- "echo": copy the following marker value exactly: PROBE_MARKER_7f3a
- "count_to_five": the integers 1 through 5 as a JSON array
- "reverse": the string "abcde" reversed
- "classify": classify the sentiment of "I love this product" as "positive", "negative", or "neutral"

Output ONLY the JSON object, nothing else."""


@dataclass
class ProbeResult:
    """Result of probing a model's capabilities."""

    model_id: str
    provider: str
    capabilities: ModelCapabilities
    raw_response: str
    latency_ms: float
    success: bool
    error: str = ""


def _grade_response(response_text: str) -> tuple[str, str]:
    """Grade instruction following and JSON mode from probe response.

    Returns:
        (instruction_following_grade, json_mode_grade)
    """
    text = response_text.strip()

    # Strip markdown code fences if present
    if text.startswith("```"):
        lines = text.split("\n")
        # Remove first line (```json or ```) and last line (```)
        if len(lines) >= 3:
            text = "\n".join(lines[1:-1]).strip()

    # Try to parse as JSON
    try:
        data = json.loads(text)
    except (json.JSONDecodeError, ValueError):
        # Can't parse JSON at all
        return "low", "none"

    if not isinstance(data, dict):
        return "low", "prompted"

    score = 0
    total = 4

    # Check echo
    if data.get("echo") == "PROBE_MARKER_7f3a":
        score += 1

    # Check count_to_five
    if data.get("count_to_five") == [1, 2, 3, 4, 5]:
        score += 1

    # Check reverse
    if data.get("reverse") == "edcba":
        score += 1

    # Check classify
    if data.get("classify") == "positive":
        score += 1

    # Grade instruction following
    if score == total or score >= total - 1:
        instruction_grade = "high"
    elif score >= 2:
        instruction_grade = "medium"
    else:
        instruction_grade = "low"

    # JSON mode grade: if we parsed valid JSON, at least "prompted"
    json_grade = "prompted"

    return instruction_grade, json_grade


async def probe_model(
    provider: LLMProvider,
    model_id: str,
    provider_name: str = "",
) -> ProbeResult:
    """Run the capability probe against a model.

    Args:
        provider: The LLM provider to use.
        model_id: The model identifier to probe.
        provider_name: Provider name for the capability profile (e.g., "ollama", "anthropic").

    Returns:
        A ProbeResult with the graded capabilities.
    """
    start = time.monotonic()
    try:
        response: LLMResponse = await provider.complete(
            _PROBE_PROMPT,
            system=_PROBE_SYSTEM,
            max_tokens=256,
            temperature=0.0,
            model=model_id,
        )
    except Exception as e:
        elapsed = (time.monotonic() - start) * 1000
        logger.warning("probe failed for %s: %s", model_id, e)
        return ProbeResult(
            model_id=model_id,
            provider=provider_name,
            capabilities=ModelCapabilities(
                model_id=model_id,
                provider=provider_name,
                source="probed",
                notes=f"probe failed: {e}",
            ),
            raw_response="",
            latency_ms=elapsed,
            success=False,
            error=str(e),
        )

    elapsed = (time.monotonic() - start) * 1000

    instruction_grade, json_grade = _grade_response(response.content)

    # Build capability profile from probe results
    caps = ModelCapabilities(
        model_id=model_id,
        provider=provider_name or (response.provider_name or "unknown"),
        instruction_following=instruction_grade,
        json_mode=json_grade,
        source="probed",
        notes=f"probed in {elapsed:.0f}ms, score based on structured output test",
    )

    logger.info(
        "probe complete for %s: instruction=%s, json=%s, latency=%.0fms",
        model_id,
        instruction_grade,
        json_grade,
        elapsed,
    )

    return ProbeResult(
        model_id=model_id,
        provider=provider_name,
        capabilities=caps,
        raw_response=response.content,
        latency_ms=elapsed,
        success=True,
    )
