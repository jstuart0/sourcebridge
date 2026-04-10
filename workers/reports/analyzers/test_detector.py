# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Test framework and coverage detection.

Scans a repository directory for test files, test frameworks,
and coverage configurations.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class TestDetectionResult:
    """Results of scanning a repository for test evidence."""

    test_file_count: int = 0
    test_files: list[str] = field(default_factory=list)
    frameworks: list[str] = field(default_factory=list)
    has_coverage_config: bool = False
    coverage_tool: str = ""
    has_test_script: bool = False
    test_directory: str = ""

    def to_dict(self) -> dict:
        return {
            "test_file_count": self.test_file_count,
            "test_files": self.test_files[:20],  # cap for report data
            "frameworks": self.frameworks,
            "has_coverage_config": self.has_coverage_config,
            "coverage_tool": self.coverage_tool,
            "has_test_script": self.has_test_script,
            "test_directory": self.test_directory,
        }


# Framework detection by config file presence
_FRAMEWORK_MARKERS = {
    "jest.config.js": "Jest",
    "jest.config.ts": "Jest",
    "jest.config.mjs": "Jest",
    "vitest.config.ts": "Vitest",
    "vitest.config.js": "Vitest",
    "vitest.config.mjs": "Vitest",
    "cypress.config.js": "Cypress",
    "cypress.config.ts": "Cypress",
    "playwright.config.ts": "Playwright",
    "playwright.config.js": "Playwright",
    "pytest.ini": "pytest",
    "pyproject.toml": None,  # check for [tool.pytest] inside
    "setup.cfg": None,  # check for [tool:pytest]
    ".mocharc.yml": "Mocha",
    ".mocharc.json": "Mocha",
    "karma.conf.js": "Karma",
    "phpunit.xml": "PHPUnit",
    "phpunit.xml.dist": "PHPUnit",
}

# Coverage config markers
_COVERAGE_MARKERS = {
    ".nycrc": "NYC/Istanbul",
    ".nycrc.json": "NYC/Istanbul",
    ".c8rc.json": "c8",
    "codecov.yml": "Codecov",
    ".codecov.yml": "Codecov",
    ".coveragerc": "Coverage.py",
}

# Test file patterns
_TEST_PATTERNS = {
    ".test.ts", ".test.tsx", ".test.js", ".test.jsx",
    ".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx",
    "_test.go", "_test.py",
    "test_",  # Python prefix
}


def detect_tests(repo_path: str) -> TestDetectionResult:
    """Scan a repository for test evidence."""
    result = TestDetectionResult()
    root = Path(repo_path)

    if not root.exists():
        return result

    seen_frameworks: set[str] = set()
    test_files: list[str] = []

    for dirpath, dirnames, filenames in os.walk(root):
        # Skip common non-source dirs
        rel_dir = os.path.relpath(dirpath, root)
        if any(skip in rel_dir.split(os.sep) for skip in [
            "node_modules", ".git", "vendor", ".venv", "venv",
            "__pycache__", ".next", "dist", "build",
        ]):
            continue

        for filename in filenames:
            rel_path = os.path.relpath(os.path.join(dirpath, filename), root)

            # Check framework markers
            if filename in _FRAMEWORK_MARKERS:
                fw = _FRAMEWORK_MARKERS[filename]
                if fw:
                    seen_frameworks.add(fw)
                elif filename == "pyproject.toml":
                    try:
                        content = Path(os.path.join(dirpath, filename)).read_text(errors="ignore")
                        if "[tool.pytest" in content:
                            seen_frameworks.add("pytest")
                    except OSError:
                        pass

            # Check coverage markers
            if filename in _COVERAGE_MARKERS:
                result.has_coverage_config = True
                result.coverage_tool = _COVERAGE_MARKERS[filename]

            # Check test files
            is_test = False
            for pattern in _TEST_PATTERNS:
                if pattern.startswith("test_"):
                    if filename.startswith("test_") and filename.endswith(".py"):
                        is_test = True
                        break
                elif filename.endswith(pattern):
                    is_test = True
                    break

            if is_test:
                test_files.append(rel_path)

        # Detect test directories
        for dirname in dirnames:
            if dirname in ("tests", "test", "__tests__", "spec", "specs"):
                if not result.test_directory:
                    result.test_directory = os.path.relpath(
                        os.path.join(dirpath, dirname), root
                    )

    # Check package.json for test script
    pkg_json = root / "package.json"
    if pkg_json.exists():
        try:
            import json
            pkg = json.loads(pkg_json.read_text(errors="ignore"))
            scripts = pkg.get("scripts", {})
            if "test" in scripts:
                result.has_test_script = True
                # Detect framework from test command
                cmd = scripts["test"]
                if "jest" in cmd:
                    seen_frameworks.add("Jest")
                elif "vitest" in cmd:
                    seen_frameworks.add("Vitest")
                elif "mocha" in cmd:
                    seen_frameworks.add("Mocha")
        except (OSError, json.JSONDecodeError):
            pass

    result.test_file_count = len(test_files)
    result.test_files = sorted(test_files)
    result.frameworks = sorted(seen_frameworks)
    return result
