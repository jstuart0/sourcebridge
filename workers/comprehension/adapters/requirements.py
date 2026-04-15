# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""RequirementsCorpus adapter.

Wraps a set of requirements (dict-shaped, matching what the existing
requirements pipeline passes around) as a CorpusSource so the
comprehension engine can produce hierarchical summaries of requirement
documents the same way it does for code.

Hierarchy:

    collection (ROOT)
     ├─ document (GROUP)
     │   ├─ section (LEAF_CONTAINER)
     │   │   └─ requirement (LEAF)
     │   └─ ...
     └─ ...

Input shape (intentionally flexible so the same adapter can consume
both the worker-side requirements protobuf and a hand-built dict in
tests):

    {
      "collection_id": "req-col-1",          # optional, derived from name if missing
      "collection_name": "Product Requirements",
      "documents": [                          # or "document": {...} for single-doc
        {
          "id": "doc-1",
          "title": "Onboarding",
          "sections": [                       # optional; requirements may be flat
            {"id": "sec-1", "title": "Sign-in", "requirements": [req_dict, ...]},
          ],
          "requirements": [req_dict, ...],    # flat fallback when no sections
        }
      ],
    }

A requirement dict is:

    {"id": ..., "external_id": ..., "title": ..., "description": ...,
     "priority": ..., "tags": [...]}

