"""Scanner for extracting specs from doc comments (godoc, JSDoc, docstrings, Javadoc)."""

from __future__ import annotations

import re

from workers.requirements.spec_models import CandidateSpec

# Behavioral keywords that indicate a comment describes a requirement
BEHAVIORAL_KEYWORDS = {
    "creates", "returns", "validates", "processes", "handles", "ensures",
    "verifies", "checks", "computes", "transforms", "fetches", "stores",
    "deletes", "updates", "authorizes", "authenticates",
    "must", "should", "shall", "required", "expected", "guarantee",
    "invariant", "precondition", "postcondition",
    "if", "when", "unless", "provided that", "given that",
}

# Auto-generated markers to skip
AUTO_GENERATED_MARKERS = [
    "Code generated",
    "DO NOT EDIT",
    "@Generated",
    "auto-generated",
    "AUTO GENERATED",
    "This file is generated",
]

MIN_WORD_COUNT = 20


class DocCommentScanner:
    """Extracts candidate specs from documentation comments."""

    def extract(
        self, path: str, content: str, language: str
    ) -> list[CandidateSpec]:
        """Extract candidate specs from doc comments in a source file."""
        # Skip auto-generated files
        first_500 = content[:500]
        for marker in AUTO_GENERATED_MARKERS:
            if marker in first_500:
                return []

        if language == "go":
            return self._extract_go(path, content)
        elif language == "python":
            return self._extract_python(path, content)
        elif language in ("typescript", "javascript"):
            return self._extract_jsdoc(path, content, language)
        elif language == "java":
            return self._extract_javadoc(path, content)
        else:
            return []

    def _extract_go(self, path: str, content: str) -> list[CandidateSpec]:
        """Extract godoc comments from Go files."""
        candidates: list[CandidateSpec] = []
        lines = content.split("\n")

        i = 0
        while i < len(lines):
            line = lines[i]

            # Look for function/type declarations preceded by comments
            func_match = re.match(r"func\s+(?:\([^)]+\)\s+)?(\w+)", line)
            type_match = re.match(r"type\s+(\w+)\s+", line)

            if func_match or type_match:
                symbol_name = (func_match or type_match).group(1)

                # Collect preceding // comments
                comment_lines: list[str] = []
                j = i - 1
                while j >= 0 and lines[j].strip().startswith("//"):
                    comment_lines.insert(0, lines[j].strip().lstrip("/ ").strip())
                    j -= 1

                if comment_lines:
                    comment_text = " ".join(comment_lines)
                    comment_start = j + 2  # 1-indexed

                    if self._is_meaningful(comment_text):
                        keywords = self._find_keywords(comment_text)
                        candidates.append(
                            CandidateSpec(
                                source="comment",
                                source_file=path,
                                source_line=comment_start,
                                raw_text=comment_text,
                                group_key=f"{path}:{symbol_name}",
                                language="go",
                                metadata={
                                    "comment_type": "function" if func_match else "type",
                                    "symbol_name": symbol_name,
                                    "behavioral_keywords": keywords,
                                },
                            )
                        )

            # Package-level godoc
            pkg_match = re.match(r"//\s*Package\s+(\w+)\s+(.*)", line)
            if pkg_match:
                pkg_name = pkg_match.group(1)
                comment_lines = [pkg_match.group(2)]
                j = i + 1
                while j < len(lines) and lines[j].strip().startswith("//"):
                    comment_lines.append(lines[j].strip().lstrip("/ ").strip())
                    j += 1

                comment_text = " ".join(comment_lines)
                if self._is_meaningful(comment_text):
                    keywords = self._find_keywords(comment_text)
                    candidates.append(
                        CandidateSpec(
                            source="comment",
                            source_file=path,
                            source_line=i + 1,
                            raw_text=comment_text,
                            group_key=f"{path}:package:{pkg_name}",
                            language="go",
                            metadata={
                                "comment_type": "package",
                                "symbol_name": pkg_name,
                                "behavioral_keywords": keywords,
                            },
                        )
                    )

            i += 1

        return candidates

    def _extract_python(self, path: str, content: str) -> list[CandidateSpec]:
        """Extract Python docstrings."""
        candidates: list[CandidateSpec] = []

        # Find triple-quoted docstrings after def/class declarations
        pattern = re.compile(
            r'((?:def|class)\s+(\w+)[^:]*:)\s*\n\s*"""(.*?)"""',
            re.DOTALL,
        )

        for match in pattern.finditer(content):
            symbol_name = match.group(2)
            docstring = match.group(3).strip()

            # Calculate line number
            line_num = content[: match.start()].count("\n") + 1

            if self._is_meaningful(docstring):
                keywords = self._find_keywords(docstring)
                comment_type = "class" if match.group(1).startswith("class") else "function"
                candidates.append(
                    CandidateSpec(
                        source="comment",
                        source_file=path,
                        source_line=line_num,
                        raw_text=docstring,
                        group_key=f"{path}:{symbol_name}",
                        language="python",
                        metadata={
                            "comment_type": comment_type,
                            "symbol_name": symbol_name,
                            "behavioral_keywords": keywords,
                        },
                    )
                )

        return candidates

    def _extract_jsdoc(self, path: str, content: str, language: str) -> list[CandidateSpec]:
        """Extract JSDoc comments from TypeScript/JavaScript files."""
        candidates: list[CandidateSpec] = []

        # Find /** ... */ blocks
        jsdoc_pattern = re.compile(r"/\*\*(.*?)\*/", re.DOTALL)

        for match in jsdoc_pattern.finditer(content):
            raw = match.group(1)
            # Clean up JSDoc formatting
            lines = [
                re.sub(r"^\s*\*\s?", "", line).strip()
                for line in raw.split("\n")
            ]
            cleaned = " ".join(line for line in lines if line)

            # Extract @description if present
            desc_match = re.search(r"@description\s+(.*?)(?=@|\Z)", cleaned, re.DOTALL)
            text = desc_match.group(1).strip() if desc_match else cleaned

            # Remove tag annotations for the text
            text = re.sub(r"@\w+\s+\{[^}]*\}\s+\w+\s+", "", text)
            text = re.sub(r"@\w+\s+", "", text)
            text = text.strip()

            if not self._is_meaningful(text):
                continue

            # Try to find the symbol name from the following line
            end_pos = match.end()
            following = content[end_pos : end_pos + 200]
            symbol_name = "unknown"
            sym_match = re.match(
                r"\s*(?:export\s+)?(?:async\s+)?(?:function|const|let|var|class)\s+(\w+)",
                following,
            )
            if sym_match:
                symbol_name = sym_match.group(1)

            line_num = content[: match.start()].count("\n") + 1
            keywords = self._find_keywords(text)

            candidates.append(
                CandidateSpec(
                    source="comment",
                    source_file=path,
                    source_line=line_num,
                    raw_text=text,
                    group_key=f"{path}:{symbol_name}",
                    language=language,
                    metadata={
                        "comment_type": "function",
                        "symbol_name": symbol_name,
                        "behavioral_keywords": keywords,
                    },
                )
            )

        return candidates

    def _extract_javadoc(self, path: str, content: str) -> list[CandidateSpec]:
        """Extract Javadoc comments from Java files."""
        candidates: list[CandidateSpec] = []

        javadoc_pattern = re.compile(r"/\*\*(.*?)\*/", re.DOTALL)

        for match in javadoc_pattern.finditer(content):
            raw = match.group(1)
            lines = [
                re.sub(r"^\s*\*\s?", "", line).strip()
                for line in raw.split("\n")
            ]
            # Take everything before the first @param/@return/@throws
            text_lines: list[str] = []
            for line in lines:
                if line.startswith("@"):
                    break
                if line:
                    text_lines.append(line)

            text = " ".join(text_lines).strip()

            if not self._is_meaningful(text):
                continue

            end_pos = match.end()
            following = content[end_pos : end_pos + 200]
            symbol_name = "unknown"
            sym_match = re.match(
                r"\s*(?:public|private|protected)?\s*(?:static\s+)?(?:(?:final|abstract|synchronized)\s+)*\w+\s+(\w+)\s*\(",
                following,
            )
            if sym_match:
                symbol_name = sym_match.group(1)

            line_num = content[: match.start()].count("\n") + 1
            keywords = self._find_keywords(text)

            candidates.append(
                CandidateSpec(
                    source="comment",
                    source_file=path,
                    source_line=line_num,
                    raw_text=text,
                    group_key=f"{path}:{symbol_name}",
                    language="java",
                    metadata={
                        "comment_type": "function",
                        "symbol_name": symbol_name,
                        "behavioral_keywords": keywords,
                    },
                )
            )

        return candidates

    def _is_meaningful(self, text: str) -> bool:
        """Check if a comment is meaningful enough to be a spec candidate."""
        words = text.split()
        if len(words) < MIN_WORD_COUNT:
            return False
        return len(self._find_keywords(text)) > 0

    def _find_keywords(self, text: str) -> list[str]:
        """Find behavioral keywords in text."""
        text_lower = text.lower()
        found: list[str] = []
        for kw in BEHAVIORAL_KEYWORDS:
            if kw in text_lower:
                found.append(kw)
        return found
