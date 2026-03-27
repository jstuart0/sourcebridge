"""Fake LLM provider for deterministic testing."""

from __future__ import annotations

import json
from collections.abc import AsyncIterator

from workers.common.llm.provider import LLMResponse


class FakeLLMProvider:
    """Deterministic LLM provider for testing. Returns fixture-backed responses."""

    SUMMARY_RESPONSE = json.dumps({
        "purpose": "Processes payment transactions including validation, approval, and charging",
        "inputs": ["ctx", "order"],
        "outputs": ["receipt"],
        "dependencies": ["validate", "requireApproval", "charge"],
        "side_effects": ["Charges payment method", "May require approval"],
        "risks": ["Payment failure", "Insufficient funds"],
        "confidence": 0.92,
    })

    REVIEW_RESPONSE = json.dumps({
        "findings": [
            {
                "category": "security",
                "severity": "high",
                "message": "Payment amount not validated against negative values",
                "file_path": "payment/processor.go",
                "start_line": 3,
                "end_line": 3,
                "suggestion": "Add validation: if order.Amount <= 0 { return error }",
            }
        ],
        "score": 6.5,
    })

    DISCUSS_RESPONSE = json.dumps({
        "answer": (
            "The processPayment function orchestrates the payment flow by first"
            " validating the order, then checking if approval is needed for amounts"
            " above the threshold, and finally charging the payment method."
        ),
        "references": ["payment/processor.go:1-8"],
        "related_requirements": ["REQ-042", "REQ-017"],
    })

    LEARNING_PATH_RESPONSE = json.dumps([
        {
            "order": 1,
            "title": "Repository Overview",
            "objective": "Understand the project structure and purpose.",
            "content": "Start by reading the README and main entry point.",
            "file_paths": ["main.go"],
            "symbol_ids": [],
            "estimated_time": "10 minutes",
        },
        {
            "order": 2,
            "title": "Core Concepts",
            "objective": "Understand key types and interfaces.",
            "content": "Read the main types and their relationships.",
            "file_paths": ["main.go", "util.go"],
            "symbol_ids": [],
            "estimated_time": "20 minutes",
        },
        {
            "order": 3,
            "title": "Primary Flow",
            "objective": "Trace a request through the system end-to-end.",
            "content": "Follow the main() function through to completion.",
            "file_paths": ["main.go"],
            "symbol_ids": [],
            "estimated_time": "15 minutes",
        },
    ])

    CODE_TOUR_RESPONSE = json.dumps([
        {
            "order": 1,
            "title": "Entry Point",
            "description": "This is where the application starts.",
            "file_path": "main.go",
            "line_start": 1,
            "line_end": 20,
        },
        {
            "order": 2,
            "title": "Request Handler",
            "description": "Handles incoming HTTP requests.",
            "file_path": "main.go",
            "line_start": 22,
            "line_end": 80,
        },
        {
            "order": 3,
            "title": "Utilities",
            "description": "Helper functions used across the codebase.",
            "file_path": "util.go",
            "line_start": 1,
            "line_end": 10,
        },
    ])

    EXPLAIN_RESPONSE = (
        "This function handles payment processing. It takes a context and an order,"
        " validates the order details, checks if the amount requires approval, and"
        " then charges the payment method. Finally, it returns a receipt."
    )

    CLIFF_NOTES_RESPONSE = json.dumps([
        {
            "title": "System Purpose",
            "content": "This repository implements a payment processing system.",
            "summary": "Payment processing system.",
            "confidence": "high",
            "inferred": False,
            "evidence": [{
                "source_type": "file", "source_id": "", "file_path": "main.go",
                "line_start": 1, "line_end": 10, "rationale": "Entry point",
            }],
        },
        {
            "title": "Architecture Overview",
            "content": "The system uses a layered architecture with handlers, services, and storage.",
            "summary": "Layered architecture.",
            "confidence": "high",
            "inferred": False,
            "evidence": [{
                "source_type": "file", "source_id": "", "file_path": "main.go",
                "line_start": 1, "line_end": 20, "rationale": "Module structure",
            }],
        },
        {
            "title": "Domain Model",
            "content": "Core domain entities include Order, Payment, and Receipt.",
            "summary": "Order/Payment/Receipt domain.",
            "confidence": "medium",
            "inferred": True,
            "evidence": [],
        },
        {
            "title": "Core System Flows",
            "content": "The primary flow is: validate order -> approve -> charge -> receipt.",
            "summary": "Validate-approve-charge flow.",
            "confidence": "high",
            "inferred": False,
            "evidence": [{
                "source_type": "symbol", "source_id": "", "file_path": "main.go",
                "line_start": 1, "line_end": 20, "rationale": "Main flow",
            }],
        },
        {
            "title": "Code Structure",
            "content": "Two files: main.go (entry) and util.go (helpers).",
            "summary": "Two-file structure.",
            "confidence": "high",
            "inferred": False,
            "evidence": [{
                "source_type": "file", "source_id": "", "file_path": "main.go",
                "line_start": 0, "line_end": 0, "rationale": "File listing",
            }],
        },
        {
            "title": "Complexity & Risk Areas",
            "content": "Payment validation and error handling are the main risk areas.",
            "summary": "Payment validation risks.",
            "confidence": "medium",
            "inferred": True,
            "evidence": [],
        },
        {
            "title": "Suggested Starting Points",
            "content": "Start with main.go to understand the entry point and primary flow.",
            "summary": "Start with main.go.",
            "confidence": "high",
            "inferred": False,
            "evidence": [{
                "source_type": "file", "source_id": "", "file_path": "main.go",
                "line_start": 1, "line_end": 10, "rationale": "Entry point",
            }],
        },
    ])

    @property
    def default_model(self) -> str:
        """Return the default model ID."""
        return "fake-test-model"

    async def complete(
        self,
        prompt: str,
        *,
        system: str = "",
        max_tokens: int = 4096,
        temperature: float = 0.0,
        model: str | None = None,
    ) -> LLMResponse:
        """Return deterministic fixture response based on prompt content."""
        content = self.SUMMARY_RESPONSE
        if "cliff notes" in prompt.lower() or "required sections" in prompt.lower():
            content = self.CLIFF_NOTES_RESPONSE
        elif "learning path" in prompt.lower():
            content = self.LEARNING_PATH_RESPONSE
        elif "code tour" in prompt.lower():
            content = self.CODE_TOUR_RESPONSE
        elif "review" in prompt.lower() or "security" in prompt.lower():
            content = self.REVIEW_RESPONSE
        elif "discuss" in prompt.lower() or "question" in prompt.lower() or "what does" in prompt.lower():
            content = self.DISCUSS_RESPONSE
        elif "explain" in prompt.lower():
            content = self.EXPLAIN_RESPONSE

        return LLMResponse(
            content=content,
            model="fake-test-model",
            input_tokens=len(prompt.split()),
            output_tokens=len(content.split()),
            stop_reason="end_turn",
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
        """Stream deterministic response word by word."""
        response = await self.complete(prompt, system=system, max_tokens=max_tokens, temperature=temperature)
        for word in response.content.split():
            yield word + " "