All fields are optional; the adapter is tolerant of missing keys.
"""

from __future__ import annotations

from collections.abc import Iterable
from dataclasses import dataclass, field
from typing import Any

from workers.comprehension.corpus import CorpusUnit, UnitKind


@dataclass
class RequirementsCorpus:
    """A CorpusSource built from a requirements collection dict."""

    payload: dict[str, Any]
    corpus_id: str = ""
    corpus_type: str = "requirements"
    _units: dict[str, CorpusUnit] = field(default_factory=dict)
    _children_by_parent: dict[str, list[str]] = field(default_factory=dict)
    _leaf_texts: dict[str, str] = field(default_factory=dict)
    _revision_fp: str = ""

    def __post_init__(self) -> None:
        if not self.corpus_id:
            self.corpus_id = str(
                self.payload.get("collection_id")
                or self.payload.get("id")
                or self.payload.get("collection_name")
                or "requirements-corpus"
            )
        self._build()

    # ------------------------------------------------------------------
    # CorpusSource interface

    def root(self) -> CorpusUnit:
        return self._units["collection"]

    def children(self, unit: CorpusUnit) -> Iterable[CorpusUnit]:
        for child_id in self._children_by_parent.get(unit.id, []):
            yield self._units[child_id]

    def leaf_content(self, unit: CorpusUnit) -> str:
        if not unit.kind.is_leaf():
            raise ValueError(f"not a leaf: {unit.id}")
        return self._leaf_texts.get(unit.id, "")

    def revision_fingerprint(self) -> str:
        return self._revision_fp

    # ------------------------------------------------------------------
    # Construction

    def _build(self) -> None:
        self._revision_fp = str(self.payload.get("revision_fingerprint") or self.payload.get("updated_at") or "")

        collection_name = str(self.payload.get("collection_name") or "Requirements")
        self._add_unit(
            CorpusUnit(
                id="collection",
                kind=UnitKind.ROOT,
                level=3,
                label=collection_name,
                metadata={"collection_id": self.corpus_id},
            )
        )

        # Normalize to a list of documents. The adapter accepts either a
        # top-level "documents" list, a single "document" dict, or a
        # flat "requirements" list that we wrap into a synthetic
        # single-doc structure.
        documents = self._normalize_documents(self.payload)

        for doc in documents:
            doc_id_raw = str(doc.get("id") or doc.get("title") or "doc")
            doc_title = str(doc.get("title") or doc_id_raw)
            doc_unit_id = f"doc:{doc_id_raw}"
            self._add_unit(
                CorpusUnit(
                    id=doc_unit_id,
                    kind=UnitKind.GROUP,
                    level=2,
                    label=doc_title,
                    parent_id="collection",
                    metadata={"document_id": doc_id_raw},
                )
            )

            # Normalize to a list of sections. When no sections are
            # provided, synthesize a single "Requirements" section that
            # holds the flat list.
            sections = self._normalize_sections(doc)

            for section in sections:
                sec_id_raw = str(section.get("id") or section.get("title") or "section")
                sec_title = str(section.get("title") or sec_id_raw)
                sec_unit_id = f"sec:{doc_id_raw}:{sec_id_raw}"
                self._add_unit(
                    CorpusUnit(
                        id=sec_unit_id,
                        kind=UnitKind.LEAF_CONTAINER,
                        level=1,
                        label=sec_title,
                        parent_id=doc_unit_id,
                        metadata={
                            "document_id": doc_id_raw,
                            "section_id": sec_id_raw,
                        },
                    )
                )

                requirements = section.get("requirements") or []
                if not requirements:
                    # Empty section — synthesize a single leaf with a
                    # placeholder so the section still appears in the
                    # tree.
                    leaf_id = f"req:{doc_id_raw}:{sec_id_raw}:empty"
                    self._add_unit(
                        CorpusUnit(
                            id=leaf_id,
                            kind=UnitKind.LEAF,
                            level=0,
                            label="(no requirements)",
                            parent_id=sec_unit_id,
                            metadata={
                                "document_id": doc_id_raw,
                                "section_id": sec_id_raw,
                            },
                        )
                    )
                    self._leaf_texts[leaf_id] = f"Section `{sec_title}` of `{doc_title}` has no requirements."
                    continue

                for req in requirements:
                    req_id_raw = str(req.get("id") or req.get("external_id") or req.get("title") or "req")
                    label = str(req.get("title") or req.get("external_id") or req_id_raw)
                    leaf_id = f"req:{doc_id_raw}:{sec_id_raw}:{req_id_raw}"
                    self._add_unit(
                        CorpusUnit(
                            id=leaf_id,
                            kind=UnitKind.LEAF,
                            level=0,
                            label=label[:80],
                            parent_id=sec_unit_id,
                            size_tokens=max(50, len(str(req.get("description") or "")) // 4),
                            metadata={
                                "document_id": doc_id_raw,
                                "section_id": sec_id_raw,
                                "requirement_id": req_id_raw,
                                "priority": req.get("priority"),
                                "tags": req.get("tags"),
                            },
                        )
                    )
                    self._leaf_texts[leaf_id] = _render_requirement_text(req)

    def _normalize_documents(self, payload: dict[str, Any]) -> list[dict[str, Any]]:
        """Produce a list of document dicts from a flexible input shape."""
        if isinstance(payload.get("documents"), list):
            return [d for d in payload["documents"] if isinstance(d, dict)]
        if isinstance(payload.get("document"), dict):
            return [payload["document"]]
        # Flat payload — wrap it in a synthetic single document.
        if isinstance(payload.get("requirements"), list):
            return [
                {
                    "id": payload.get("collection_id") or "doc",
                    "title": payload.get("collection_name") or "Requirements",
                    "requirements": payload["requirements"],
                }
            ]
        return []

    def _normalize_sections(self, doc: dict[str, Any]) -> list[dict[str, Any]]:
        """Produce a list of section dicts from a flexible document shape."""
        if isinstance(doc.get("sections"), list):
            return [s for s in doc["sections"] if isinstance(s, dict)]
        reqs = doc.get("requirements") or []
        if isinstance(reqs, list):
            return [
                {
                    "id": "section",
                    "title": "Requirements",
                    "requirements": reqs,
                }
            ]
        return []

    def _add_unit(self, unit: CorpusUnit) -> None:
        self._units[unit.id] = unit
        if unit.parent_id:
            self._children_by_parent.setdefault(unit.parent_id, []).append(unit.id)


def _render_requirement_text(req: dict[str, Any]) -> str:
    """Build the leaf text the comprehension strategy will summarize.

    Includes every grounding field that's available — title, external
    id, priority, tags, description — in a compact form the leaf prompt
    can summarize without additional lookups.
    """
    parts: list[str] = []
    title = str(req.get("title") or "")
    ext_id = str(req.get("external_id") or "")
    if ext_id:
        parts.append(f"[{ext_id}] {title}".strip())
    elif title:
        parts.append(title)

    description = str(req.get("description") or "").strip()
    if description:
        parts.append(description)

    priority = str(req.get("priority") or "")
    if priority:
        parts.append(f"Priority: {priority}")

    tags = req.get("tags")
    if isinstance(tags, list) and tags:
        parts.append(f"Tags: {', '.join(str(t) for t in tags)}")

    return "\n".join(parts) if parts else "(empty requirement)"
