"""Shared LLM response parsing utilities."""

from __future__ import annotations

import json
import re


def strip_llm_wrapping(raw: str) -> str:
    """Strip common LLM output wrapping: <think> blocks and markdown fences.

    Many reasoning models (Qwen, DeepSeek, etc.) wrap their output in
    <think>...</think> blocks. Some also wrap JSON in markdown code fences.
    This function strips both, returning the inner content.
    """
    text = raw.strip()

    # Strip <think>...</think> blocks
    text = re.sub(r"<think>.*?</think>", "", text, flags=re.DOTALL).strip()

    # Strip markdown code fences
    if text.startswith("```"):
        first_nl = text.find("\n")
        text = text[first_nl + 1 :] if first_nl != -1 else text[3:]
        text = text.rstrip()
        if text.endswith("```"):
            text = text[:-3].rstrip()

    return text


def parse_json_response(raw: str) -> dict | list | None:
    """Parse a JSON response from an LLM, tolerating common quirks.

    Handles: <think> blocks, markdown fences, nested code blocks.
    Returns None if parsing fails completely.
    """
    text = strip_llm_wrapping(raw)

    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass

    # Try extracting from remaining markdown code blocks
    if "```" in text:
        try:
            start = text.index("```") + 3
            if text[start:].startswith("json"):
                start += 4
            start_nl = text.find("\n", start)
            if start_nl != -1:
                start = start_nl + 1
            end = text.index("```", start)
            return json.loads(text[start:end].strip())
        except (json.JSONDecodeError, ValueError):
            pass

    return None
