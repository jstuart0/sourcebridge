"""OpenAI-compatible LLM adapter (works with OpenAI, Ollama, vLLM)."""

from __future__ import annotations

import re
from collections.abc import AsyncIterator

import openai
import structlog

from workers.common.llm.provider import LLMResponse

log = structlog.get_logger()

# When a response comes back with stop_reason=length AND empty visible content
# (all tokens were consumed by <think> output), retry once with a doubled
# max_tokens. This ceiling prevents runaway token budgets when the retry
# itself is also dominated by think tokens.
_RETRY_MAX_TOKENS_CEILING = 16384

# Injected as a suffix to the system prompt on the empty-content retry so
# the model receives the most direct possible instruction to skip reasoning.
_RETRY_NO_THINK_SUFFIX = (
    "\n\nIMPORTANT: Output only the requested content. "
    "Do not use any internal reasoning, scratch space, or `<think>` tags. "
    "Begin your response with the answer directly."
)


def _strip_think_tags(text: str) -> str:
    """Strip <think>…</think> (and <thinking>…</thinking>) blocks from output.

    Handles:
    - Mixed case: <Think>, <THINK>, <Thinking>, etc.
    - Multiple blocks in one response.
    - Unclosed blocks at end-of-string (model truncated mid-thought) — the
      opening tag and everything after it is dropped.

    This is applied to every response unconditionally. Thinking tokens are
    universally undesirable in SourceBridge's hot paths regardless of which
    model produced them.
    """
    # First pass: remove fully-closed blocks (greedy-stop so multiple blocks
    # in one response are all stripped).
    text = re.sub(r"<think(?:ing)?>.*?</think(?:ing)?>", "", text, flags=re.DOTALL | re.IGNORECASE)
    # Second pass: drop any remaining unclosed opening tag through end-of-string.
    text = re.sub(r"<think(?:ing)?>.+", "", text, flags=re.DOTALL | re.IGNORECASE)
    return text.strip()


# Qwen 3 / 3.5 honor a `/no_think` directive in the user message to
# skip the reasoning block. llama.cpp also understands this (via the
# chat template's enable_thinking jinja var), but Ollama ignores
# `chat_template_kwargs={"enable_thinking": False}` entirely — it
# just renders the chat template verbatim, leaving thinking on. The
# directive is the only portable way to disable thinking on Ollama,
# and it's harmless on llama.cpp because the template still honors
# the kwarg and the `/no_think` token is treated as a model-level
# directive, not leaked into the output. Scoping to the Qwen family
# (by model-id prefix match) avoids poisoning other models' prompts
# with a string they have no rule to interpret.
_QWEN_MODEL_PREFIXES = ("qwen3", "qwen-3", "qwen3.5", "qwen-3.5", "qwen3.6", "qwen-3.6")
_NO_THINK_TOKEN = "/no_think"


def _is_qwen_thinking_model(model: str | None) -> bool:
    """True when the model id looks like a Qwen 3/3.5/3.6 variant."""
    if not model:
        return False
    m = model.lower()
    return any(m.startswith(p) for p in _QWEN_MODEL_PREFIXES)


def _maybe_inject_no_think(
    messages: list[dict[str, str]],
    *,
    model: str,
    disable_thinking: bool,
) -> list[dict[str, str]]:
    """Append `/no_think` to the last user message for Qwen models.

    Mutation is scoped:
      - only when `disable_thinking` is True (caller opted in),
      - only when the target model is a Qwen 3.x reasoning variant,
      - only when the last message is a user turn (system prompts
        shouldn't carry directives — some Ollama templates drop
        system messages),
      - skipped when the directive is already present so retries
        don't accumulate duplicates.

    Returns a new list; the input is not mutated.
    """
    if not disable_thinking or not _is_qwen_thinking_model(model):
        return messages
    if not messages:
        return messages
    out = [dict(m) for m in messages]
    last = out[-1]
    if last.get("role") != "user":
        return messages
    content = last.get("content") or ""
    if _NO_THINK_TOKEN in content:
        return messages
    last["content"] = f"{content}\n\n{_NO_THINK_TOKEN}"
    return out


def _normalize_api_key(provider_name: str | None, api_key: str) -> str:
    """Normalize auth for OpenAI-compatible backends.

    Local OpenAI-compatible servers like Ollama, LM Studio, llama.cpp, vLLM,
    and SGLang commonly do not require authentication. Passing the historical
    placeholder ``not-needed`` causes some servers or proxies to reject the
    request. Keep explicit credentials intact, but strip well-known dummy
    placeholders for these local/self-hosted providers.
    """

    normalized = (api_key or "").strip()
    if normalized == "":
        return ""

    provider = (provider_name or "").strip().lower()
    if provider in {"ollama", "lmstudio", "llama-cpp", "vllm", "sglang"} and normalized.lower() in {
        "not-needed",
        "none",
        "dummy",
    }:
        return ""
    return normalized


