"""Symbol resolution for change simulation via embedding similarity."""

from __future__ import annotations

import math
from dataclasses import dataclass

import structlog

from workers.common.embedding.provider import EmbeddingProvider

log = structlog.get_logger()

TOP_N = 10
CONFIDENCE_THRESHOLD = 0.35
ANCHOR_BOOST = 0.15
_EMBED_BATCH_SIZE = 256


@dataclass
class SymbolInfo:
    """Lightweight symbol descriptor for resolution."""

    id: str
    name: str
    qualified_name: str
    kind: str
    file_path: str
    signature: str = ""
    doc_comment: str = ""


@dataclass
class ResolvedSymbol:
    """A symbol identified as affected by the hypothetical change."""

    symbol_id: str
    name: str
    qualified_name: str
    kind: str
    file_path: str
    similarity: float
    is_anchor: bool = False


def _cosine_similarity(a: list[float], b: list[float]) -> float:
    dot = sum(x * y for x, y in zip(a, b, strict=False))
    norm_a = math.sqrt(sum(x * x for x in a))
    norm_b = math.sqrt(sum(x * x for x in b))
    if norm_a == 0 or norm_b == 0:
        return 0.0
    return dot / (norm_a * norm_b)


def _symbol_to_embedding_text(symbol: SymbolInfo) -> str:
    parts = [symbol.qualified_name]
    if symbol.signature:
        parts.append(symbol.signature)
    if symbol.doc_comment:
        parts.append(symbol.doc_comment)
    return " ".join(parts)


def _dirname(path: str) -> str:
    idx = path.rfind("/")
    if idx == -1:
        return ""
    return path[:idx]


async def resolve_symbols(
    description: str,
    symbols: list[SymbolInfo],
    anchor_file: str | None,
    anchor_symbol: str | None,
    embedding_provider: EmbeddingProvider,
    top_n: int = TOP_N,
    confidence_threshold: float = CONFIDENCE_THRESHOLD,
) -> list[ResolvedSymbol]:
    """Resolve which symbols would be affected by the described change."""

    if not symbols:
        return []

    # Step 1: Early anchor resolution
    primary_symbols: list[SymbolInfo] = []
    if anchor_symbol:
        exact_matches = [s for s in symbols if s.name == anchor_symbol or s.qualified_name == anchor_symbol]
        if anchor_file:
            exact_matches = [s for s in exact_matches if s.file_path == anchor_file]
        if exact_matches:
            primary_symbols = exact_matches
        else:
            raise ValueError(f"Anchor symbol not found: {anchor_symbol}")

    # Step 2: Build embedding texts
    symbol_texts = [_symbol_to_embedding_text(s) for s in symbols]

    # Step 3: Embed in batches
    all_texts = [description] + symbol_texts
    all_embeddings: list[list[float]] = []
    for i in range(0, len(all_texts), _EMBED_BATCH_SIZE):
        batch = all_texts[i : i + _EMBED_BATCH_SIZE]
        batch_embeddings = await embedding_provider.embed(batch)
        all_embeddings.extend(batch_embeddings)

    desc_embedding = all_embeddings[0]
    symbol_embeddings = all_embeddings[1:]

    # Step 4: Score each symbol
    scored: list[tuple[float, SymbolInfo]] = []
    anchor_dir = _dirname(anchor_file) if anchor_file else None

    for i, s in enumerate(symbols):
        sim = _cosine_similarity(desc_embedding, symbol_embeddings[i])

        # Apply anchor boost
        if anchor_file and s.file_path == anchor_file:
            sim += ANCHOR_BOOST
        elif anchor_dir and s.file_path.startswith(anchor_dir + "/"):
            sim += ANCHOR_BOOST * 0.5

        scored.append((sim, s))

    # Step 5: Sort by score descending
    scored.sort(key=lambda x: x[0], reverse=True)

    # Step 6: Filter by threshold and take top N
    primary_ids = {s.id for s in primary_symbols}
    results: list[ResolvedSymbol] = []
    for sim, s in scored[:top_n]:
        if sim >= confidence_threshold:
            results.append(
                ResolvedSymbol(
                    symbol_id=s.id,
                    name=s.name,
                    qualified_name=s.qualified_name,
                    kind=s.kind,
                    file_path=s.file_path,
                    similarity=sim,
                    is_anchor=s.id in primary_ids,
                )
            )

    # Step 7: Ensure anchor symbols are always included
    result_ids = {r.symbol_id for r in results}
    for ps in primary_symbols:
        if ps.id not in result_ids:
            results.insert(
                0,
                ResolvedSymbol(
                    symbol_id=ps.id,
                    name=ps.name,
                    qualified_name=ps.qualified_name,
                    kind=ps.kind,
                    file_path=ps.file_path,
                    similarity=1.0,
                    is_anchor=True,
                ),
            )

    log.info(
        "resolve_symbols",
        description_len=len(description),
        total_symbols=len(symbols),
        resolved=len(results),
        top_similarity=results[0].similarity if results else 0.0,
    )

    return results
