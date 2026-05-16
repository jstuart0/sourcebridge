"""OpenAI-compatible LLM adapter (works with OpenAI, Ollama, vLLM)."""

from __future__ import annotations

import json
import re
from collections.abc import AsyncIterator

import httpx
import openai
import structlog

from workers.common.llm.concurrency import is_local_provider
from workers.common.llm.provider import LLMResponse
from workers.common.llm.rebind_guard import RebindGuardedTransport

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


# Empirical finding (2026-05-06): ALL of the following suppression strategies
# are silently ignored by Ollama's OpenAI-compat shim (/v1/chat/completions):
#
#   - chat_template_kwargs={"enable_thinking": False}   (llama.cpp extension)
#   - /no_think suffix in user message                  (Qwen model directive)
#   - /no_think in system message                       (same)
#   - top-level think: false                            (swallowed by shim)
#   - extra_body.think: false                           (also swallowed)
#
# The ONLY working knob on Ollama ≥0.6.0 is `think: false` at the top level
# of the NATIVE /api/chat endpoint. That path is taken when provider_name ==
# "ollama" and disable_thinking is True. All other providers and all other
# configurations continue through the OpenAI-compat path.
#
# For llama.cpp (and compatible servers): chat_template_kwargs={"enable_thinking":
# False} still works. It is still set in the extra_body for non-Ollama providers
# so those users are unaffected.
#
# References:
#   https://github.com/ollama/ollama/issues/10456
#   https://docs.ollama.com/capabilities/thinking
#   thoughts/shared/investigations/2026-05-06-ollama-think-suppression-empirical.md


