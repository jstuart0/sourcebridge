"""Worker configuration."""

from __future__ import annotations

import os

from pydantic import field_validator
from pydantic_settings import BaseSettings

# Maximum allowed value for llm_max_concurrent_calls (D9 / H1).
# Enforced both here at config-load time and in GetProviderCapabilities on
# the response path as defense in depth. See also the DB CHECK constraint
# in Phase 5's migration.
HARD_CONCURRENCY_CEILING: int = 256

# Supported provider sets. Kept as module-level frozensets so validators,
# the factories in workers/common/{llm,embedding}/config.py, and tests
# all consume one source of truth. Adding a provider means editing this
# file plus the corresponding factory dispatch — single grep target.
#
# Tester report 2026-04-30 (Pazaryna) Issue 3 + R2 / CA-125: pre-fix,
# unknown providers reached the factory and crashed the worker mid-init
# with NotImplementedError / ValueError. The validator below rejects
# unknown values at config-load time with an actionable message naming
# the supported set.
SUPPORTED_LLM_PROVIDERS: frozenset[str] = frozenset({
    "anthropic",
    "openai",
    "ollama",
    "vllm",
    "llama-cpp",
    "sglang",
    "gemini",
    "openrouter",
    "lmstudio",
})

SUPPORTED_EMBEDDING_PROVIDERS: frozenset[str] = frozenset({
    "ollama",
    "openai",
    "openai-compatible",
})


def _coerce_provider(
    value: object,
    allowed: frozenset[str],
    label: str,
    *,
    required: bool,
) -> str:
    """Normalize and validate a provider string against ``allowed``.

    - Coerces ``value`` to a stripped ``str`` (defensively — env vars
      arrive as strings, but the validator runs in ``mode='before'``
      so anything could in theory land here).
    - Empty string: returned as-is when ``required=False`` (signals
      "fall back"); raises when ``required=True``.
    - Unknown value: raises ``ValueError`` with an actionable message
      listing the supported providers in stable alphabetical order
      and, for the embedding case, the well-known anthropic-isn't-here
      gotcha that the tester report flagged.
    """
    if value is None:
        text = ""
    else:
        text = str(value).strip()

    if not text:
        if required:
            raise ValueError(
                f"{label} is required. Set one of: {_format_supported(allowed)}."
            )
        return ""

    if text in allowed:
        return text

    msg = (
        f"{label} {text!r} is not supported. "
        f"Supported {label}s: {_format_supported(allowed)}."
    )
    # Embedding-specific guidance: the tester report (Pazaryna 2026-04-30)
    # found a real user setting embedding_provider=anthropic because the
    # README's LLM-providers table listed anthropic as a first-class
    # provider. Naming the gotcha in the error itself short-circuits
    # the next user's stack-trace-spelunking session.
    if label == "embedding provider" and text == "anthropic":
        msg += (
            " Anthropic does not offer an embeddings API as of 2026; "
            "use 'ollama' (the default), 'openai', or 'openai-compatible' "
            "for a self-hosted endpoint."
        )
    raise ValueError(msg)


def _format_supported(allowed: frozenset[str]) -> str:
    """Return the supported set as a stable-ordered, quoted, comma list."""
    return ", ".join(repr(v) for v in sorted(allowed))


