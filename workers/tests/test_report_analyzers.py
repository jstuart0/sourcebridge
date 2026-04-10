# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Tests for report analyzers."""

import os
import tempfile
from pathlib import Path

from workers.reports.analyzers.test_detector import detect_tests
from workers.reports.analyzers.auth_detector import detect_auth
from workers.reports.analyzers.secret_scanner import scan_secrets
from workers.reports.analyzers.cicd_detector import detect_cicd
from workers.reports.analyzers.git_analyzer import analyze_git
from workers.reports.analyzers.owasp_scanner import is_owasp_available, scan_owasp


def _create_fixture(tmpdir: str, files: dict[str, str]) -> str:
    """Create a temporary file structure for testing."""
    for path, content in files.items():
        full = os.path.join(tmpdir, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        Path(full).write_text(content)
    return tmpdir


# --- Test Detector ---

def test_detect_tests_empty_repo():
    with tempfile.TemporaryDirectory() as tmp:
        result = detect_tests(tmp)
        assert result.test_file_count == 0
        assert result.frameworks == []


def test_detect_tests_jest():
    with tempfile.TemporaryDirectory() as tmp:
        _create_fixture(tmp, {
            "jest.config.ts": "export default {}",
            "src/__tests__/auth.test.ts": "test('works', () => {})",
            "src/__tests__/api.test.ts": "test('works', () => {})",
            "package.json": '{"scripts": {"test": "jest"}}',
        })
        result = detect_tests(tmp)
        assert "Jest" in result.frameworks
        assert result.test_file_count == 2
        assert result.has_test_script


def test_detect_tests_pytest():
    with tempfile.TemporaryDirectory() as tmp:
        _create_fixture(tmp, {
            "tests/test_auth.py": "def test_login(): pass",
            "tests/test_api.py": "def test_endpoint(): pass",
            "pyproject.toml": "[tool.pytest.ini_options]\ntestpaths = ['tests']",
        })
        result = detect_tests(tmp)
        assert "pytest" in result.frameworks
        assert result.test_file_count == 2
        assert result.test_directory == "tests"


# --- Auth Detector ---

def test_detect_auth_empty():
    with tempfile.TemporaryDirectory() as tmp:
        result = detect_auth(tmp)
        assert result.provider == "none_detected"
        assert result.patterns == []


def test_detect_auth_supabase():
    with tempfile.TemporaryDirectory() as tmp:
        _create_fixture(tmp, {
            "src/auth.ts": 'import { createClient } from "@supabase/supabase-js"\nconst supabase = createClient(url, key)\nsupabase.auth.getUser()',
        })
        result = detect_auth(tmp)
        assert "Supabase Auth" in result.patterns
        assert result.provider == "supabase"


def test_detect_auth_sso_and_mfa():
    with tempfile.TemporaryDirectory() as tmp:
        _create_fixture(tmp, {
            "src/auth.ts": 'SAML integration with OneLogin\nconst mfa = require("totp")',
        })
        result = detect_auth(tmp)
        assert result.has_sso
        assert result.has_mfa


# --- Secret Scanner ---

def test_scan_secrets_empty():
    with tempfile.TemporaryDirectory() as tmp:
        result = scan_secrets(tmp)
        assert result.finding_count == 0


def test_scan_secrets_env_file():
    with tempfile.TemporaryDirectory() as tmp:
        _create_fixture(tmp, {
            ".env": "SECRET=value",
            ".env.example": "SECRET=placeholder",
        })
        result = scan_secrets(tmp)
        assert result.has_env_file
        assert result.has_env_example


def test_scan_secrets_hardcoded():
    with tempfile.TemporaryDirectory() as tmp:
        _create_fixture(tmp, {
            "src/config.ts": 'const apiKey = "sk_live_1234567890abcdef";\nprocess.env.DATABASE_URL',
        })
        result = scan_secrets(tmp)
        assert result.finding_count > 0
        assert "DATABASE_URL" in result.env_vars_referenced


# --- CI/CD Detector ---

def test_detect_cicd_empty():
    with tempfile.TemporaryDirectory() as tmp:
        result = detect_cicd(tmp)
        assert result.tools == []


def test_detect_cicd_docker_and_github():
    with tempfile.TemporaryDirectory() as tmp:
        _create_fixture(tmp, {
            "Dockerfile": "FROM node:18",
            ".github/workflows/deploy.yml": "name: Deploy",
            "vercel.json": "{}",
        })
        result = detect_cicd(tmp)
        assert result.has_dockerfile
        assert result.has_github_actions
        assert result.has_vercel
        assert "Docker" in result.tools
        assert "GitHub Actions" in result.tools
        assert "Vercel" in result.tools


# --- Git Analyzer ---

def test_analyze_git_no_repo():
    with tempfile.TemporaryDirectory() as tmp:
        result = analyze_git(tmp)
        assert result.contributor_count == 0
        assert result.total_commits == 0


# --- OWASP Scanner ---

def test_owasp_not_available():
    """OWASP scanner returns disabled when not configured."""
    # Ensure env var is not set
    os.environ.pop("SOURCEBRIDGE_OWASP_ENABLED", None)
    assert not is_owasp_available()
    result = scan_owasp("/tmp/fake")
    assert not result.enabled
    assert "not configured" in result.error
