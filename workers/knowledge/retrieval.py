# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Embedding-based retrieval for building focused LLM context.

Instead of sending the entire repository snapshot to the LLM, this module
selects the most relevant symbols using cosine similarity between the
query (or a synthetic overview query) and symbol representations.

Structural metadata (languages, modules, counts, requirements, coverage)
is always preserved — retrieval only affects which symbols and docs are
included in detail.
"""

from __future__ import annotations

import json
import math

import structlog

from workers.common.embedding.provider import EmbeddingProvider

log = structlog.get_logger()

# Maximum symbols to include after retrieval
_DEFAULT_TOP_K = 80

# Batch size for embedding requests
_EMBED_BATCH_SIZE = 256


def cosine_similarity(a: list[float], b: list[float]) -> float:
    """Compute cosine similarity between two vectors."""
    dot = sum(x * y for x, y in zip(a, b, strict=False))
    mag_a = math.sqrt(sum(x * x for x in a))
    mag_b = math.sqrt(sum(x * x for x in b))
    if mag_a == 0 or mag_b == 0:
        return 0.0
    return dot / (mag_a * mag_b)


def _symbol_text(sym: dict) -> str:
    """Build a text representation of a symbol for embedding."""
    parts = [sym.get("qualified_name") or sym.get("name", "")]
    kind = sym.get("kind", "")
    if kind:
        parts.append(kind)
    fp = sym.get("file_path", "")
    if fp:
        parts.append(fp)
    doc = sym.get("doc_comment", "")
    if doc:
        parts.append(doc[:200])
    return " ".join(parts)


async def _batch_embed(
    provider: EmbeddingProvider,
    texts: list[str],
) -> list[list[float]]:
    """Embed texts in batches to avoid overwhelming the provider."""
    all_embeddings: list[list[float]] = []
    for i in range(0, len(texts), _EMBED_BATCH_SIZE):
        batch = texts[i : i + _EMBED_BATCH_SIZE]
        embeddings = await provider.embed(batch)
        all_embeddings.extend(embeddings)
    return all_embeddings


async def retrieve_relevant_snapshot(
    snapshot_json: str,
    query: str,
    embedding_provider: EmbeddingProvider,
    top_k: int = _DEFAULT_TOP_K,
) -> str:
    """Build a focused snapshot by retrieving symbols relevant to the query.

    Always preserves:
    - All structural metadata (languages, modules, counts, coverage_ratio)
    - All requirements (small, important for traceability questions)

    Uses embedding similarity to select:
    - Top-K symbols across all symbol lists
    - Relevant docs (by path similarity to selected files)
    - Links involving selected symbols
    """
    try:
        snap = json.loads(snapshot_json)
    except (json.JSONDecodeError, TypeError):
        log.warn("retrieval_json_parse_failed")
        return snapshot_json

    # Collect all symbols from all lists with their source list name
    symbol_keys = (
        "entry_points",
        "public_api",
        "test_symbols",
        "complex_symbols",
        "high_fan_out_symbols",
        "high_fan_in_symbols",
    )
    all_symbols: list[tuple[str, int, dict]] = []  # (list_key, index, symbol)
    for key in symbol_keys:
        for i, sym in enumerate(snap.get(key) or []):
            all_symbols.append((key, i, sym))

    if not all_symbols:
        log.info("retrieval_no_symbols")
        return snapshot_json

    # Build text representations
    symbol_texts = [_symbol_text(sym) for _, _, sym in all_symbols]

    # Embed query and all symbols
    log.info("retrieval_embedding", query_len=len(query), n_symbols=len(symbol_texts))
    try:
        all_texts = [query] + symbol_texts
        all_embeddings = await _batch_embed(embedding_provider, all_texts)

        query_embedding = all_embeddings[0]
        symbol_embeddings = all_embeddings[1:]

        # Score each symbol by similarity to the query
        scored: list[tuple[float, str, int, dict]] = []
        for idx, (key, orig_idx, sym) in enumerate(all_symbols):
            sim = cosine_similarity(query_embedding, symbol_embeddings[idx])
            scored.append((sim, key, orig_idx, sym))
    except Exception as exc:
        log.error("retrieval_embedding_failed", error=str(exc))
        # Fall back to condensed snapshot
        from workers.knowledge.snapshot_truncate import condense_snapshot

        return condense_snapshot(snapshot_json)

    # Sort by similarity descending, take top_k
    scored.sort(key=lambda x: x[0], reverse=True)
    selected = scored[:top_k]

    # Rebuild symbol lists with only selected symbols
    selected_by_key: dict[str, list[dict]] = {key: [] for key in symbol_keys}
    selected_file_paths: set[str] = set()
    selected_symbol_ids: set[str] = set()

    for _sim, key, _idx, sym in selected:
        # Strip doc_comment to save space (the LLM has the file path for reference)
        sym_copy = {k: v for k, v in sym.items() if k != "doc_comment"}
        selected_by_key[key].append(sym_copy)
        if sym.get("file_path"):
            selected_file_paths.add(sym["file_path"])
        if sym.get("id"):
            selected_symbol_ids.add(sym["id"])

    for key in symbol_keys:
        snap[key] = selected_by_key[key]

    # Filter links to only those involving selected symbols
    links = snap.get("links") or []
    snap["links"] = [link for link in links if link.get("symbol_id") in selected_symbol_ids]

    # Filter docs to only those whose paths overlap with selected files
    docs = snap.get("docs") or []
    if docs:
        relevant_docs = []
        for doc in docs:
            doc_path = doc.get("path", "")
            # Include doc if any selected file is in the same directory
            doc_dir = "/".join(doc_path.split("/")[:-1]) if "/" in doc_path else ""
            if any(fp.startswith(doc_dir) for fp in selected_file_paths if doc_dir):
                # Keep path but strip content to save space
                relevant_docs.append({"path": doc_path})
            if len(relevant_docs) >= 10:
                break
        snap["docs"] = relevant_docs

    result = json.dumps(snap, separators=(",", ":"))
    log.info(
        "retrieval_complete",
        query=query[:80],
        symbols_selected=len(selected),
        symbols_total=len(all_symbols),
        original_chars=len(snapshot_json),
        result_chars=len(result),
    )
    return result


def build_overview_query(
    repository_name: str,
    action: str,
    scope_type: str = "repository",
    scope_path: str = "",
) -> str:
    """Build a synthetic query for snapshot retrieval.

    For repository scope, uses a broad query for comprehensive coverage.
    For file/symbol scopes, uses a targeted query so retrieval selects
    symbols relevant to the specific scope rather than a generic overview.
    """
    # Scoped queries — narrow retrieval to the target context
    if scope_type == "symbol" and scope_path:
        parts = scope_path.rsplit("#", 1)
        file_part = parts[0] if parts else scope_path
        symbol_part = parts[1] if len(parts) > 1 else scope_path
        return (
            f"Symbol {symbol_part} in {file_part} callers callees "
            f"dependencies side effects usage patterns of {repository_name}"
        )
    if scope_type == "file" and scope_path:
        return f"File {scope_path} symbols responsibilities dependencies patterns usage of {repository_name}"
    if scope_type == "requirement" and scope_path:
        return (
            f"Requirement {scope_path} implementation linked symbols files "
            f"cross-cutting behavior traceability {repository_name}"
        )
    if scope_type == "module" and scope_path:
        return f"Module {scope_path} components files API boundaries patterns of {repository_name}"

    # Broad queries for repository scope
    queries = {
        "cliff_notes": (
            f"Overview architecture structure main components modules entry points "
            f"API endpoints configuration database models tests of {repository_name}"
        ),
        "learning_path": (
            f"Getting started core concepts fundamentals architecture data flow "
            f"API patterns configuration setup tests of {repository_name}"
        ),
        "code_tour": (
            f"Entry point request flow data model API handler middleware "
            f"database configuration deployment of {repository_name}"
        ),
        "workflow_story": (
            f"Request flow execution path handler middleware worker pipeline "
            f"data processing error handling lifecycle of {repository_name}"
        ),
    }
    return queries.get(action, f"Architecture overview of {repository_name}")
