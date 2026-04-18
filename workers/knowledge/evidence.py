# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Shared evidence extraction and DEEP quality gates."""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from pathlib import PurePosixPath
from typing import Iterable

from workers.knowledge.types import EvidenceRef

FORBIDDEN_GENERIC_PHRASES = [
    "various components",
    "the system handles",
    "as needed",
    "and more",
    "etc.",
    "several modules",
    "in some cases",
    "this functionality",
    "comprehensive platform",
    "designed for",
    "leveraging",
    "serving developers and teams",
    "multi-language architecture",
]

SECTION_WEAKNESS_PHRASES = {
    "Domain Model": (
        "revolves around",
        "fundamental concept",
        "central concept",
        "structured representation",
        "iterative approach to knowledge processing",
        "allowing external systems to interact",
    ),
    "Key Abstractions": (
        "key abstractions include",
        "collectively manage",
        "lifecycle of data processing",
        "crucial component",
        "central abstraction",
    ),
}

RELEVANCE_DOWNRANK_PATTERNS = (
    "/examples/",
    "/example/",
    "/demo/",
    "/demos/",
    "/benchmark-results/",
    "/benchmarks/",
    "/fixtures/",
    "/fixture/",
    "/mocks/",
    "/mock/",
    "/fake/",
    "_test.",
    "/testdata/",
)

UNSUPPORTED_CLAIM_TERMS = (
    "microservice",
    "microservices",
    "redis",
    "postgresql",
    "postgres",
    "graphviz",
    "kafka",
    "rabbitmq",
    "kubernetes",
    "docker",
    "mysql",
    "mongodb",
    "sqlite",
    "dynamodb",
    "nats",
    "temporal",
    "bullmq",
    "celery",
    "sidekiq",
    "master control program",
)

SPECULATIVE_SENTENCE_PHRASES = (
    "not explicitly detailed",
    "not explicitly shown",
    "not directly shown",
    "not directly detailed",
    "not directly visible",
    "is inferred",
    "are inferred",
    "can be inferred",
    "could be inferred",
    "likely ",
    "likely to ",
    "suggests ",
    "suggesting ",
    "implies ",
    "implying ",
    "might ",
    "may ",
    "potentially ",
    "would be used",
    "would be consumed",
    "necessary to store",
)

_INLINE_FILE_LINE_RE = re.compile(r"(?P<path>[A-Za-z0-9_./-]+\.[A-Za-z0-9]+):(?P<start>\d+)(?:-(?P<end>\d+))?")
_SYMBOL_MENTION_RE = re.compile(r"`?[A-Za-z_][A-Za-z0-9_]*\(\)`?")
_INVALID_EVIDENCE_PATHS = {"", "none", "null", "repository", "repo", "unknown"}


@dataclass
class ExtractedEvidence:
    path: str
    line_start: int | None = None
    line_end: int | None = None
    kind: str = "code_ref"


@dataclass
class EvidenceGateResult:
    evidence: list[ExtractedEvidence] = field(default_factory=list)
    below_threshold: bool = False
    forbidden_phrases: list[str] = field(default_factory=list)
    unsupported_claim_terms: list[str] = field(default_factory=list)


def find_forbidden_generic_phrases(text: str) -> list[str]:
    lowered = (text or "").lower()
    return [phrase for phrase in FORBIDDEN_GENERIC_PHRASES if phrase in lowered]


def find_section_weakness_phrases(title: str, text: str) -> list[str]:
    lowered = (text or "").lower()
    return [phrase for phrase in SECTION_WEAKNESS_PHRASES.get(title, ()) if phrase in lowered]


def is_valid_evidence_path(path: str) -> bool:
    candidate = (path or "").strip()
    if candidate.lower() in _INVALID_EVIDENCE_PATHS:
        return False
    name = PurePosixPath(candidate).name
    return "." in name and not name.endswith(".")


def extract_inline_evidence(text: str) -> list[ExtractedEvidence]:
    out: list[ExtractedEvidence] = []
    for match in _INLINE_FILE_LINE_RE.finditer(text or ""):
        start = int(match.group("start"))
        end = int(match.group("end")) if match.group("end") else start
        path = match.group("path")
        if not is_valid_evidence_path(path):
            continue
        out.append(ExtractedEvidence(path=path, line_start=start, line_end=end, kind="code_ref"))
    return out


