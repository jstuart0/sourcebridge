# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

from __future__ import annotations

from workers.common.llm.provider import LLMResponse
from workers.knowledge.architecture_diagram import generate_architecture_diagram


class _SequenceProvider:
    def __init__(self, responses: list[str]) -> None:
        self._responses = responses
        self.calls: list[str] = []

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
        self.calls.append(prompt)
        content = self._responses.pop(0)
        return LLMResponse(
            content=content,
            model=model or "test-model",
            input_tokens=100,
            output_tokens=50,
            provider_name="test",
        )

    async def stream(self, *args, **kwargs):  # pragma: no cover
        raise NotImplementedError


async def test_generate_architecture_diagram_retries_invalid_mermaid() -> None:
    provider = _SequenceProvider(
        [
            "I think this system has an API and a database.",
            'flowchart LR\napi["API"] --> db["DB"]',
        ]
    )
    result, usage = await generate_architecture_diagram(
        provider,
        repository_name="Example Repo",
        audience="developer",
        depth="medium",
        snapshot_json='{"repository_name":"Example Repo"}',
        deterministic_diagram_json='{"modules":[{"path":"api","outbound_paths":["db"]}]}',
        model_override="test-model",
    )

    assert len(provider.calls) == 2
    assert result["mermaid_source"].startswith("flowchart LR")
    assert "retry regenerated invalid Mermaid" in result["repair_summary"]
    assert usage.input_tokens == 200
    assert usage.output_tokens == 100


async def test_generate_architecture_diagram_falls_back_to_system_view() -> None:
    provider = _SequenceProvider(
        [
            "This is not mermaid.",
            "Still not mermaid.",
        ]
    )
    snapshot_json = """{
      "system_components": [
        {"id": "user_interfaces", "label": "User Interfaces", "kind": "interface"},
        {"id": "api_auth", "label": "API & Auth", "kind": "service"},
        {"id": "persistence", "label": "Persistence", "kind": "storage"}
      ],
      "system_flows": [
        {"source_id": "user_interfaces", "target_id": "api_auth", "summary": "HTTP/API requests"},
        {"source_id": "api_auth", "target_id": "persistence", "summary": "reads and writes metadata"}
      ]
    }"""
    result, usage = await generate_architecture_diagram(
        provider,
        repository_name="Example Repo",
        audience="developer",
        depth="medium",
        snapshot_json=snapshot_json,
        deterministic_diagram_json='{"modules":[{"path":"internal/api","outbound_paths":["internal/db"]}]}',
        model_override="test-model",
    )

    assert len(provider.calls) == 2
    assert 'user["User"]' in result["mermaid_source"]
    assert 'user_interfaces["User Interfaces"]' in result["mermaid_source"]
    assert "fell back to deterministic system view" in result["repair_summary"]
    assert usage.input_tokens == 200


async def test_generate_architecture_diagram_rejects_dense_generic_system_view() -> None:
    provider = _SequenceProvider(
        [
            """flowchart LR
user["User"]
ui["User Interfaces"]
api["API & Auth"]
jobs["Background Workers"]
db["Persistence"]
repo["Repository Access"]
graph["Code Graph & Index"]
user --> ui
ui -->|primary flow| api
ui -->|primary flow| jobs
api -->|primary flow| jobs
api -->|primary flow| db
jobs -->|primary flow| api
jobs -->|major flow| db
jobs -->|primary flow| repo
jobs -->|primary flow| graph
repo -->|primary flow| jobs
graph -->|primary flow| jobs
db -->|primary flow| api""",
            """flowchart LR
user["User"]
user_interfaces["User Interfaces"]
api_auth["API & Auth"]
knowledge_orchestration["Knowledge Orchestration"]
background_workers["Background Workers"]
persistence["Persistence"]
user --> user_interfaces
user_interfaces -->|HTTP/API requests| api_auth
api_auth -->|routes generation requests| knowledge_orchestration
knowledge_orchestration -->|dispatches jobs| background_workers
background_workers -->|stores artifacts and job state| persistence""",
        ]
    )
    result, usage = await generate_architecture_diagram(
        provider,
        repository_name="Example Repo",
        audience="developer",
        depth="medium",
        snapshot_json='{"repository_name":"Example Repo"}',
        deterministic_diagram_json='{"modules":[{"path":"internal/api","outbound_paths":["internal/knowledge"]},{"path":"internal/knowledge","outbound_paths":["workers"]}]}',
        model_override="test-model",
    )

    assert len(provider.calls) == 2
    assert "primary flow" not in result["mermaid_source"]
    assert "regenerated diagram to satisfy system-view quality gate" in result["repair_summary"]
    assert usage.input_tokens == 200
    assert usage.output_tokens == 100
