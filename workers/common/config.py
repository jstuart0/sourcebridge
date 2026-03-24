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
    llm_timeout: int = 30

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
