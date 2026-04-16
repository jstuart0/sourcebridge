# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Model capability registry.

This is the Phase 4 in-memory version of the registry the plan
eventually persists to ``ca_model_capabilities``. It lets the
StrategySelector answer the question "can this strategy safely run on
the currently configured model?" without hitting the DB or a provider
introspection API.

The registry is deliberately small:

  - A :class:`ModelCapabilities` dataclass captures every field the
    selector actually reads today (context window, instruction
    following grade, JSON mode, long-context quality curve).
  - A set of builtin profiles seeds the registry with the models we
    actually use in production and development (Claude Sonnet/Opus/Haiku
    4.x, GPT-4.1 family, Gemini 2.5, Llama 3.3 at common context sizes,
    Qwen 2.5 Coder, Mistral, Ollama defaults).
  - :func:`lookup_capabilities` is forgiving — unknown models fall back
    to a conservative profile that assumes a small 4K context and low
    instruction following. This means "default to the safest strategy"
    rather than "fail closed" when an operator uses a model we haven't
    catalogued yet.

Phase 6 will replace the builtin seed with a DB-backed table and add
a probe routine that tests unknown models at configuration time.
Phase 4 ships just enough to make the StrategySelector actually do
something useful.
"""

from __future__ import annotations

from dataclasses import dataclass, field

# Grade values form an ordered scale: low < medium < high. Keeping
# them as strings matches the plan's data model and avoids leaking
# enum noise into settings-level JSON.
_GRADE_ORDER = {"low": 0, "medium": 1, "high": 2}


def _grade_at_least(actual: str, required: str) -> bool:
    """Return True when ``actual`` satisfies a minimum ``required`` grade."""
    return _GRADE_ORDER.get(actual.lower(), 0) >= _GRADE_ORDER.get(required.lower(), 0)


@dataclass
class ModelCapabilities:
    """What a model can actually do, distilled to the fields the
    StrategySelector reads.

    Every field has a conservative default so a partially-populated
    record (e.g. from a probe that only tested instruction following)
    still produces a usable profile.
    """

    model_id: str
    provider: str = ""
    # declared_context_tokens is what the provider reports.
    declared_context_tokens: int = 4096
    # effective_context_tokens is what we've observed to work reliably
    # — usually <= declared, especially for models with "context rot"
    # degradation on long inputs.
    effective_context_tokens: int = 4096
    # long_context_quality maps {token_count: grade}. Sparse map;
    # the lookup walks the sorted keys to find the appropriate grade
    # for a target input size.
    long_context_quality: dict[int, str] = field(default_factory=dict)
    instruction_following: str = "low"
    json_mode: str = "none"  # "none" | "prompted" | "native"
    tool_use: str = "none"  # "none" | "supported" | "native"
    extraction_grade: str = "low"
    creative_grade: str = "low"
    embedding_model: bool = False
    source: str = "builtin"  # "builtin" | "probed" | "manual"
    notes: str = ""

    def meets_context(self, min_tokens: int) -> bool:
        """Does this model's effective context satisfy the minimum?"""
        return self.effective_context_tokens >= min_tokens

    def meets_instruction_following(self, required: str) -> bool:
        return _grade_at_least(self.instruction_following, required)

    def meets_json_mode(self, required_native: bool) -> bool:
        if not required_native:
            return True
        return self.json_mode in {"native", "prompted"}

    def meets_tool_use(self, required_native: bool) -> bool:
        if not required_native:
            return True
        return self.tool_use in {"supported", "native"}

    def long_context_grade_at(self, tokens: int) -> str:
        """Return the quality grade expected at the given input size.

        Walks the long_context_quality map and returns the grade for
        the closest documented bucket at or below ``tokens``. When no
        data is available, assumes ``medium`` — conservative but not
        blocking.
        """
        if not self.long_context_quality:
            return "medium"
        best: tuple[int, str] = (0, "medium")
        for bucket, grade in self.long_context_quality.items():
            if bucket <= tokens and bucket >= best[0]:
                best = (bucket, grade)
        return best[1]


# ----------------------------------------------------------------------
# Builtin profiles
#
# Grades and context numbers are drawn from published documentation
# (Anthropic, OpenAI, Google, Meta) and recent benchmarks
# (LongCodeBench, Chroma Context Rot). Numbers are deliberately
# conservative — when in doubt we under-declare so the selector
# prefers the safer strategy.


