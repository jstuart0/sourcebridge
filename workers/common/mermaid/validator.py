# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

from __future__ import annotations

import re
from dataclasses import dataclass, field

_FENCE_RE = re.compile(r"```(?:mermaid)?\s*([\s\S]*?)```", re.IGNORECASE)
_NODE_RE = re.compile(r'^\s*([A-Za-z0-9_]+)\s*\[\s*"([^"]+)"\s*\]\s*$')
_SUBGRAPH_RE = re.compile(r'^\s*subgraph\s+([A-Za-z0-9_]+)\s*\[\s*"([^"]+)"\s*\]\s*$')
_EDGE_RE = re.compile(r'^\s*([A-Za-z0-9_]+)\s*-->\s*(?:\|[^|]*\|\s*)?([A-Za-z0-9_]+)\s*$')
_INLINE_NODE_RE = re.compile(r'([A-Za-z0-9_]+)\s*\[\s*"([^"]+)"\s*\]')
_QUOTED_EDGE_LABEL_RE = re.compile(r'(-->\s*)\|"([^"]+)"\|')


@dataclass
class MermaidValidationResult:
    mermaid_source: str
    raw_mermaid_source: str
    validation_status: str
    repair_summary: str = ""
    node_labels: dict[str, str] = field(default_factory=dict)
    edge_pairs: set[tuple[str, str]] = field(default_factory=set)


def extract_mermaid_block(raw: str) -> str:
    text = (raw or "").strip()
    match = _FENCE_RE.search(text)
    if match:
        return match.group(1).strip()
    return text


def _normalize_mermaid(raw: str) -> str:
    lines = [line.rstrip() for line in extract_mermaid_block(raw).splitlines() if line.strip()]
    if not lines:
        return ""
    if not lines[0].startswith("flowchart"):
        lines.insert(0, "flowchart LR")
    return "\n".join(lines).strip()


def _rename_conflicting_subgraphs(lines: list[str]) -> tuple[list[str], list[str]]:
    node_labels: dict[str, str] = {}
    subgraph_labels: dict[str, str] = {}
    for line in lines:
        node_match = _NODE_RE.match(line)
        if node_match:
            node_labels[node_match.group(1)] = node_match.group(2)
            continue
        subgraph_match = _SUBGRAPH_RE.match(line)
        if subgraph_match:
            subgraph_labels[subgraph_match.group(1)] = subgraph_match.group(2)

    renames: dict[str, str] = {}
    for subgraph_id, subgraph_label in subgraph_labels.items():
        if subgraph_label in node_labels.values():
            renames[subgraph_id] = f"{subgraph_id}_group"

    if not renames:
        return lines, []

    repaired: list[str] = []
    rename_notes: list[str] = []
    for line in lines:
        updated = line
        for old, new in renames.items():
            if _SUBGRAPH_RE.match(updated):
                updated = re.sub(rf"^(\s*subgraph\s+){re.escape(old)}(\s*\[)", rf"\1{new}\2", updated)
        repaired.append(updated)
    for old, new in renames.items():
        rename_notes.append(f"renamed subgraph {old} -> {new}")
    return repaired, rename_notes


def _normalize_edge_labels(lines: list[str]) -> tuple[list[str], list[str]]:
    repaired: list[str] = []
    notes: list[str] = []
    changed = False
    for line in lines:
        updated = _QUOTED_EDGE_LABEL_RE.sub(r"\1|\2|", line)
        if updated != line:
            changed = True
        repaired.append(updated)
    if changed:
        notes.append("normalized quoted edge labels")
    return repaired, notes


def validate_and_repair_mermaid(raw: str) -> MermaidValidationResult:
    extracted = extract_mermaid_block(raw).strip()
    normalized = _normalize_mermaid(raw)
    if not normalized:
        raise ValueError("empty Mermaid output")

    original = normalized
    lines = normalized.splitlines()
    lines, repairs = _rename_conflicting_subgraphs(lines)
    lines, edge_repairs = _normalize_edge_labels(lines)
    repairs.extend(edge_repairs)
    normalized = "\n".join(lines).strip()

    if not normalized.startswith("flowchart"):
        raise ValueError("Mermaid diagram must start with flowchart")

    node_labels: dict[str, str] = {}
    subgraph_labels: dict[str, str] = {}
    edge_pairs: set[tuple[str, str]] = set()
    for line in normalized.splitlines()[1:]:
        node_match = _NODE_RE.match(line)
        if node_match:
            node_labels[node_match.group(1)] = node_match.group(2)
            continue
        subgraph_match = _SUBGRAPH_RE.match(line)
        if subgraph_match:
            subgraph_labels[subgraph_match.group(1)] = subgraph_match.group(2)
            continue
        edge_match = _EDGE_RE.match(line)
        if edge_match:
            edge_pairs.add((edge_match.group(1), edge_match.group(2)))
        for inline_id, inline_label in _INLINE_NODE_RE.findall(line):
            node_labels.setdefault(inline_id, inline_label)

    if not node_labels and not subgraph_labels:
        raise ValueError("Mermaid diagram did not define any nodes or subgraphs")

    validation_status = "valid"
    if normalized != original or repairs or normalized != extracted:
        validation_status = "repaired"

    return MermaidValidationResult(
        mermaid_source=normalized,
        raw_mermaid_source=extract_mermaid_block(raw).strip(),
        validation_status=validation_status,
        repair_summary="; ".join(repairs),
        node_labels=node_labels | subgraph_labels,
        edge_pairs=edge_pairs,
    )


def infer_edge_labels(result: MermaidValidationResult) -> set[tuple[str, str]]:
    labelled: set[tuple[str, str]] = set()
    for src_id, tgt_id in result.edge_pairs:
        src = result.node_labels.get(src_id, src_id)
        tgt = result.node_labels.get(tgt_id, tgt_id)
        labelled.add((src, tgt))
    return labelled
