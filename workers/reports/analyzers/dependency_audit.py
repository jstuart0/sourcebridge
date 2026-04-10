# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Dependency vulnerability audit.

Runs npm audit and/or pip audit if available, parses results.
Auto-detects which tools are applicable based on repo content.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class DependencyAuditResult:
    vulnerability_count: int = 0
    critical: int = 0
    high: int = 0
    medium: int = 0
    low: int = 0
    outdated_count: int = 0
    vulnerabilities: list[dict[str, str]] = field(default_factory=list)
    tool_used: str = ""
    error: str = ""

    def to_dict(self) -> dict:
        return {
            "vulnerability_count": self.vulnerability_count,
            "critical": self.critical,
            "high": self.high,
            "medium": self.medium,
            "low": self.low,
            "outdated_count": self.outdated_count,
            "vulnerabilities": self.vulnerabilities[:100],
            "tool_used": self.tool_used,
            "error": self.error,
        }


def audit_dependencies(repo_path: str) -> DependencyAuditResult:
    """Run dependency audit on a repository."""
    root = Path(repo_path)
    result = DependencyAuditResult()

    if not root.exists():
        return result

    # Try npm audit
    if (root / "package.json").exists() and shutil.which("npm"):
        npm_result = _run_npm_audit(root)
        if npm_result:
            return npm_result

    # Try pip audit
    if (root / "requirements.txt").exists() or (root / "pyproject.toml").exists():
        if shutil.which("pip-audit"):
            pip_result = _run_pip_audit(root)
            if pip_result:
                return pip_result

    return result


def _run_npm_audit(root: Path) -> DependencyAuditResult | None:
    """Run npm audit and parse JSON output."""
    try:
        out = subprocess.run(
            ["npm", "audit", "--json"],
            cwd=str(root),
            capture_output=True, text=True, timeout=120,
        )
        data = json.loads(out.stdout) if out.stdout else {}
    except (subprocess.TimeoutExpired, json.JSONDecodeError, OSError) as e:
        return DependencyAuditResult(tool_used="npm audit", error=str(e))

    result = DependencyAuditResult(tool_used="npm audit")
    vuln_metadata = data.get("metadata", {}).get("vulnerabilities", {})
    result.critical = vuln_metadata.get("critical", 0)
    result.high = vuln_metadata.get("high", 0)
    result.medium = vuln_metadata.get("moderate", 0)
    result.low = vuln_metadata.get("low", 0)
    result.vulnerability_count = result.critical + result.high + result.medium + result.low

    advisories = data.get("advisories", data.get("vulnerabilities", {}))
    if isinstance(advisories, dict):
        for name, info in list(advisories.items())[:100]:
            if isinstance(info, dict):
                result.vulnerabilities.append({
                    "name": name,
                    "severity": info.get("severity", "unknown"),
                    "title": info.get("title", info.get("via", [{}])[0].get("title", "") if isinstance(info.get("via"), list) and info.get("via") else ""),
                    "range": info.get("range", ""),
                })

    return result


def _run_pip_audit(root: Path) -> DependencyAuditResult | None:
    """Run pip-audit and parse JSON output."""
    try:
        out = subprocess.run(
            ["pip-audit", "--format", "json", "--requirement", "requirements.txt"],
            cwd=str(root),
            capture_output=True, text=True, timeout=120,
        )
        data = json.loads(out.stdout) if out.stdout else []
    except (subprocess.TimeoutExpired, json.JSONDecodeError, OSError) as e:
        return DependencyAuditResult(tool_used="pip-audit", error=str(e))

    result = DependencyAuditResult(tool_used="pip-audit")
    if isinstance(data, list):
        for item in data[:100]:
            severity = item.get("fix_versions", [""])[0] if item.get("fix_versions") else ""
            result.vulnerabilities.append({
                "name": item.get("name", ""),
                "severity": "high",
                "title": item.get("description", ""),
                "version": item.get("version", ""),
            })
        result.vulnerability_count = len(result.vulnerabilities)
        result.high = result.vulnerability_count  # pip-audit doesn't provide severity tiers

    return result