def _ollama_native_base_url(base_url: str) -> str:
    """Derive the Ollama root URL from an OpenAI-compat base_url.

    Strips a trailing '/v1' (with or without trailing slash) so that
    '/v1/chat/completions' → '' and we can append '/api/chat'.

    Examples:
        'http://host:11434/v1'  → 'http://host:11434'
        'http://host:11434/v1/' → 'http://host:11434'
        'http://host:11434'     → 'http://host:11434'
    """
    url = base_url.rstrip("/")
    if url.endswith("/v1"):
        url = url[:-3]
    return url


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
    if is_local_provider(provider) and normalized.lower() in {
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
        allow_private_base_url: bool = True,
    ) -> None:
        normalized_api_key = _normalize_api_key(provider_name, api_key)
        # openai>=2.34 rejects an empty api_key at construction time even for
        # local providers that never use auth headers. Use a sentinel that
        # won't be transmitted: local servers accept any non-empty string.
        client_api_key = normalized_api_key or "local-no-auth-required"
        # Default of 900s (15 min) matches WorkerConfig.llm_timeout and is
        # tuned for slow local models (qwen3:32b, MoEs, large thinking
        # models). Callers can pass an explicit timeout sourced from the
        # admin-configured TimeoutSecs value.
        effective_timeout = 900.0 if timeout is None or timeout <= 0 else float(timeout)
        # X-H2: DNS rebind guard — re-validates the resolved IP on every request.
        # allow_private mirrors SOURCEBRIDGE_WORKER_LLM_ALLOW_PRIVATE_BASE_URL so
        # Ollama/vLLM operators (allow_private=True, the default) can still reach
        # their private-network endpoints while cloud-metadata IPs are always blocked.
        _transport = RebindGuardedTransport(allow_private=allow_private_base_url)
        _http_client = httpx.AsyncClient(
            transport=_transport,
            timeout=effective_timeout,
        )
        self.client = openai.AsyncOpenAI(
            api_key=client_api_key,
            base_url=base_url,
            timeout=effective_timeout,
            default_headers=extra_headers or {},
            max_retries=0,  # Phase 3: SDK retry disabled; tenacity owns retry (Decision 3)
            http_client=_http_client,
        )
        self.model = model
        self.draft_model = draft_model
        self.provider_name = provider_name
        self.disable_thinking = disable_thinking
        self.timeout = effective_timeout
        # Retained separately so _complete_ollama_native / _stream_ollama_native
        # can derive the root URL without going through the openai client object.
        self._base_url = base_url or ""

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

        Attempt budget: a single shared counter caps total attempts at 3
        (initial call + up to 2 retries). Shared-budget pattern is documented
        here so future streaming-retry redesign cannot accidentally double it.
        (Streaming retry redesign deferred — see plan §D6 / M1.)
        """
        import time as _time

        use_model = model or self.model

        extra_body: dict[str, object] = {}
        if self.draft_model:
            extra_body["draft_model"] = self.draft_model
        # For llama.cpp and compatible servers: chat_template_kwargs toggles
        # the Jinja template variable that disables the <think> block. This is
        # NOT honored by Ollama's OpenAI-compat shim (see module-level comment).
        # Ollama with disable_thinking branches below to _complete_ollama_native.
        provider = (self.provider_name or "").strip().lower()
        if self.disable_thinking and provider != "ollama":
            extra_body["chat_template_kwargs"] = {"enable_thinking": False}

        # Ollama-specific path: when disable_thinking is requested, use the
        # native /api/chat endpoint with top-level think: false. The OpenAI-
        # compat shim silently ignores all suppression strategies (empirically
        # confirmed 2026-05-06). Everything else, including Ollama WITH thinking
        # enabled, uses the standard OpenAI-compat path below.
        if provider == "ollama" and self.disable_thinking:
            return await self._complete_ollama_native(
                prompt=prompt,
                system=system,
                max_tokens=max_tokens,
                temperature=temperature,
                use_model=use_model,
            )

        t0 = _time.monotonic()
        result = await self._complete_once(
            prompt=prompt,
            system=system,
            max_tokens=max_tokens,
            temperature=temperature,
            frequency_penalty=frequency_penalty,
            use_model=use_model,
            extra_body=extra_body,
        )
        original_latency_ms = int((_time.monotonic() - t0) * 1000)

        # Auto-retry: when stop_reason=length AND visible content is empty the
        # model burned its entire budget on <think> tokens. Double the budget
        # (up to the ceiling) and inject a harder no-think suffix so the model
        # starts with the answer rather than reasoning first.
        #
        # Attempt budget (H3): shared counter across the entire chain; capped at
        # 3 total (initial + up to 2 retries). This is the unary path only;
        # streaming retry redesign is deferred (plan §M1).
        attempt_budget = 3
        attempt_budget -= 1  # consumed by the initial call above

        if result.stop_reason == "length" and not result.content.strip():
            attempt_budget -= 1  # consuming attempt 2
            retry_max_tokens = min(max_tokens * 2, _RETRY_MAX_TOKENS_CEILING)
            t_retry = _time.monotonic()
            log.warning(
                "llm_empty_content_retry",
                attempt=2,
                strategy="max_tokens_double",
                model=use_model,
                original_max_tokens=max_tokens,
                retry_max_tokens=retry_max_tokens,
                original_latency_ms=original_latency_ms,
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
            retry_latency_ms = int((_time.monotonic() - t_retry) * 1000)
            if retry_result.content.strip():
                log.info(
                    "llm_empty_content_retry_recovered",
                    attempt=2,
                    strategy="max_tokens_double",
                    retry_latency_ms=retry_latency_ms,
                    model=use_model,
                    provider=self.provider_name or "openai-compatible",
                )
                return retry_result

            # Second attempt also empty; budget allows one more attempt.
            if attempt_budget > 0:
                attempt_budget -= 1  # consuming attempt 3
                # Third attempt: surface the original (no further retries).
                # Callers (e.g. require_nonempty) receive the real stop_reason
                # and token counts for diagnostics.
                log.warning(
                    "llm_empty_content_retry",
                    attempt=3,
                    strategy="surface_empty",
                    model=use_model,
                    original_max_tokens=retry_max_tokens,
                    retry_max_tokens=retry_max_tokens,
                    original_latency_ms=retry_latency_ms,
                    provider=self.provider_name or "openai-compatible",
                )

        return result

    async def _complete_ollama_native(
        self,
        *,
        prompt: str,
        system: str,
        max_tokens: int,
        temperature: float,
        use_model: str,
    ) -> LLMResponse:
        """POST to Ollama's native /api/chat with think: false.

        The OpenAI-compat shim (/v1/chat/completions) silently ignores all
        thinking-suppression strategies. The native endpoint is the only knob
        that works. Returns the same LLMResponse shape as _complete_once so
        callers are transport-agnostic.

        Native response shape differs from OpenAI:
          message.content            (not choices[0].message.content)
          done_reason                (not finish_reason)
          eval_count                 (not usage.completion_tokens)
          prompt_eval_count          (not usage.prompt_tokens)
        """
        import time as _time

        root = _ollama_native_base_url(self._base_url)
        url = f"{root}/api/chat"

        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        body: dict[str, object] = {
            "model": use_model,
            "messages": messages,
            "stream": False,
            "think": False,
            "options": {
                "num_predict": max_tokens,
                "temperature": temperature,
            },
        }

        log.info(
            "llm_request_dispatch",
            operation="complete",
            provider="ollama",
            model=use_model,
            base_url=url,
            disable_thinking=True,
            enable_thinking_override=False,
            draft_model=None,
            transport="native",
        )

        t0 = _time.monotonic()
        async with httpx.AsyncClient(timeout=self.timeout) as client:
            resp = await client.post(url, json=body)
            resp.raise_for_status()
        generation_time_ms = int((_time.monotonic() - t0) * 1000)

        data = resp.json()
        msg = data.get("message") or {}
        raw_content = msg.get("content") or ""

        if raw_content and re.search(r"<think(?:ing)?>", raw_content, re.IGNORECASE):
            log.warning(
                "llm_response_contained_think_tags",
                provider="ollama",
                model=use_model,
            )
        content = _strip_think_tags(raw_content) if raw_content else ""

        return LLMResponse(
            content=content,
            model=use_model,
            input_tokens=data.get("prompt_eval_count") or 0,
            output_tokens=data.get("eval_count") or 0,
            stop_reason=data.get("done_reason") or "",
            tokens_per_second=None,
            generation_time_ms=float(generation_time_ms),
            acceptance_rate=None,
            provider_name=self.provider_name,
        )

    async def _stream_ollama_native(
        self,
        *,
        prompt: str,
        system: str,
        max_tokens: int,
        temperature: float,
        use_model: str,
    ) -> AsyncIterator[str]:
        """Stream from Ollama's native /api/chat with think: false.

        Each newline-delimited JSON chunk has a `message.content` field
        containing the next delta. The final chunk has `done: true`. Think
        blocks (if somehow present) are stripped from each delta.
        """
        root = _ollama_native_base_url(self._base_url)
        url = f"{root}/api/chat"

        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        body: dict[str, object] = {
            "model": use_model,
            "messages": messages,
            "stream": True,
            "think": False,
            "options": {
                "num_predict": max_tokens,
                "temperature": temperature,
            },
        }

        log.info(
            "llm_request_dispatch",
            operation="stream",
            provider="ollama",
            model=use_model,
            base_url=url,
            disable_thinking=True,
            enable_thinking_override=False,
            draft_model=None,
            transport="native",
        )

        async with httpx.AsyncClient(timeout=self.timeout) as client, client.stream("POST", url, json=body) as resp:
                resp.raise_for_status()
                async for line in resp.aiter_lines():
                    if not line:
                        continue
                    try:
                        chunk = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    delta = (chunk.get("message") or {}).get("content") or ""
                    if delta:
                        yield _strip_think_tags(delta) if re.search(
                            r"<think(?:ing)?>", delta, re.IGNORECASE
                        ) else delta
                    if chunk.get("done"):
                        break

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
        """Stream a completion.

        Ollama with disable_thinking branches to _stream_ollama_native, which
        uses the native /api/chat endpoint with think: false. All other
        configurations use the OpenAI-compat path.
        """
        use_model = model or self.model
        provider = (self.provider_name or "").strip().lower()

        # Ollama native path: see module-level comment for why.
        if provider == "ollama" and self.disable_thinking:
            async for chunk in self._stream_ollama_native(
                prompt=prompt,
                system=system,
                max_tokens=max_tokens,
                temperature=temperature,
                use_model=use_model,
            ):
                yield chunk
            return

        messages: list[dict[str, str]] = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": prompt})

        extra_body: dict[str, object] | None = None
        if self.draft_model:
            extra_body = {"draft_model": self.draft_model}
        # For llama.cpp and compatible non-Ollama servers: disable <think> via
        # the chat template kwarg. Ollama with thinking enabled uses this path
        # too (thinking is on, so no suppression is needed).
        if self.disable_thinking:
            extra_body = dict(extra_body or {})
            extra_body["chat_template_kwargs"] = {"enable_thinking": False}

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