class OpenAICompatProvider:
    """OpenAI-compatible LLM provider."""

    def __init__(
        self,
        api_key: str = "",
        model: str = "gpt-4o",
        base_url: str | None = None,
        extra_headers: dict[str, str] | None = None,
        draft_model: str | None = None,
        provider_name: str | None = None,
        disable_thinking: bool = False,
        timeout: float | None = None,
    ) -> None:
        normalized_api_key = _normalize_api_key(provider_name, api_key)
        # Default of 900s (15 min) matches WorkerConfig.llm_timeout and is
        # tuned for slow local models (qwen3:32b, MoEs, large thinking
        # models). Callers can pass an explicit timeout sourced from the
        # admin-configured TimeoutSecs value.
        effective_timeout = 900.0 if timeout is None or timeout <= 0 else float(timeout)
        self.client = openai.AsyncOpenAI(
            api_key=normalized_api_key,
            base_url=base_url,
            timeout=effective_timeout,
            default_headers=extra_headers or {},
        )
        self.model = model
        self.draft_model = draft_model
        self.provider_name = provider_name
        self.disable_thinking = disable_thinking
        self.timeout = effective_timeout

    @property
    def default_model(self) -> str:
        """Return the default model ID."""
        return self.model

    def _request_metadata(
        self,
        *,
        use_model: str,
        extra_body: dict[str, object] | None,
        operation: str,
    ) -> dict[str, object]:
        chat_template_kwargs = extra_body.get("chat_template_kwargs") if extra_body else None
        draft_model = extra_body.get("draft_model") if extra_body else None
        enable_thinking = None
        if isinstance(chat_template_kwargs, dict):
            enable_thinking = chat_template_kwargs.get("enable_thinking")
        return {
            "operation": operation,
            "provider": self.provider_name or "openai-compatible",
            "model": use_model,
            "base_url": str(self.client.base_url),
            "disable_thinking": self.disable_thinking,
            "enable_thinking_override": enable_thinking,
            "draft_model": draft_model,
        }

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        frequency_penalty: float = 0.0,
        model: str | None = None,
    ) -> LLMResponse:
        """Generate a completion.

        Includes two layers of defense against thinking-model derailment:
        1. <think> blocks are stripped from every response unconditionally.
        2. If stop_reason is "length" and the post-strip content is empty, a
           single retry fires with doubled max_tokens and a stronger no-think
           system-prompt suffix. If the retry also returns empty, the empty
           response is returned so callers (e.g. require_nonempty) can handle it.
        """
        use_model = model or self.model

        extra_body: dict[str, object] = {}
        if self.draft_model:
            extra_body["draft_model"] = self.draft_model
        # Two-pronged "disable thinking" that works for both llama.cpp
        # and Ollama:
        #   1. `chat_template_kwargs={"enable_thinking": False}` — llama.cpp
        #      extension, toggles the Jinja template variable in the chat
        #      template. Ignored (and not an error) on Ollama / OpenAI /
        #      Anthropic.
        #   2. `/no_think` suffix on the last user message — Qwen 3.x
        #      model-level directive, honored on Ollama. Scoped to Qwen
        #      so other models don't see a stray directive string.
        # Using both makes either backend work with no runtime detection.
        if self.disable_thinking:
            extra_body["chat_template_kwargs"] = {"enable_thinking": False}

        result = await self._complete_once(
            prompt=prompt,
            system=system,
            max_tokens=max_tokens,
            temperature=temperature,
            frequency_penalty=frequency_penalty,
            use_model=use_model,
            extra_body=extra_body,
        )

        # Auto-retry: when stop_reason=length AND visible content is empty the
        # model burned its entire budget on <think> tokens. Double the budget
        # (up to the ceiling) and inject a harder no-think suffix so the model
        # starts with the answer rather than reasoning first.
        if result.stop_reason == "length" and not result.content.strip():
            retry_max_tokens = min(max_tokens * 2, _RETRY_MAX_TOKENS_CEILING)
            log.warning(
                "llm_empty_content_retry",
                model=use_model,
                original_max_tokens=max_tokens,
                retry_max_tokens=retry_max_tokens,
                provider=self.provider_name or "openai-compatible",
            )
            retry_system = system + _RETRY_NO_THINK_SUFFIX
            retry_result = await self._complete_once(
                prompt=prompt,
                system=retry_system,
                max_tokens=retry_max_tokens,
                temperature=temperature,
                frequency_penalty=frequency_penalty,
                use_model=use_model,
                extra_body=extra_body,
            )
            if retry_result.content.strip():
                return retry_result
            # Both attempts empty — surface the original so callers get the
            # real stop_reason and token counts for diagnostics.

        return result

    async def _complete_once(
        self,
        *,
        prompt: str,
        system: str,
        max_tokens: int,
        temperature: float,
        frequency_penalty: float,
        use_model: str,
        extra_body: dict[str, object],
    ) -> LLMResponse:
        """Single (non-retrying) completion call; shared by complete() and retry."""
        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})
        messages = _maybe_inject_no_think(
            messages, model=use_model, disable_thinking=self.disable_thinking
        )

        log.info(
            "llm_request_dispatch",
            **self._request_metadata(
                use_model=use_model,
                extra_body=extra_body or None,
                operation="complete",
            ),
        )

        response = await self.client.chat.completions.create(
            model=use_model,
            messages=messages,  # type: ignore[arg-type]
            max_tokens=max_tokens,
            temperature=temperature,
            frequency_penalty=frequency_penalty,
            extra_body=extra_body or None,
        )
        choice = response.choices[0]

        # Extract performance metrics from server-specific response extensions
        tokens_per_second: float | None = None
        generation_time_ms: float | None = None
        acceptance_rate: float | None = None

        # llama-server includes 'timings' in the response
        # vLLM/SGLang may include timing in usage extensions
        # LM Studio includes stats in the response
        raw = response.model_extra or {}
        if "timings" in raw:
            timings = raw["timings"]
            tokens_per_second = timings.get("predicted_per_second")
            if "predicted_ms" in timings:
                generation_time_ms = timings["predicted_ms"]
            acceptance_rate = timings.get("acceptance_rate")
        elif "usage" in raw and isinstance(raw["usage"], dict):
            usage_ext = raw["usage"]
            tokens_per_second = usage_ext.get("tokens_per_second")
            generation_time_ms = usage_ext.get("total_time_ms")

        # LM Studio: compute acceptance_rate from draft token counts
        if acceptance_rate is None and "stats" in raw:
            stats = raw["stats"]
            tokens_per_second = tokens_per_second or stats.get("tokens_per_second")
            accepted = stats.get("accepted_draft_tokens_count")
            total = stats.get("total_draft_tokens_count")
            if accepted is not None and total and total > 0:
                acceptance_rate = accepted / total

        raw_content = choice.message.content or ""
        if raw_content and re.search(r"<think(?:ing)?>", raw_content, re.IGNORECASE):
            log.warning(
                "llm_response_contained_think_tags",
                provider=self.provider_name or "openai-compatible",
                model=use_model,
            )
        content = _strip_think_tags(raw_content) if raw_content else ""

        return LLMResponse(
            content=content,
            model=use_model,
            input_tokens=response.usage.prompt_tokens if response.usage else 0,
            output_tokens=response.usage.completion_tokens if response.usage else 0,
            stop_reason=choice.finish_reason or "",
            tokens_per_second=tokens_per_second,
            generation_time_ms=generation_time_ms,
            acceptance_rate=acceptance_rate,
            provider_name=self.provider_name,
        )

    async def stream(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> AsyncIterator[str]:
        """Stream a completion."""
        use_model = model or self.model
        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        extra_body: dict[str, object] | None = None
        if self.draft_model:
            extra_body = {"draft_model": self.draft_model}
        # Mirror the two-pronged disable-thinking strategy used in
        # complete(): kwarg for llama.cpp, `/no_think` directive for
        # Ollama-served Qwen models. See the comment in complete().
        if self.disable_thinking:
            extra_body = dict(extra_body or {})
            extra_body["chat_template_kwargs"] = {"enable_thinking": False}
        messages = _maybe_inject_no_think(
            messages, model=use_model, disable_thinking=self.disable_thinking
        )

        log.info(
            "llm_request_dispatch",
            **self._request_metadata(
                use_model=use_model,
                extra_body=extra_body,
                operation="stream",
            ),
        )

        stream = await self.client.chat.completions.create(
            model=use_model,
            messages=messages,  # type: ignore[arg-type]
            max_tokens=max_tokens,
            temperature=temperature,
            stream=True,
            extra_body=extra_body,
        )
        async for chunk in stream:
            if chunk.choices and chunk.choices[0].delta.content:
                yield chunk.choices[0].delta.content
