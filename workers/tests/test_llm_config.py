import pytest
import pytest_asyncio  # noqa: F401 — registers asyncio mode

from workers.common.config import SUPPORTED_LLM_PROVIDERS, WorkerConfig
from workers.common.grpc_metadata import RuntimeLLMOverride, resolve_llm_override
from workers.common.llm.concurrency import ConcurrencyConfig, ConcurrencyGatedProvider, ProviderGateRegistry
from workers.common.llm.config import (
    _resolve_disable_thinking,
    create_llm_provider,
    create_llm_provider_for_request,
    create_report_provider,
)


def test_resolve_disable_thinking_prefers_worker_disable(monkeypatch):
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING", "true")
    monkeypatch.setenv("SOURCEBRIDGE_LLM_ENABLE_THINKING", "true")

    assert _resolve_disable_thinking() is True


def test_resolve_disable_thinking_report_scope_can_override(monkeypatch):
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING", raising=False)
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_ENABLE_THINKING", raising=False)
    monkeypatch.setenv("SOURCEBRIDGE_WORKER_LLM_REPORT_ENABLE_THINKING", "false")

    assert _resolve_disable_thinking(report=True) is True


def test_resolve_disable_thinking_global_enable_disables_flag(monkeypatch):
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_DISABLE_THINKING", raising=False)
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_ENABLE_THINKING", raising=False)
    monkeypatch.delenv("SOURCEBRIDGE_WORKER_LLM_REPORT_ENABLE_THINKING", raising=False)
    monkeypatch.setenv("SOURCEBRIDGE_LLM_ENABLE_THINKING", "true")

    assert _resolve_disable_thinking() is False


class _FakeMetadataContext:
    def __init__(self, pairs: list[tuple[str, str]]) -> None:
        self._pairs = pairs

    def invocation_metadata(self):
        return self._pairs


def test_resolve_llm_override_parses_timeout_seconds():
    """The API sets x-sb-llm-timeout-seconds from LLMConfig.TimeoutSecs so
    admins can tune the HTTP timeout without a worker restart."""
    ctx = _FakeMetadataContext(
        [
            ("x-sb-llm-provider", "openrouter"),
            ("x-sb-llm-timeout-seconds", "1200"),
        ]
    )
    override = resolve_llm_override(ctx)
    assert override is not None
    assert override.provider == "openrouter"
    assert override.timeout_seconds == 1200


def test_resolve_llm_override_ignores_invalid_timeout():
    ctx = _FakeMetadataContext(
        [
            ("x-sb-llm-provider", "openrouter"),
            ("x-sb-llm-timeout-seconds", "not-a-number"),
        ]
    )
    override = resolve_llm_override(ctx)
    assert override is not None
    assert override.timeout_seconds == 0


@pytest.mark.asyncio
async def test_create_llm_provider_for_request_passes_timeout_to_client(gate_registry):
    """End-to-end check: a request-scoped timeout reaches the HTTP client."""
    cfg = WorkerConfig(
        llm_provider="openai",
        llm_api_key="test",
        llm_model="gpt-4o",
        llm_timeout=900,
    )
    provider, model = await create_llm_provider_for_request(
        cfg,
        provider="openai",
        model="gpt-4o",
        api_key="test",
        timeout_seconds=1800,
        gate_registry=gate_registry,
    )
    # OpenAICompatProvider stores the effective timeout on the instance
    # for downstream visibility.  When wrapped, unwrap to access raw attrs.
    raw = getattr(provider, "_raw", provider)
    assert getattr(raw, "timeout", None) == 1800.0
    assert model == "gpt-4o"


@pytest.mark.asyncio
async def test_create_llm_provider_for_request_falls_back_to_bootstrap_timeout(gate_registry):
    """No per-request override → worker's bootstrap llm_timeout wins."""
    cfg = WorkerConfig(
        llm_provider="openai",
        llm_api_key="test",
        llm_model="gpt-4o",
        llm_timeout=900,
    )
    provider, _ = await create_llm_provider_for_request(
        cfg,
        provider="openai",
        model="gpt-4o",
        api_key="test",
        timeout_seconds=0,
        gate_registry=gate_registry,
    )
    raw = getattr(provider, "_raw", provider)
    assert getattr(raw, "timeout", None) == 900.0


def test_runtime_override_is_empty_when_only_default_timeout():
    """Empty override (default=0 timeout) must still be treated as empty."""
    override = RuntimeLLMOverride()
    assert override.is_empty() is True


# ─── Factory defense in depth (CA-125) ───────────────────────────────


@pytest.mark.asyncio
async def test_create_llm_provider_rejects_unknown_provider_with_actionable_message():
    """create_llm_provider catches unknown providers reaching it via
    paths that bypass the WorkerConfig validator (notably
    config.model_copy(update={'llm_provider': '...'}) used by
    create_llm_provider_for_request — pydantic v2 model_copy does NOT
    re-run validators by default).

    Tester report 2026-04-30 (Pazaryna) R2 / CA-125: pre-fix this raised
    a bare ValueError reading 'Unknown LLM provider: x' with no list
    of valid alternatives.
    """
    cfg = WorkerConfig(llm_provider="anthropic", llm_api_key="test", llm_model="claude")
    # Bypass the validator the same way per-request overrides do.
    bypassed = cfg.model_copy(update={"llm_provider": "totally-fake"})
    with pytest.raises(ValueError) as exc_info:
        await create_llm_provider(bypassed)
    msg = str(exc_info.value)
    assert "totally-fake" in msg
    # Every supported provider must be named so the user knows what to
    # switch to.
    for provider in SUPPORTED_LLM_PROVIDERS:
        assert repr(provider) in msg, f"supported provider {provider} not surfaced in error: {msg}"


