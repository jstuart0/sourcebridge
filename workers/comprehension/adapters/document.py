# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""DocumentCorpus adapter.

Wraps plain-text documents (markdown, README, plain text, uploaded
files) as a CorpusSource. Segments by markdown heading level where
available; falls back to paragraph splits for plain text so the
strategies still have a meaningful hierarchy to work with.

Hierarchy:

    collection (ROOT)
     ├─ document (GROUP)
     │   ├─ section (LEAF_CONTAINER)          ← H2 / H3 boundary
     │   │   └─ paragraph (LEAF)
     │   └─ ...
     └─ ...

Input shape (deliberately small):

    {
      "collection_id": "docs-bundle",
      "collection_name": "Onboarding Docs",
      "documents": [
        {"id": "readme", "title": "README", "content": "..."},
        {"id": "architecture", "title": "Architecture", "content": "..."},
      ],
    }

A single document can also be passed directly at the top level with
``content`` / ``title`` keys — the adapter wraps it into the
collection shape automatically.
"""

from __future__ import annotations

import re
from collections.abc import Iterable
from dataclasses import dataclass, field
from typing import Any

from workers.comprehension.corpus import CorpusUnit, UnitKind

# Markdown heading regex — matches # through ###### at the start of a line.
_HEADING_RE = re.compile(r"^(#{1,6})\s+(.+?)\s*$", re.MULTILINE)

# Paragraph split for plain-text fallback — two or more newlines.
_PARAGRAPH_SPLIT_RE = re.compile(r"\n\s*\n+")

# Default paragraph budget — we cap the number of leaves per section
# so a runaway document with thousands of paragraphs doesn't spawn
# thousands of leaf calls. Operators can override via the constructor.
DEFAULT_MAX_PARAGRAPHS_PER_SECTION = 64


@dataclass
class DocumentCorpus:
    """A CorpusSource built from a set of plain-text/markdown documents."""

    payload: dict[str, Any]
    corpus_id: str = ""
    corpus_type: str = "document"
    max_paragraphs_per_section: int = DEFAULT_MAX_PARAGRAPHS_PER_SECTION
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
                or "document-corpus"
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
        self._revision_fp = str(
            self.payload.get("revision_fingerprint")
            or self.payload.get("updated_at")
            or ""
        )
        collection_name = str(self.payload.get("collection_name") or "Documents")
        self._add_unit(CorpusUnit(
            id="collection",
            kind=UnitKind.ROOT,
            level=3,
            label=collection_name,
            metadata={"collection_id": self.corpus_id},
        ))

        documents = self._normalize_documents(self.payload)
        for doc in documents:
            self._build_document(doc)

    def _normalize_documents(self, payload: dict[str, Any]) -> list[dict[str, Any]]:
        docs_field = payload.get("documents")
        if isinstance(docs_field, list):
            return [d for d in docs_field if isinstance(d, dict)]
        if isinstance(payload.get("content"), str):
            # Single-document shortcut.
            return [{
                "id": payload.get("id") or payload.get("title") or "document",
                "title": payload.get("title") or "Document",
                "content": payload["content"],
            }]
        return []

    def _build_document(self, doc: dict[str, Any]) -> None:
        doc_id_raw = str(doc.get("id") or doc.get("title") or "doc")
        doc_title = str(doc.get("title") or doc_id_raw)
        doc_unit_id = f"doc:{doc_id_raw}"
        self._add_unit(CorpusUnit(
            id=doc_unit_id,
            kind=UnitKind.GROUP,
            level=2,
            label=doc_title,
            parent_id="collection",
            metadata={"document_id": doc_id_raw},
        ))

        content = str(doc.get("content") or "").strip()
        if not content:
            # Empty document — synthesize a single section/leaf so the
            # document still appears in the tree.
            self._add_empty_section(doc_id_raw, doc_unit_id, doc_title)
            return

        sections = _split_markdown_sections(content)
        if not sections:
            # Fallback: plain-text paragraph splitting with a single
            # synthetic section.
            paragraphs = _split_paragraphs(content, self.max_paragraphs_per_section)
            self._add_section_with_paragraphs(
                doc_id_raw,
                doc_unit_id,
                section_id="section",
                section_title=doc_title,
                paragraphs=paragraphs,
            )
            return

        for sec_idx, (heading, body) in enumerate(sections):
            paragraphs = _split_paragraphs(body, self.max_paragraphs_per_section)
            self._add_section_with_paragraphs(
                doc_id_raw,
                doc_unit_id,
                section_id=f"sec-{sec_idx}",
                section_title=heading,
                paragraphs=paragraphs,
            )

    def _add_empty_section(
        self,
        doc_id_raw: str,
        doc_unit_id: str,
        doc_title: str,
    ) -> None:
        sec_unit_id = f"sec:{doc_id_raw}:empty"
        self._add_unit(CorpusUnit(
            id=sec_unit_id,
            kind=UnitKind.LEAF_CONTAINER,
            level=1,
            label=doc_title,
            parent_id=doc_unit_id,
            metadata={"document_id": doc_id_raw},
        ))
        leaf_id = f"para:{doc_id_raw}:empty"
        self._add_unit(CorpusUnit(
            id=leaf_id,
            kind=UnitKind.LEAF,
            level=0,
            label="(empty document)",
            parent_id=sec_unit_id,
            metadata={"document_id": doc_id_raw},
        ))
        self._leaf_texts[leaf_id] = f"Document `{doc_title}` is empty."

    def _add_section_with_paragraphs(
        self,
        doc_id_raw: str,
        doc_unit_id: str,
        *,
        section_id: str,
        section_title: str,
        paragraphs: list[str],
    ) -> None:
        sec_unit_id = f"sec:{doc_id_raw}:{section_id}"
        self._add_unit(CorpusUnit(
            id=sec_unit_id,
            kind=UnitKind.LEAF_CONTAINER,
            level=1,
            label=section_title[:80],
            parent_id=doc_unit_id,
            metadata={
                "document_id": doc_id_raw,
                "section_id": section_id,
            },
        ))

        if not paragraphs:
            leaf_id = f"para:{doc_id_raw}:{section_id}:empty"
            self._add_unit(CorpusUnit(
                id=leaf_id,
                kind=UnitKind.LEAF,
                level=0,
                label="(no paragraphs)",
                parent_id=sec_unit_id,
                metadata={"document_id": doc_id_raw},
            ))
            self._leaf_texts[leaf_id] = f"Section `{section_title}` has no paragraphs."
            return

        for idx, paragraph in enumerate(paragraphs):
            leaf_id = f"para:{doc_id_raw}:{section_id}:{idx}"
            label = paragraph.splitlines()[0][:80] if paragraph.splitlines() else "paragraph"
            self._add_unit(CorpusUnit(
                id=leaf_id,
                kind=UnitKind.LEAF,
                level=0,
                label=label,
                parent_id=sec_unit_id,
                size_tokens=max(20, len(paragraph) // 4),
                metadata={
                    "document_id": doc_id_raw,
                    "section_id": section_id,
                    "paragraph_index": idx,
                },
            ))
            self._leaf_texts[leaf_id] = paragraph.strip()

    def _add_unit(self, unit: CorpusUnit) -> None:
        self._units[unit.id] = unit
        if unit.parent_id:
            self._children_by_parent.setdefault(unit.parent_id, []).append(unit.id)


# ----------------------------------------------------------------------
# Splitting helpers


def _split_markdown_sections(content: str) -> list[tuple[str, str]]:
    """Split markdown content into (heading, body) pairs.

    Matches heading boundaries at the first H2 or H3; the H1 is
    treated as the document title and its body (anything before the
    first H2) is emitted as an "Overview" section when present.

    Returns an empty list when the content has no headings — callers
    fall back to paragraph-only splitting in that case.
    """
    headings = list(_HEADING_RE.finditer(content))
    if not headings:
        return []

    # Decide the split level: if there are any H2s, split on them;
    # otherwise split on H3 or below.
    split_level = min(len(h.group(1)) for h in headings)
    if split_level == 1:
        # If there are only H1s, bump to splitting on everything except
        # the single top H1 (treated as the doc title).
        split_level = 2 if any(len(h.group(1)) >= 2 for h in headings) else 1

    splits: list[tuple[int, str]] = []
    for h in headings:
        level = len(h.group(1))
        if level <= split_level:
            splits.append((h.start(), h.group(2)))

    if not splits:
        return []

    sections: list[tuple[str, str]] = []
    # Pre-heading preamble (H1 body).
    if splits[0][0] > 0:
        preamble = content[: splits[0][0]].strip()
        if preamble:
            sections.append(("Overview", preamble))

    for i, (start, title) in enumerate(splits):
        # Find the end of the heading line so the body starts after it.
        line_end = content.find("\n", start)
        body_start = line_end + 1 if line_end >= 0 else len(content)
        body_end = splits[i + 1][0] if i + 1 < len(splits) else len(content)
        body = content[body_start:body_end].strip()
        sections.append((title.strip(), body))
    return sections


def _split_paragraphs(text: str, max_paragraphs: int) -> list[str]:
    """Split body text into paragraphs, capped at ``max_paragraphs``.

    When the body has more paragraphs than the cap allows, the tail
    paragraphs are merged into the last slot so every word still makes
    it into the tree. This keeps the "every piece of the corpus
    contributes" invariant from the plan.
    """
    if not text.strip():
        return []
    chunks = [p.strip() for p in _PARAGRAPH_SPLIT_RE.split(text) if p.strip()]
    if len(chunks) <= max_paragraphs:
        return chunks
    # Merge overflow into the last slot.
    head = chunks[: max_paragraphs - 1]
    tail = "\n\n".join(chunks[max_paragraphs - 1:])
    return head + [tail]