def extract_section_evidence_refs(evidence: Iterable[EvidenceRef]) -> list[ExtractedEvidence]:
    out: list[ExtractedEvidence] = []
    for ref in evidence:
        if not is_valid_evidence_path(ref.file_path):
            continue
        out.append(
            ExtractedEvidence(
                path=ref.file_path,
                line_start=ref.line_start if ref.line_start > 0 else None,
                line_end=ref.line_end if ref.line_end > 0 else None,
                kind="section_field",
            )
        )
    return out


def extract_step_file_symbol_evidence(content: str, file_paths: Iterable[str]) -> list[ExtractedEvidence]:
    has_symbol = bool(_SYMBOL_MENTION_RE.search(content or ""))
    out: list[ExtractedEvidence] = []
    for file_path in file_paths:
        file_path = (file_path or "").strip()
        if not is_valid_evidence_path(file_path):
            continue
        out.append(ExtractedEvidence(path=file_path, kind="section_field" if has_symbol else "code_ref"))
    return out


def extract_code_tour_stop_evidence(file_path: str, line_start: int, line_end: int) -> list[ExtractedEvidence]:
    if not is_valid_evidence_path(file_path) or line_start <= 0 or line_end <= 0:
        return []
    return [ExtractedEvidence(path=file_path, line_start=line_start, line_end=line_end, kind="section_field")]


def relevance_penalty(path: str, *, profile: str = "product_core", scope_path: str = "") -> int:
    candidate = f"/{(path or '').strip().lstrip('/')}".lower()
    scoped = (scope_path or "").strip().lstrip("/").lower()
    if not candidate or profile != "product_core":
        return 0
    if scoped and (candidate == f"/{scoped}" or candidate.startswith(f"/{scoped}/")):
        return 0
    return 1 if any(pattern in candidate for pattern in RELEVANCE_DOWNRANK_PATTERNS) else 0


def detect_unsupported_claim_terms(text: str, evidence_store_text: str) -> list[str]:
    lowered = (text or "").lower()
    store_lowered = (evidence_store_text or "").lower()
    hits: list[str] = []
    for term in UNSUPPORTED_CLAIM_TERMS:
        if term in lowered and term not in store_lowered:
            hits.append(term)
    return hits


def strip_unsupported_claim_sentences(text: str, unsupported_terms: Iterable[str]) -> str:
    content = (text or "").strip()
    if not content:
        return content
    blocked = tuple(term.lower() for term in unsupported_terms if term)
    if not blocked:
        return content
    pieces = re.split(r"(?<=[.!?])\s+|\n+", content)
    kept = [piece.strip() for piece in pieces if piece.strip() and not any(term in piece.lower() for term in blocked)]
    if not kept:
        return "*Insufficient grounded evidence to support the original system-level claim.*"
    return "\n\n".join(kept)


def strip_forbidden_phrase_sentences(text: str, forbidden_phrases: Iterable[str]) -> str:
    content = (text or "").strip()
    if not content:
        return content
    blocked = tuple(phrase.lower() for phrase in forbidden_phrases if phrase)
    if not blocked:
        return content
    pieces = re.split(r"(?<=[.!?])\s+|\n+", content)
    kept = [piece.strip() for piece in pieces if piece.strip() and not any(phrase in piece.lower() for phrase in blocked)]
    if not kept:
        return "*Insufficient grounded evidence to support the original summary wording.*"
    return "\n\n".join(kept)


def strip_speculative_sentences(text: str) -> str:
    content = (text or "").strip()
    if not content:
        return content
    pieces = re.split(r"(?<=[.!?])\s+|\n+", content)
    kept = [
        piece.strip()
        for piece in pieces
        if piece.strip() and not any(phrase in piece.lower() for phrase in SPECULATIVE_SENTENCE_PHRASES)
    ]
    if not kept:
        return "*Insufficient grounded evidence to support the original inferred wording.*"
    return "\n\n".join(kept)


def evaluate_evidence_gate(
    *,
    text: str,
    evidence: Iterable[ExtractedEvidence],
    minimum: int,
    evidence_store_text: str = "",
) -> EvidenceGateResult:
    extracted = list(evidence) + extract_inline_evidence(text)
    return EvidenceGateResult(
        evidence=extracted,
        below_threshold=len(extracted) < minimum,
        forbidden_phrases=find_forbidden_generic_phrases(text),
        unsupported_claim_terms=detect_unsupported_claim_terms(text, evidence_store_text),
    )
