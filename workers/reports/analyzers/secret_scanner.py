# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Secret and credential pattern detection.

Scans for exposed credentials, hardcoded secrets, and insecure
secret management patterns.
"""

from __future__ import annotations

import os
import re
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class SecretScanResult:
    """Results of scanning for secret/credential patterns."""

    finding_count: int = 0
    findings: list[dict[str, str]] = field(default_factory=list)
    has_env_file: bool = False
    has_env_example: bool = False
    env_vars_referenced: list[str] = field(default_factory=list)
    secret_management_tool: str = ""

    def to_dict(self) -> dict:
        return {
            "finding_count": self.finding_count,
            "findings": self.findings[:50],
            "has_env_file": self.has_env_file,
            "has_env_example": self.has_env_example,
            "env_vars_referenced": self.env_vars_referenced[:30],
            "secret_management_tool": self.secret_management_tool,
        }


_SECRET_PATTERNS = [
    (re.compile(r'(?:password|passwd|pwd)\s*[=:]\s*["\'][^"\']{4,}["\']', re.IGNORECASE), "hardcoded_password"),
    (re.compile(r'(?:api[_-]?key|apikey)\s*[=:]\s*["\'][a-zA-Z0-9_\-]{10,}["\']', re.IGNORECASE), "hardcoded_api_key"),
    (re.compile(r'(?:secret|token)\s*[=:]\s*["\'][a-zA-Z0-9_\-]{10,}["\']', re.IGNORECASE), "hardcoded_secret"),
    (re.compile(r'SUPABASE_SERVICE_ROLE_KEY|SUPABASE_SERVICE_KEY|service.role.key', re.IGNORECASE), "service_role_key_reference"),
    (re.compile(r'private[_-]?key\s*[=:]\s*["\']', re.IGNORECASE), "private_key_reference"),
    (re.compile(r'-----BEGIN (?:RSA |EC |DSA )?PRIVATE KEY-----', re.IGNORECASE), "embedded_private_key"),
]

_ENV_VAR_PATTERN = re.compile(r'(?:process\.env|os\.environ|os\.getenv)\s*[\[.(]\s*["\']?([A-Z_][A-Z0-9_]*)', re.IGNORECASE)

_SECRET_MGMT_MARKERS = {
    "vault": "HashiCorp Vault",
    "aws-secretsmanager": "AWS Secrets Manager",
    "google-secret-manager": "Google Secret Manager",
    "azure-keyvault": "Azure Key Vault",
    "doppler": "Doppler",
    "infisical": "Infisical",
}

_SCAN_EXTENSIONS = {".ts", ".tsx", ".js", ".jsx", ".py", ".go", ".java", ".rb", ".rs", ".php", ".yaml", ".yml", ".toml", ".json"}


def scan_secrets(repo_path: str) -> SecretScanResult:
    """Scan a repository for secret/credential patterns."""
    result = SecretScanResult()
    root = Path(repo_path)

    if not root.exists():
        return result

    env_vars: set[str] = set()

    for dirpath, dirnames, filenames in os.walk(root):
        rel_dir = os.path.relpath(dirpath, root)
        if any(skip in rel_dir.split(os.sep) for skip in [
            "node_modules", ".git", "vendor", ".venv", "venv",
            "__pycache__", ".next", "dist", "build",
        ]):
            continue

        for filename in filenames:
            rel_path = os.path.relpath(os.path.join(dirpath, filename), root)

            # Check for .env files
            if filename == ".env" or filename == ".env.local":
                result.has_env_file = True
            if filename == ".env.example" or filename == ".env.sample":
                result.has_env_example = True

            ext = os.path.splitext(filename)[1].lower()
            if ext not in _SCAN_EXTENSIONS:
                continue

            filepath = os.path.join(dirpath, filename)
            try:
                content = Path(filepath).read_text(errors="ignore")[:50000]
            except OSError:
                continue

            # Check secret patterns
            for pattern, finding_type in _SECRET_PATTERNS:
                matches = pattern.findall(content)
                if matches:
                    result.findings.append({
                        "type": finding_type,
                        "file": rel_path,
                        "count": len(matches) if isinstance(matches, list) else 1,
                    })

            # Collect env var references
            for match in _ENV_VAR_PATTERN.finditer(content):
                env_vars.add(match.group(1))

            # Check for secret management tools
            content_lower = content.lower()
            for marker, tool in _SECRET_MGMT_MARKERS.items():
                if marker in content_lower:
                    result.secret_management_tool = tool

    result.finding_count = len(result.findings)
    result.env_vars_referenced = sorted(env_vars)
    return result
