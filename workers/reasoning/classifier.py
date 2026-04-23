"""LLM-backed question classifier for the agentic QA loop.

Quality-push Phase 2. Runs a cheap Haiku call to produce the
canonical question class plus evidence-kind hints + advisory
symbol/file/topic candidates. The Go orchestrator uses these
to skew the seed context so the agentic loop starts with higher-
quality hypotheses than a keyword-only classifier gives.

Fail-open: if Haiku isn't available or returns garbage, the
caller falls back to the Go-side keyword classifier — a valid
default that just lacks the candidate fields.
"""

from __future__ import annotations

import json
import logging
import re
from dataclasses import dataclass, field
from typing import Any

from workers.common.llm.provider import LLMProvider, require_nonempty

log = logging.getLogger(__name__)


# Haiku model id. Falls back gracefully if not available — the caller
# passes in whichever provider is configured.
DEFAULT_CLASSIFIER_MODEL = "claude-haiku-4-5"

# Canonical class strings. Must match QuestionKind values on the Go
# side (see internal/qa/classifier.go) exactly.
CANONICAL_CLASSES = frozenset(
    {
        "architecture",
        "behavior",
        "execution_flow",
        "cross_cutting",
        "ownership",
        "requirement_coverage",
        "data_model",
        "risk_review",
    }
)


@dataclass
class ClassificationResult:
    """Structured output from the LLM classifier."""

    question_class: str
    needs_call_graph: bool = False
    needs_requirements: bool = False
    needs_tests: bool = False
    needs_summaries: bool = False
    symbol_candidates: list[str] = field(default_factory=list)
    file_candidates: list[str] = field(default_factory=list)
    topic_terms: list[str] = field(default_factory=list)
    input_tokens: int = 0
    output_tokens: int = 0
    model: str = ""


_SYSTEM_PROMPT = """You are a question-classifier for a codebase QA assistant.

Given a user question about a codebase, produce a STRICT JSON object
with exactly these fields. No prose, no markdown, no code fences —
the output must be parseable as JSON directly.

{
  "question_class": "<one of: architecture | behavior | execution_flow | cross_cutting | ownership | requirement_coverage | data_model | risk_review>",
  "needs_call_graph":   <true|false — would callers/callees help?>,
  "needs_requirements": <true|false — is this about a compliance/spec item?>,
  "needs_tests":        <true|false — are unit tests likely to be ground-truth evidence?>,
  "needs_summaries":    <true|false — would module-level summaries help?>,
  "symbol_candidates":  [<up to 6 plausible function/type names mentioned or implied>],
  "file_candidates":    [<up to 6 plausible file path substrings>],
  "topic_terms":        [<up to 6 normalized search terms, lowercased>]
}

Class hints:
- architecture: high-level, subsystem relationships, diagrams, 1000-foot view.
- behavior: what does X do / what happens under condition Y.
- execution_flow: trace of a request/job/command through the stack.
- cross_cutting: concern that spans multiple files/layers (auth,
  caching, rate limiting, tenancy, observability, security).
- ownership: who/what owns X; where is X defined.
- requirement_coverage: questions about compliance or spec items.
- data_model: schema, tables, entity relationships.
- risk_review: bug-hunt, vulnerability, unsafe-usage.

Be conservative with needs_* flags — only set true when the
evidence kind is CLEARLY going to help. Default false.

Output JSON only. No prose."""


_USER_PROMPT_TEMPLATE = """Question: {question}
{optional_context}"""


def _build_user_prompt(
    question: str, file_path: str, pinned_code: str
) -> str:
    optional_parts: list[str] = []
    if file_path:
        optional_parts.append(f"Pinned file: {file_path}")
    if pinned_code:
        # Cap at 800 chars — enough to hint, not enough to drive cost.
        snippet = pinned_code[:800]
        optional_parts.append(f"Pinned code (first 800 chars):\n{snippet}")
    optional_context = "\n\n".join(optional_parts)
    return _USER_PROMPT_TEMPLATE.format(
        question=question,
        optional_context=optional_context if optional_context else "",
    ).strip()


_JSON_BLOCK_RE = re.compile(r"\{.*\}", re.DOTALL)


def _parse_classifier_json(raw: str) -> dict[str, Any]:
    """Parse the classifier's JSON output, tolerating stray prose on
    either side. Raises ValueError on unrecoverable garbage."""
    # Fast path: entire output is JSON.
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        pass
    # Lenient: find the outermost {...} block.
    match = _JSON_BLOCK_RE.search(raw)
    if not match:
        raise ValueError(f"classifier output has no JSON block: {raw[:200]}")
    return json.loads(match.group(0))


def _coerce_bool(v: Any) -> bool:
    if isinstance(v, bool):
        return v
    if isinstance(v, str):
        return v.strip().lower() in ("true", "1", "yes")
    return bool(v)


def _coerce_str_list(v: Any, cap: int = 6) -> list[str]:
    if not isinstance(v, list):
        return []
    out: list[str] = []
    for item in v:
        if not isinstance(item, str):
            continue
        item = item.strip()
        if item:
            out.append(item)
        if len(out) >= cap:
            break
    return out


async def classify_question(
    provider: LLMProvider,
    *,
    question: str,
    file_path: str = "",
    pinned_code: str = "",
    model: str | None = None,
) -> ClassificationResult:
    """Run the Haiku classifier. Raises on provider error; callers
    should catch and fall back to the keyword classifier.

    `model` may be passed to override the default Haiku model (e.g.
    when the deployment doesn't have Haiku access). In that case the
    caller's model is used and the prompt still produces well-formed
    JSON on any reasonable Claude / GPT-4-class model.
    """
    system = _SYSTEM_PROMPT
    prompt = _build_user_prompt(question, file_path, pinned_code)
    resp = await provider.complete(
        prompt=prompt,
        system=system,
        max_tokens=512,
        temperature=0.0,
        model=model or DEFAULT_CLASSIFIER_MODEL,
    )
    resp = require_nonempty(resp, context="classify_question")
    parsed = _parse_classifier_json(resp.content)

    question_class = str(parsed.get("question_class", "")).strip().lower()
    if question_class not in CANONICAL_CLASSES:
        log.warning(
            "classify_question_unknown_class",
            extra={"raw_class": question_class},
        )
        question_class = "behavior"  # safe default

    return ClassificationResult(
        question_class=question_class,
        needs_call_graph=_coerce_bool(parsed.get("needs_call_graph")),
        needs_requirements=_coerce_bool(parsed.get("needs_requirements")),
        needs_tests=_coerce_bool(parsed.get("needs_tests")),
        needs_summaries=_coerce_bool(parsed.get("needs_summaries")),
        symbol_candidates=_coerce_str_list(parsed.get("symbol_candidates")),
        file_candidates=_coerce_str_list(parsed.get("file_candidates")),
        topic_terms=_coerce_str_list(parsed.get("topic_terms")),
        input_tokens=resp.input_tokens or 0,
        output_tokens=resp.output_tokens or 0,
        model=resp.model or (model or DEFAULT_CLASSIFIER_MODEL),
    )