class WorkerConfig(BaseSettings):
    """Configuration for the SourceBridge worker process."""

    model_config = {"env_prefix": "SOURCEBRIDGE_WORKER_"}

    grpc_port: int = 50051
    max_workers: int = 10

    # LLM provider
    llm_provider: str = "anthropic"
    llm_api_key: str = ""
    llm_model: str = "claude-sonnet-4-20250514"
    llm_base_url: str = ""
    llm_draft_model: str = ""  # LM Studio only: sent as draft_model in request body
    # Per-request HTTP timeout applied to the OpenAI-compatible LLM client.
    # Large local models (qwen3:32b, qwen3.6 MoE, llama3.3:70b) can legitimately
    # take minutes per completion when the user asked for deep, grounded output.
    # 900s (15 min) is a safe ceiling that still catches hung providers.
    llm_timeout: int = 900

    # Report-specific LLM overrides (optional)
    llm_report_model: str = ""  # If set, used for report generation instead of llm_model
    llm_report_provider: str = ""  # Optional: separate LLM provider for reports
    llm_report_api_key: str = ""  # API key for report provider (if different)
    llm_report_base_url: str = ""  # Base URL for report provider (if different)

    # Report validation
    llm_validation_model: str = ""  # Model for report validation (can be cheaper/faster)
    report_validation_enabled: bool = False  # Enable validation pass after generation

    # Embedding provider
    embedding_provider: str = "ollama"
    embedding_api_key: str = ""
    embedding_model: str = "nomic-embed-text"
    embedding_dimension: int = 768
    embedding_base_url: str = ""

    # SurrealDB
    surreal_url: str = "ws://localhost:8000/rpc"
    surreal_namespace: str = "sourcebridge"
    surreal_database: str = "sourcebridge"
    surreal_user: str = "root"
    surreal_pass: str = "root"

    # Test mode
    test_mode: bool = False

    # Debug mode: gates gRPC server reflection (SEC-11).
    # When False (the default), grpc-reflection is not registered — the
    # service schema is not advertised to unauthenticated callers, which
    # prevents enumeration of internal RPC methods in production.
    # Set SOURCEBRIDGE_WORKER_DEBUG=true in dev compose to keep grpcurl
    # working locally.
    debug: bool = False

    # Operator-declared upstream LLM parallelism.
    # 0 = unknown/unbounded (default; Go orchestrator does not clamp).
    # 1..256 = clamp orchestrator MaxConcurrency to this value.
    # Seeded on first boot from SOURCEBRIDGE_LLM_PARALLEL_HINT when the
    # DB profile row's max_concurrent_calls IS NULL (see Phase 5 migration).
    # Never overrides an operator-set DB value. Config-level env var:
    # SOURCEBRIDGE_WORKER_LLM_MAX_CONCURRENT_CALLS (or the shared hint).
    #
    # LEGACY SEED (Phase 2+): The ProviderGateRegistry is the runtime owner
    # of the effective LLM concurrency cap.  This field is used only as the
    # fallback seed when the gate's per-provider override is absent AND the
    # kill switch is off.  GetProviderCapabilities sources its cap from the
    # registry's effective value, not from this field directly.
    # See plan 2026-05-06-deliver-worker-llm-concurrency Decision 12.
    llm_max_concurrent_calls: int = 0

    # gRPC auth
    grpc_auth_secret: str = ""

    # gRPC mTLS (slice 4 of plan 2026-04-29-workspace-llm-source-of-truth-r2.md).
    # When tls_enabled is true and all three paths are valid, the worker
    # binds with grpc.ssl_server_credentials and require_client_auth=True;
    # the API client must present a cert signed by tls_ca_path's CA.
    # Default false → legacy add_insecure_port path (OSS dev compat).
    tls_enabled: bool = False
    tls_cert_path: str = ""
    tls_key_path: str = ""
    tls_ca_path: str = ""

    # Shutdown drain ceiling. R3 followups T1.8: when SIGTERM arrives,
    # the worker flips its health-servicer aggregate to NOT_SERVING (so
    # the kubelet's gRPC readiness probe stops directing new work to
    # this pod) and then calls grpc.aio.Server.stop(grace=N) where N is
    # this value. Default 3600s (60 minutes) — matches
    # TimeoutKnowledgeRepository on the Go client side, so the longest
    # legitimate in-flight RPC has time to finish naturally during a
    # cert-rotation rolling restart.
    #
    # The Kubernetes terminationGracePeriodSeconds on the worker
    # Deployment must exceed this value so the kubelet's SIGKILL is
    # the outer bound, not the inner one. Today that's 3900s (65min).
    shutdown_grace_seconds: int = 3600

    @field_validator("llm_max_concurrent_calls", mode="before")
    @classmethod
    def _validate_llm_max_concurrent_calls(cls, v: object) -> int:
        """Clamp and validate the declared parallelism at config-load time.

        Accepts int or str (env vars arrive as strings). Values outside
        [0, HARD_CONCURRENCY_CEILING] are rejected with an actionable message.
        """
        try:
            val = int(v)
        except (TypeError, ValueError) as err:
            raise ValueError(
                f"llm_max_concurrent_calls must be an integer, got {v!r}."
            ) from err
        if val < 0 or val > HARD_CONCURRENCY_CEILING:
            raise ValueError(
                f"llm_max_concurrent_calls must be between 0 and {HARD_CONCURRENCY_CEILING}, got {val}."
            )
        return val

    @field_validator("llm_provider", mode="before")
    @classmethod
    def _validate_llm_provider(cls, v: object) -> str:
        """Reject unknown LLM providers at config-load time with an
        actionable message naming the supported set. Empty string is
        rejected here — llm_provider is required.

        See SUPPORTED_LLM_PROVIDERS for the canonical list. Adding a new
        provider requires editing both the set and the factory dispatch
        in workers/common/llm/config.py:create_llm_provider.
        """
        return _coerce_provider(v, SUPPORTED_LLM_PROVIDERS, "LLM provider", required=True)

    @field_validator("embedding_provider", mode="before")
    @classmethod
    def _validate_embedding_provider(cls, v: object) -> str:
        """Reject unknown embedding providers at config-load time.

        Pre-fix, setting SOURCEBRIDGE_WORKER_EMBEDDING_PROVIDER=anthropic
        (a reasonable guess for a user reading the LLM-providers table
        in the README) crashed the worker mid-init with NotImplementedError.
        The error here names the supported set and explains why
        Anthropic is not on it (no embeddings API as of 2026).
        """
        return _coerce_provider(v, SUPPORTED_EMBEDDING_PROVIDERS, "embedding provider", required=True)

    @field_validator("llm_report_provider", mode="before")
    @classmethod
    def _validate_llm_report_provider(cls, v: object) -> str:
        """Reject unknown report-LLM providers. Empty string is allowed
        — it signals "fall back to llm_provider for reports too" (the
        existing semantics in workers/common/llm/config.py:create_report_provider).
        """
        return _coerce_provider(v, SUPPORTED_LLM_PROVIDERS, "report LLM provider", required=False)

    def model_post_init(self, __context: object) -> None:
        # Read SOURCEBRIDGE_LLM_PARALLEL_HINT as a fallback for
        # llm_max_concurrent_calls when the primary env var is not set.
        # This matches the seed-once pattern in Phase 5's envBootstrapToLegacy
        # (Go side): the hint seeds the value only when not already set.
        if self.llm_max_concurrent_calls == 0:
            hint = os.getenv("SOURCEBRIDGE_LLM_PARALLEL_HINT", "").strip()
            if hint:
                try:
                    val = int(hint)
                    if 0 <= val <= HARD_CONCURRENCY_CEILING:
                        self.llm_max_concurrent_calls = val
                except (TypeError, ValueError):
                    pass  # malformed hint — ignore silently; Phase 5 DB migration is authoritative

        self.test_mode = self._fallback_bool_env(
            current=self.test_mode,
            primary_env="SOURCEBRIDGE_WORKER_TEST_MODE",
            fallback_env="SOURCEBRIDGE_TEST_MODE",
        )
        self.surreal_url = self._fallback_env(
            current=self.surreal_url,
            default_value="ws://localhost:8000/rpc",
            primary_env="SOURCEBRIDGE_WORKER_SURREAL_URL",
            fallback_env="SOURCEBRIDGE_STORAGE_SURREAL_URL",
        )
        self.surreal_namespace = self._fallback_env(
            current=self.surreal_namespace,
            default_value="sourcebridge",
            primary_env="SOURCEBRIDGE_WORKER_SURREAL_NAMESPACE",
            fallback_env="SOURCEBRIDGE_STORAGE_SURREAL_NAMESPACE",
        )
        self.surreal_database = self._fallback_env(
            current=self.surreal_database,
            default_value="sourcebridge",
            primary_env="SOURCEBRIDGE_WORKER_SURREAL_DATABASE",
            fallback_env="SOURCEBRIDGE_STORAGE_SURREAL_DATABASE",
        )
        self.surreal_user = self._fallback_env(
            current=self.surreal_user,
            default_value="root",
            primary_env="SOURCEBRIDGE_WORKER_SURREAL_USER",
            fallback_env="SOURCEBRIDGE_STORAGE_SURREAL_USER",
        )
        self.surreal_pass = self._fallback_env(
            current=self.surreal_pass,
            default_value="root",
            primary_env="SOURCEBRIDGE_WORKER_SURREAL_PASS",
            fallback_env="SOURCEBRIDGE_STORAGE_SURREAL_PASS",
        )

    @staticmethod
    def _fallback_env(current: str, default_value: str, primary_env: str, fallback_env: str) -> str:
        if current and current != default_value:
            return current
        primary = os.getenv(primary_env, "").strip()
        if primary:
            return primary
        fallback = os.getenv(fallback_env, "").strip()
        if fallback:
            return fallback
        return current

    @staticmethod
    def _fallback_bool_env(current: bool, primary_env: str, fallback_env: str) -> bool:
        primary = os.getenv(primary_env, "").strip()
        if primary:
            return primary.lower() in ("true", "1", "yes", "on")
        fallback = os.getenv(fallback_env, "").strip()
        if fallback:
            return fallback.lower() in ("true", "1", "yes", "on")
        return current
