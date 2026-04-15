# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""CodeCorpus adapter.

Wraps a KnowledgeSnapshot dict (the JSON shape emitted by
``internal/knowledge/snapshot.go`` on the Go side) as a CorpusSource
that the comprehension strategies can consume.

Hierarchy:

    repo (ROOT)
     ├─ package (GROUP)
     │   ├─ file (LEAF_CONTAINER)
     │   │   └─ symbol / segment (LEAF)
     │   └─ file (LEAF_CONTAINER)
     │       └─ segment (LEAF)
     └─ ...

The snapshot includes modules, files (implicit via file_path on each
symbol), and symbols grouped by role (EntryPoints, PublicAPI, etc.).
We combine those into a single flat set of symbols per file and emit
each one as a leaf. When the snapshot has no structured symbols for a
file (e.g. a language we don't parse), we emit a single synthetic
"file-level" leaf so the file still contributes to the tree.

Leaf content is the symbol's signature + doc comment — the snapshot
does NOT carry raw source code (the Go side intentionally avoids
sending full files over gRPC). For Phase 3 this is enough to produce
meaningful summaries; Phase 4+ can optionally add a source-slicing
pass that fetches the raw body from the worker's repo cache.
"""

from __future__ import annotations

from collections.abc import Iterable
from dataclasses import dataclass, field
from typing import Any

from workers.comprehension.corpus import CorpusUnit, UnitKind, content_hash

SYMBOL_CHUNK_TARGET = 4
NOISY_SYMBOL_THRESHOLD = 6
INTEGRATION_PATH_MARKERS = (
    "/service",
    "/services/",
    "/integration",
    "/integrations/",
    "crawler",
    "scraper",
    "sheets",
    "client",
)


@dataclass
class CodeCorpus:
    """A CorpusSource built from a KnowledgeSnapshot dict."""

    snapshot: dict[str, Any]
    corpus_id: str = ""
    corpus_type: str = "code"
    _units: dict[str, CorpusUnit] = field(default_factory=dict)
    _children_by_parent: dict[str, list[str]] = field(default_factory=dict)
    _leaf_texts: dict[str, str] = field(default_factory=dict)
    _revision_fp: str = ""

    def __post_init__(self) -> None:
        # Use the snapshot's repository id as the corpus id by default;
        # fall back to a placeholder so the CorpusSource protocol is
        # always satisfied even for malformed snapshots.
        if not self.corpus_id:
            self.corpus_id = str(self.snapshot.get("repository_id") or "code-corpus")
        self._build()

    # ------------------------------------------------------------------
    # CorpusSource interface

    def root(self) -> CorpusUnit:
        return self._units["repo"]

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
    # Construction — happens once in __post_init__

    def _build(self) -> None:
        rev = self.snapshot.get("source_revision") or {}
        self._revision_fp = str(rev.get("content_fingerprint") or rev.get("commit_sha") or "")

        repo_name = str(self.snapshot.get("repository_name") or "repository")
        self._add_unit(
            CorpusUnit(
                id="repo",
                kind=UnitKind.ROOT,
                level=3,
                label=repo_name,
                metadata={
                    "repository_id": str(self.snapshot.get("repository_id") or ""),
                    "repository_name": repo_name,
                    "file_count": int(self.snapshot.get("file_count") or 0),
                    "symbol_count": int(self.snapshot.get("symbol_count") or 0),
                },
            )
        )

        # --- Group symbols by file path ------------------------------
        # Pull every symbol the snapshot mentions into a flat set keyed
        # by file path. We de-dupe on symbol id so a symbol appearing in
        # both "entry_points" and "public_api" still counts once.
        symbols_by_file: dict[str, list[dict[str, Any]]] = {}
        seen_symbol_ids: set[str] = set()
        for group_key in (
            "entry_points",
            "public_api",
            "complex_symbols",
            "test_symbols",
            "high_fan_out_symbols",
            "high_fan_in_symbols",
        ):
            for sym in self.snapshot.get(group_key) or []:
                sym_id = str(sym.get("id") or "")
                if sym_id and sym_id in seen_symbol_ids:
                    continue
                if sym_id:
                    seen_symbol_ids.add(sym_id)
                file_path = str(sym.get("file_path") or "").strip()
                if not file_path:
                    continue
                symbols_by_file.setdefault(file_path, []).append(sym)

        # --- Module → files map --------------------------------------
        modules_raw = self.snapshot.get("modules") or []
        # Fall back to a single "(root)" module when the snapshot
        # didn't emit structural modules; every file lands there.
        if not modules_raw:
            modules_raw = [{"name": "(root)", "path": "", "file_count": 0}]

        # Bucket files into modules by longest-matching prefix. Files
        # without a matching module still need a home — we create an
        # "(unassigned)" package that groups them.
        module_paths = [
            (str(m.get("path") or ""), str(m.get("name") or m.get("path") or "(root)")) for m in modules_raw
        ]
        # Sort by path length desc so the most-specific prefix wins.
        module_paths.sort(key=lambda mp: -len(mp[0]))

        package_label_by_path: dict[str, str] = {}
        for path, label in module_paths:
            package_label_by_path.setdefault(path, label)

        files_by_module: dict[str, list[str]] = {}
        for file_path in symbols_by_file:
            matched_label = "(unassigned)"
            for path, label in module_paths:
                if path == "" or file_path.startswith(path + "/") or file_path == path:
                    matched_label = label
                    break
            files_by_module.setdefault(matched_label, []).append(file_path)

        # Deterministic ordering for stable tests and prompts.
        for files in files_by_module.values():
            files.sort()

        module_labels = sorted(files_by_module.keys())

        # --- Emit package / file / segment units --------------------
        for module_label in module_labels:
            package_id = f"package:{module_label}"
            self._add_unit(
                CorpusUnit(
                    id=package_id,
                    kind=UnitKind.GROUP,
                    level=2,
                    label=module_label,
                    parent_id="repo",
                    metadata={"module_label": module_label},
                )
            )

            for file_path in files_by_module[module_label]:
                file_id = f"file:{file_path}"
                language = _language_for_path(file_path)
                self._add_unit(
                    CorpusUnit(
                        id=file_id,
                        kind=UnitKind.LEAF_CONTAINER,
                        level=1,
                        label=_basename(file_path),
                        parent_id=package_id,
                        metadata={
                            "file_path": file_path,
                            "module_label": module_label,
                            "language": language,
                        },
                    )
                )

                symbols = symbols_by_file.get(file_path, [])
                if not symbols:
                    # Synthesize a single file-level leaf so the file
                    # still contributes content even without structured
                    # symbols.
                    leaf_id = f"leaf:{file_path}"
                    leaf_text = (
                        f"File `{file_path}` is part of the `{module_label}` module. "
                        "No structured symbols were extracted for this file."
                    )
                    self._add_unit(
                        CorpusUnit(
                            id=leaf_id,
                            kind=UnitKind.LEAF,
                            level=0,
                            label=_basename(file_path),
                            parent_id=file_id,
                            size_tokens=200,
                            content_hash=content_hash(leaf_text),
                            metadata={
                                "file_path": file_path,
                                "language": language,
                            },
                        )
                    )
                    self._leaf_texts[leaf_id] = leaf_text
                    continue

                symbol_groups = _group_symbols_for_file(file_path, symbols)
                for group_index, symbol_group in enumerate(symbol_groups, start=1):
                    if len(symbol_group) == 1:
                        sym = symbol_group[0]
                        sym_id_raw = str(sym.get("id") or sym.get("qualified_name") or sym.get("name") or "")
                        if not sym_id_raw:
                            continue
                        leaf_id = f"leaf:{file_path}#{sym_id_raw}"
                        name = str(sym.get("name") or sym_id_raw)
                        signature = str(sym.get("signature") or "")
                        doc = str(sym.get("doc_comment") or "")
                        kind = str(sym.get("kind") or "symbol")
                        start_line = int(sym.get("start_line") or 0)
                        end_line = int(sym.get("end_line") or 0)
                        body_lines = int(sym.get("line_count") or max(0, end_line - start_line))

                        leaf_text = _render_leaf_text(
                            name=name,
                            kind=kind,
                            signature=signature,
                            doc=doc,
                            file_path=file_path,
                            start_line=start_line,
                            end_line=end_line,
                        )
                        self._add_unit(
                            CorpusUnit(
                                id=leaf_id,
                                kind=UnitKind.LEAF,
                                level=0,
                                label=name,
                                parent_id=file_id,
                                size_tokens=max(50, body_lines * 8),
                                content_hash=content_hash(leaf_text),
                                metadata={
                                    "file_path": file_path,
                                    "language": language,
                                    "symbol_id": sym_id_raw,
                                    "symbol_name": name,
                                    "symbol_kind": kind,
                                    "start_line": start_line,
                                    "end_line": end_line,
                                },
                            )
                        )
                        self._leaf_texts[leaf_id] = leaf_text
                        continue

                    chunk_label = f"{_basename(file_path)} chunk {group_index}"
                    leaf_id = f"leaf:{file_path}#chunk:{group_index}"
                    leaf_text = _render_symbol_chunk_text(
                        file_path=file_path,
                        symbols=symbol_group,
                    )
                    total_lines = 0
                    for sym in symbol_group:
                        start_line = int(sym.get("start_line") or 0)
                        end_line = int(sym.get("end_line") or 0)
                        total_lines += int(sym.get("line_count") or max(0, end_line - start_line))
                    self._add_unit(
                        CorpusUnit(
                            id=leaf_id,
                            kind=UnitKind.LEAF,
                            level=0,
                            label=chunk_label,
                            parent_id=file_id,
                            size_tokens=max(150, total_lines * 8),
                            content_hash=content_hash(leaf_text),
                            metadata={
                                "file_path": file_path,
                                "language": language,
                                "symbol_count": len(symbol_group),
                                "symbol_names": [str(sym.get("name") or "") for sym in symbol_group],
                                "chunked": True,
                            },
                        )
                    )
                    self._leaf_texts[leaf_id] = leaf_text

        # --- Scope context fallback -----------------------------------
        # If the snapshot carries a scope_context (for scoped artifacts
        # like a single-file cliff note), we still want that file to
        # appear even if it didn't show up in the symbol groups above.
        scope_ctx = self.snapshot.get("scope_context") or {}
        target_file = scope_ctx.get("target_file") if isinstance(scope_ctx, dict) else None
        if isinstance(target_file, dict):
            file_path = str(target_file.get("path") or "")
            if file_path and f"file:{file_path}" not in self._units:
                language = _language_for_path(file_path)
                # Ensure the unassigned package exists.
                package_id = "package:(unassigned)"
                if package_id not in self._units:
                    self._add_unit(
                        CorpusUnit(
                            id=package_id,
                            kind=UnitKind.GROUP,
                            level=2,
                            label="(unassigned)",
                            parent_id="repo",
                        )
                    )
                file_id = f"file:{file_path}"
                self._add_unit(
                    CorpusUnit(
                        id=file_id,
                        kind=UnitKind.LEAF_CONTAINER,
                        level=1,
                        label=_basename(file_path),
                        parent_id=package_id,
                        metadata={"file_path": file_path, "language": language},
                    )
                )
                leaf_id = f"leaf:{file_path}"
                self._add_unit(
                    CorpusUnit(
                        id=leaf_id,
                        kind=UnitKind.LEAF,
                        level=0,
                        label=_basename(file_path),
                        parent_id=file_id,
                        metadata={"file_path": file_path, "language": language},
                    )
                )
                self._leaf_texts[leaf_id] = (
                    f"Focused scope file `{file_path}`. The snapshot provided no structured symbols for this file."
                )

    def _add_unit(self, unit: CorpusUnit) -> None:
        self._units[unit.id] = unit
        if unit.parent_id:
            self._children_by_parent.setdefault(unit.parent_id, []).append(unit.id)


# ----------------------------------------------------------------------
# Helpers

_EXT_LANG = {
    ".go": "go",
    ".py": "python",
    ".ts": "typescript",
    ".tsx": "typescript",
    ".js": "javascript",
    ".jsx": "javascript",
    ".rs": "rust",
    ".java": "java",
    ".cs": "csharp",
    ".cpp": "cpp",
    ".cc": "cpp",
    ".c": "c",
    ".h": "c",
    ".rb": "ruby",
    ".php": "php",
    ".swift": "swift",
    ".kt": "kotlin",
    ".md": "markdown",
    ".sql": "sql",
    ".yml": "yaml",
    ".yaml": "yaml",
    ".toml": "toml",
    ".json": "json",
    ".sh": "bash",
}


def _language_for_path(path: str) -> str:
    idx = path.rfind(".")
    if idx < 0:
        return "unknown"
    return _EXT_LANG.get(path[idx:].lower(), "unknown")


def _basename(path: str) -> str:
    if not path:
        return path
    idx = path.rfind("/")
    if idx < 0:
        return path
    return path[idx + 1 :]


def _render_leaf_text(
    *,
    name: str,
    kind: str,
    signature: str,
    doc: str,
    file_path: str,
    start_line: int,
    end_line: int,
) -> str:
    """Build the leaf 'code' text for the summarization prompt.

    The snapshot doesn't ship raw source bodies over gRPC, so we feed
    the model the symbol's signature, doc comment, and structural
    metadata. This is enough for a useful leaf summary — the model can
    describe what the symbol does from its name + signature + doc.
    """
    parts: list[str] = [f"{kind} {name}"]
    if signature:
        parts.append(signature.strip())
    if doc:
        parts.append(f"/** {doc.strip()} */")
    location = f"// {file_path}"
    if start_line and end_line:
        location += f":{start_line}-{end_line}"
    parts.append(location)
    return "\n".join(parts)


def _render_symbol_chunk_text(*, file_path: str, symbols: list[dict[str, Any]]) -> str:
    parts = [f"File summary chunk for {file_path}"]
    for sym in symbols:
        sym_id_raw = str(sym.get("id") or sym.get("qualified_name") or sym.get("name") or "")
        name = str(sym.get("name") or sym_id_raw)
        signature = str(sym.get("signature") or "")
        doc = str(sym.get("doc_comment") or "")
        kind = str(sym.get("kind") or "symbol")
        start_line = int(sym.get("start_line") or 0)
        end_line = int(sym.get("end_line") or 0)
        parts.append("")
        parts.append(
            _render_leaf_text(
                name=name,
                kind=kind,
                signature=signature,
                doc=doc,
                file_path=file_path,
                start_line=start_line,
                end_line=end_line,
            )
        )
    return "\n".join(parts)


def _group_symbols_for_file(file_path: str, symbols: list[dict[str, Any]]) -> list[list[dict[str, Any]]]:
    if not _should_chunk_symbols(file_path, symbols):
        return [[sym] for sym in symbols]
    grouped: list[list[dict[str, Any]]] = []
    for idx in range(0, len(symbols), SYMBOL_CHUNK_TARGET):
        grouped.append(symbols[idx : idx + SYMBOL_CHUNK_TARGET])
    return grouped


def _should_chunk_symbols(file_path: str, symbols: list[dict[str, Any]]) -> bool:
    if len(symbols) < NOISY_SYMBOL_THRESHOLD:
        return False
    lowered = file_path.lower()
    if any(marker in lowered for marker in INTEGRATION_PATH_MARKERS):
        return True
    total_body_lines = 0
    for sym in symbols:
        start_line = int(sym.get("start_line") or 0)
        end_line = int(sym.get("end_line") or 0)
        total_body_lines += int(sym.get("line_count") or max(0, end_line - start_line))
    avg_body_lines = total_body_lines / max(len(symbols), 1)
    return avg_body_lines <= 30
