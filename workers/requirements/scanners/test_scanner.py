"""Scanner for extracting specs from test files."""

from __future__ import annotations

import os
import re

from workers.requirements.spec_models import CandidateSpec

# Test file patterns per language
TEST_FILE_PATTERNS: dict[str, list[str]] = {
    "go": ["_test.go"],
    "python": ["test_", "_test.py"],
    "typescript": [".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx"],
    "javascript": [".test.js", ".spec.js", ".test.jsx", ".spec.jsx"],
    "java": ["Test.java", "Tests.java", "Spec.java"],
    "rust": [],  # Rust uses inline #[test] modules
    "csharp": ["Test.cs", "Tests.cs"],
    "ruby": ["_spec.rb", "_test.rb"],
}

# Regex patterns for extracting test function names
TEST_PATTERNS: dict[str, list[str]] = {
    "go": [
        r"func\s+(Test\w+)\s*\(\s*\w+\s+\*testing\.T\s*\)",
        r"func\s+\(\s*\w+\s+\*\w+\s*\)\s+(Test\w+)",
    ],
    "python": [
        r"def\s+(test_\w+)\s*\(",
        r"class\s+(Test\w+)\s*[:\(]",
    ],
    "typescript": [
        r'(?:it|test)\s*\(\s*["\'](.+?)["\']\s*,',
        r'describe\s*\(\s*["\'](.+?)["\']\s*,',
    ],
    "javascript": [
        r'(?:it|test)\s*\(\s*["\'](.+?)["\']\s*,',
        r'describe\s*\(\s*["\'](.+?)["\']\s*,',
    ],
    "java": [
        r"@Test\s*(?:\([^)]*\))?\s*\w+\s+\w+\s+(\w+)\s*\(",
        r"@(?:ParameterizedTest|RepeatedTest)\s*.*?\s+(\w+)\s*\(",
    ],
    "rust": [
        r"#\[test\]\s*(?:#\[.*?\]\s*)*fn\s+(\w+)",
        r"#\[tokio::test\]\s*(?:#\[.*?\]\s*)*(?:async\s+)?fn\s+(\w+)",
    ],
}

# Assertion patterns for extracting behavioral hints
ASSERTION_PATTERNS: dict[str, list[str]] = {
    "go": [
        r"(?:assert|require)\.\w+\(.*?\)",
        r"t\.(?:Error|Fatal|Fail)\w*\(",
    ],
    "python": [
        r"self\.assert\w+\(",
        r"assert\s+.+",
        r"pytest\.raises\(\s*(\w+)",
    ],
    "typescript": [
        r"expect\(.+?\)\.\w+\(",
        r"assert\.\w+\(",
    ],
    "javascript": [
        r"expect\(.+?\)\.\w+\(",
        r"assert\.\w+\(",
    ],
}


class TestFileScanner:
    """Extracts candidate specs from test files."""

    def is_test_file(self, path: str, language: str) -> bool:
        """Check if a file is a test file based on naming conventions."""
        basename = os.path.basename(path)

        patterns = TEST_FILE_PATTERNS.get(language, [])
        for pattern in patterns:
            if pattern.startswith("test_"):
                if basename.startswith("test_"):
                    return True
            elif basename.endswith(pattern):
                return True

        # Rust: check for inline test modules
        if language == "rust" and path.endswith(".rs"):
            return True  # Will be filtered by content in extract()

        # Check common test directory patterns
        parts = path.replace("\\", "/").split("/")
        test_dirs = {"tests", "test", "__tests__", "spec", "specs"}
        return any(p in test_dirs for p in parts)

    def extract(
        self,
        path: str,
        content: str,
        language: str,
        all_files: list,
    ) -> list[CandidateSpec]:
        """Extract candidate specs from a test file."""
        candidates: list[CandidateSpec] = []

        patterns = TEST_PATTERNS.get(language, [])
        if not patterns:
            return candidates

        lines = content.split("\n")
        repo_files = [f.path for f in all_files] if all_files else []

        # Extract test names with line numbers
        test_entries: list[tuple[str, int]] = []
        for i, line in enumerate(lines, 1):
            for pattern in patterns:
                for match in re.finditer(pattern, line):
                    test_name = match.group(1)
                    test_entries.append((test_name, i))

        if not test_entries:
            return candidates

        # Resolve the file under test
        file_under_test = self._resolve_file_under_test(path, language, repo_files)
        group_key = file_under_test or path

        # Extract assertion patterns for the whole file
        assertions = self._extract_assertions(content, language)

        for test_name, line_num in test_entries:
            candidates.append(
                CandidateSpec(
                    source="test",
                    source_file=path,
                    source_line=line_num,
                    raw_text=test_name,
                    group_key=group_key,
                    language=language,
                    metadata={
                        "test_name": test_name,
                        "assertions": assertions[:5],  # Limit to 5 most common
                        "test_type": "unit",
                    },
                )
            )

        return candidates

    def _resolve_file_under_test(self, test_file: str, language: str, repo_files: list[str]) -> str | None:
        """Map a test file to its implementation file."""
        basename = os.path.basename(test_file)
        dirpath = os.path.dirname(test_file)

        candidates: list[str] = []

        if language == "go":
            # user_test.go -> user.go
            impl = basename.replace("_test.go", ".go")
            candidates.append(os.path.join(dirpath, impl))

        elif language == "python":
            # test_user.py -> user.py
            if basename.startswith("test_"):
                impl = basename[5:]
                candidates.append(impl)
                candidates.append(os.path.join("src", impl))
                # Also try parent directory
                parent = os.path.dirname(dirpath)
                candidates.append(os.path.join(parent, impl))

        elif language in ("typescript", "javascript"):
            # user.test.ts -> user.ts
            for suffix in (
                ".test.ts",
                ".test.tsx",
                ".spec.ts",
                ".spec.tsx",
                ".test.js",
                ".spec.js",
                ".test.jsx",
                ".spec.jsx",
            ):
                if basename.endswith(suffix):
                    ext = ".ts" if "ts" in suffix else ".js"
                    if "tsx" in suffix:
                        ext = ".tsx"
                    impl = basename[: -len(suffix)] + ext
                    candidates.append(os.path.join(dirpath, impl))
                    # Check parent of __tests__ dir
                    if "__tests__" in dirpath:
                        parent = dirpath.replace("__tests__/", "").replace("__tests__", "")
                        candidates.append(os.path.join(parent, impl))
                    break

        elif language == "java":
            # UserServiceTest.java -> UserService.java
            for suffix in ("Test.java", "Tests.java", "Spec.java"):
                if basename.endswith(suffix):
                    impl = basename[: -len(suffix)] + ".java"
                    # Convert test path to main path
                    impl_path = test_file.replace("/test/", "/main/").replace(basename, impl)
                    candidates.append(impl_path)
                    break

        # Find the first candidate that exists in repo files
        normalized_repo = {f.replace("\\", "/") for f in repo_files}
        for candidate in candidates:
            normalized = candidate.replace("\\", "/")
            if normalized in normalized_repo:
                return normalized

        return candidates[0] if candidates else None

    def _extract_assertions(self, content: str, language: str) -> list[str]:
        """Extract assertion patterns from test file content."""
        assertions: list[str] = []
        patterns = ASSERTION_PATTERNS.get(language, [])

        for pattern in patterns:
            for match in re.finditer(pattern, content):
                text = match.group(0)
                # Normalize to just the assertion method name
                if "." in text:
                    method = text.split("(")[0].strip()
                    if method not in assertions:
                        assertions.append(method)

        return assertions