def _seed_builtin_registry() -> dict[str, ModelCapabilities]:
    return {
        # --- Anthropic ----------------------------------------------
        "claude-opus-4-6": ModelCapabilities(
            model_id="claude-opus-4-6",
            provider="anthropic",
            declared_context_tokens=200_000,
            effective_context_tokens=180_000,
            long_context_quality={
                32_000: "high",
                128_000: "high",
                200_000: "medium",
            },
            instruction_following="high",
            json_mode="prompted",
            tool_use="native",
            extraction_grade="high",
            creative_grade="high",
        ),
        "claude-sonnet-4-6": ModelCapabilities(
            model_id="claude-sonnet-4-6",
            provider="anthropic",
            declared_context_tokens=200_000,
            effective_context_tokens=160_000,
            long_context_quality={
                32_000: "high",
                128_000: "medium",
                200_000: "medium",
            },
            instruction_following="high",
            json_mode="prompted",
            tool_use="native",
            extraction_grade="high",
            creative_grade="high",
        ),
        "claude-haiku-4-5-20251001": ModelCapabilities(
            model_id="claude-haiku-4-5-20251001",
            provider="anthropic",
            declared_context_tokens=200_000,
            effective_context_tokens=120_000,
            long_context_quality={
                32_000: "high",
                128_000: "medium",
            },
            instruction_following="high",
            json_mode="prompted",
            tool_use="native",
            extraction_grade="medium",
            creative_grade="medium",
        ),
        # --- OpenAI / GPT family -----------------------------------
        "gpt-4.1": ModelCapabilities(
            model_id="gpt-4.1",
            provider="openai",
            declared_context_tokens=200_000,
            effective_context_tokens=160_000,
            long_context_quality={32_000: "high", 128_000: "medium"},
            instruction_following="high",
            json_mode="native",
            tool_use="native",
            extraction_grade="high",
            creative_grade="high",
        ),
        "gpt-4.1-mini": ModelCapabilities(
            model_id="gpt-4.1-mini",
            provider="openai",
            declared_context_tokens=128_000,
            effective_context_tokens=96_000,
            long_context_quality={32_000: "high", 64_000: "medium"},
            instruction_following="high",
            json_mode="native",
            tool_use="native",
            extraction_grade="medium",
            creative_grade="medium",
        ),
        # --- Google / Gemini ---------------------------------------
        "gemini-2.5-pro": ModelCapabilities(
            model_id="gemini-2.5-pro",
            provider="google",
            declared_context_tokens=1_000_000,
            effective_context_tokens=500_000,
            long_context_quality={
                32_000: "high",
                128_000: "high",
                512_000: "medium",
                1_000_000: "low",
            },
            instruction_following="high",
            json_mode="native",
            tool_use="native",
            extraction_grade="high",
            creative_grade="high",
        ),
        "gemini-2.5-flash": ModelCapabilities(
            model_id="gemini-2.5-flash",
            provider="google",
            declared_context_tokens=1_000_000,
            effective_context_tokens=250_000,
            long_context_quality={
                32_000: "high",
                128_000: "medium",
                512_000: "low",
            },
            instruction_following="high",
            json_mode="native",
            tool_use="native",
            extraction_grade="medium",
            creative_grade="medium",
        ),
        # --- Meta / Llama family -----------------------------------
        "llama3.3:70b": ModelCapabilities(
            model_id="llama3.3:70b",
            provider="ollama",
            declared_context_tokens=128_000,
            effective_context_tokens=32_000,  # default ollama num_ctx
            long_context_quality={32_000: "medium", 128_000: "low"},
            instruction_following="high",
            json_mode="prompted",
            tool_use="supported",
            extraction_grade="medium",
            creative_grade="medium",
        ),
        "llama3.3:8b": ModelCapabilities(
            model_id="llama3.3:8b",
            provider="ollama",
            declared_context_tokens=128_000,
            effective_context_tokens=8_192,
            long_context_quality={8_192: "medium", 32_000: "low"},
            instruction_following="medium",
            json_mode="prompted",
            extraction_grade="low",
            creative_grade="medium",
        ),
        "llama3:latest": ModelCapabilities(
            model_id="llama3:latest",
            provider="ollama",
            declared_context_tokens=8_192,
            effective_context_tokens=4_096,
            long_context_quality={4_096: "medium"},
            instruction_following="medium",
            json_mode="prompted",
            extraction_grade="low",
            creative_grade="medium",
        ),
        # --- Qwen Coder --------------------------------------------
        "qwen2.5-coder:32b": ModelCapabilities(
            model_id="qwen2.5-coder:32b",
            provider="ollama",
            declared_context_tokens=128_000,
            effective_context_tokens=32_000,
            long_context_quality={32_000: "medium", 128_000: "low"},
            instruction_following="high",
            json_mode="prompted",
            extraction_grade="medium",
            creative_grade="medium",
        ),
        # --- Embedding models --------------------------------------
        "nomic-embed-code": ModelCapabilities(
            model_id="nomic-embed-code",
            provider="ollama",
            declared_context_tokens=8_192,
            effective_context_tokens=8_192,
            embedding_model=True,
        ),
        "voyage-code-3": ModelCapabilities(
            model_id="voyage-code-3",
            provider="voyage",
            declared_context_tokens=16_000,
            effective_context_tokens=16_000,
            embedding_model=True,
        ),
    }


# Module-level registry. Tests that need isolation can clone this via
# ModelCapabilityRegistry(overrides=...).
_BUILTIN_REGISTRY: dict[str, ModelCapabilities] = _seed_builtin_registry()


def builtin_models() -> list[ModelCapabilities]:
    """Return a copy of the builtin profile list."""
    return list(_BUILTIN_REGISTRY.values())


class ModelCapabilityRegistry:
    """Lookup service for model capabilities.

    Constructed without arguments it uses the builtin seed. Tests and
    future DB-backed code inject overrides via the ``overrides``
    parameter or the :meth:`register` method.
    """

    def __init__(self, overrides: dict[str, ModelCapabilities] | None = None) -> None:
        self._models: dict[str, ModelCapabilities] = dict(_BUILTIN_REGISTRY)
        if overrides:
            self._models.update(overrides)

    def register(self, model: ModelCapabilities) -> None:
        self._models[model.model_id] = model

    def lookup(self, model_id: str) -> ModelCapabilities:
        """Return the capability profile for ``model_id``.

        When the model is not in the registry, returns a conservative
        default profile that satisfies the hierarchical strategy floor
        (``min_context_tokens=2048``, ``instruction_following=low``)
        and nothing more. The selector interprets this as "only
        hierarchical is safe" — the safest possible behavior.
        """
        if model_id in self._models:
            return self._models[model_id]
        # Unknown model fallback.
        return ModelCapabilities(
            model_id=model_id,
            provider="unknown",
            declared_context_tokens=4096,
            effective_context_tokens=4096,
            instruction_following="low",
            json_mode="none",
            extraction_grade="low",
            creative_grade="low",
            source="fallback",
            notes="unknown model, treated as minimum-capability",
        )

    def all_models(self) -> list[ModelCapabilities]:
        return list(self._models.values())
