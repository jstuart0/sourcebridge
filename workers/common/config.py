"""Worker configuration."""

from __future__ import annotations

from pydantic_settings import BaseSettings


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
    llm_timeout: int = 30

    # Report-specific LLM overrides (optional)
    llm_report_model: str = ""       # If set, used for report generation instead of llm_model
    llm_report_provider: str = ""    # Optional: separate LLM provider for reports
    llm_report_api_key: str = ""     # API key for report provider (if different)
    llm_report_base_url: str = ""    # Base URL for report provider (if different)

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

    # gRPC auth
    grpc_auth_secret: str = ""