# ─── Phase 2 gate-wiring tests ───────────────────────────────────────────────


@pytest.mark.asyncio
async def test_provider_is_wrapped_when_kill_switch_enabled(gate_registry):
    """create_llm_provider with a registry returns a ConcurrencyGatedProvider."""
    cfg = WorkerConfig(llm_provider="openai", llm_api_key="test", llm_model="gpt-4o")
    provider = await create_llm_provider(cfg, gate_registry=gate_registry)
    assert isinstance(provider, ConcurrencyGatedProvider)


@pytest.mark.asyncio
async def test_kill_switch_disables_wrapper(monkeypatch):
    """When SOURCEBRIDGE_LLM_CONCURRENCY_WRAPPER_ENABLED=false the raw provider is returned."""
    monkeypatch.setenv("SOURCEBRIDGE_LLM_CONCURRENCY_WRAPPER_ENABLED", "false")
    config = ConcurrencyConfig.from_env()
    assert not config.wrapper_enabled
    registry = ProviderGateRegistry(config)
    cfg = WorkerConfig(llm_provider="openai", llm_api_key="test", llm_model="gpt-4o")
    provider = await create_llm_provider(cfg, gate_registry=registry)
    assert not isinstance(provider, ConcurrencyGatedProvider)
    await registry.close()


@pytest.mark.asyncio
async def test_create_llm_provider_for_request_forwards_registry(gate_registry):
    """create_llm_provider_for_request wraps the returned provider when registry is supplied."""
    cfg = WorkerConfig(llm_provider="openai", llm_api_key="test", llm_model="gpt-4o")
    provider, model = await create_llm_provider_for_request(
        cfg,
        provider="openai",
        model="gpt-4o",
        api_key="test",
        gate_registry=gate_registry,
    )
    assert isinstance(provider, ConcurrencyGatedProvider)
    assert model == "gpt-4o"


@pytest.mark.asyncio
async def test_create_report_provider_uses_same_gate_for_same_endpoint(gate_registry):
    """Report provider and main provider share a gate when pointing at the same endpoint."""
    cfg = WorkerConfig(
        llm_provider="ollama",
        llm_model="qwen3:7b",
        llm_report_provider="ollama",
        llm_report_model="qwen3:14b",
    )
    main_prov = await create_llm_provider(cfg, gate_registry=gate_registry)
    report_prov = await create_report_provider(cfg, gate_registry=gate_registry)
    assert isinstance(main_prov, ConcurrencyGatedProvider)
    assert isinstance(report_prov, ConcurrencyGatedProvider)
    # Both point at the same Ollama host → same underlying _HostGate binding.
    assert main_prov._gate._binding is report_prov._gate._binding


# ─── CA-214: LLM base-URL SSRF guard ─────────────────────────────────────────

from workers.common.llm.config import validate_llm_base_url  # noqa: E402


@pytest.mark.parametrize(
    "url,allow_private,should_raise",
    [
        # Empty URL is always accepted.
        ("", True, False),
        ("", False, False),
        # Localhost — accepted when allow_private=True, rejected when False.
        ("http://localhost:11434/v1", True, False),
        ("http://localhost:11434/v1", False, True),
        # 127.0.0.1 loopback
        ("http://127.0.0.1:8080", True, False),
        ("http://127.0.0.1:8080", False, True),
        # RFC1918 — 10.x
        ("http://10.0.0.5:8080", True, False),
        ("http://10.0.0.5:8080", False, True),
        # RFC1918 — 172.16.x
        ("http://172.16.0.5:8080", True, False),
        ("http://172.16.0.5:8080", False, True),
        # RFC1918 — 192.168.x
        ("http://192.168.0.5:8080", True, False),
        ("http://192.168.0.5:8080", False, True),
        # Link-local / IMDS
        ("http://169.254.169.254/latest/meta-data/", True, False),
        ("http://169.254.169.254/latest/meta-data/", False, True),
        # CGNAT 100.64.x
        ("http://100.64.0.5:8080", True, False),
        ("http://100.64.0.5:8080", False, True),
        # IPv6 loopback
        ("http://[::1]:8080", True, False),
        ("http://[::1]:8080", False, True),
        # IPv6 multicast
        ("http://[ff00::1]:8080", True, False),
        ("http://[ff00::1]:8080", False, True),
        # Unspecified 0.0.0.0
        ("http://0.0.0.0:8080", True, False),
        ("http://0.0.0.0:8080", False, True),
        # Public HTTPS — always accepted (bare IP, no DNS lookup needed)
        ("https://104.18.0.1/v1", True, False),
        ("https://104.18.0.1/v1", False, False),
        # Bad scheme — always rejected
        ("ftp://example.com", True, True),
        ("ftp://example.com", False, True),
    ],
)
def test_validate_llm_base_url_matrix(url: str, allow_private: bool, should_raise: bool) -> None:
    """CA-214: accept/reject matrix mirrors pathutil.ValidateLLMBaseURL in Go."""
    if should_raise:
        with pytest.raises(ValueError):
            validate_llm_base_url(url, allow_private)
    else:
        # Must not raise.
        validate_llm_base_url(url, allow_private)
