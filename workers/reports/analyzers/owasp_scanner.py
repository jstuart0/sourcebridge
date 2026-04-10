# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""OWASP security scanner wrapper (optional).

Wraps Semgrep OSS for OWASP Top 10 scanning. Disabled by default.
Only available when Semgrep is installed on the system PATH.

Configuration via environment variables:
  SOURCEBRIDGE_OWASP_ENABLED=true       # enable scanning
  SOURCEBRIDGE_OWASP_SCANNER_PATH=semgrep  # path to scanner binary
  SOURCEBRIDGE_OWASP_RULESET=auto       # semgrep ruleset
  SOURCEBRIDGE_OWASP_TIMEOUT=300        # per-repo timeout in seconds
  SOURCEBRIDGE_OWASP_MAX_FINDINGS=500   # cap findings per repo
"""

from __future__ import annotations

import json
import logging
import os
import shutil
import subprocess
from dataclasses import dataclass, field

logger = logging.getLogger(__name__)


@dataclass
class OWASPScanResult:
    enabled: bool = False
    scanner: str = ""
    finding_count: int = 0
    critical: int = 0
    high: int = 0
    medium: int = 0
    low: int = 0
    findings: list[dict] = field(default_factory=list)
    error: str = ""

    def to_dict(self) -> dict:
        return {
            "enabled": self.enabled,
            "scanner": self.scanner,
            "finding_count": self.finding_count,
            "critical": self.critical,
            "high": self.high,
            "medium": self.medium,
            "low": self.low,
            "findings": self.findings[:100],
            "error": self.error,
        }


def is_owasp_available() -> bool:
    """Check if OWASP scanning is enabled and the scanner is installed."""
    enabled = os.environ.get("SOURCEBRIDGE_OWASP_ENABLED", "").lower() in ("true", "1", "yes")
    if not enabled:
        return False
    scanner_path = os.environ.get("SOURCEBRIDGE_OWASP_SCANNER_PATH", "semgrep")
    return shutil.which(scanner_path) is not None


def scan_owasp(repo_path: str) -> OWASPScanResult:
    """Run OWASP scan on a repository. Returns empty result if scanner is not available."""
    if not is_owasp_available():
        return OWASPScanResult(
            enabled=False,
            error="OWASP scanning is not configured. Install Semgrep and set SOURCEBRIDGE_OWASP_ENABLED=true.",
        )

    scanner_path = os.environ.get("SOURCEBRIDGE_OWASP_SCANNER_PATH", "semgrep")
    ruleset = os.environ.get("SOURCEBRIDGE_OWASP_RULESET", "auto")
    timeout = int(os.environ.get("SOURCEBRIDGE_OWASP_TIMEOUT", "300"))
    max_findings = int(os.environ.get("SOURCEBRIDGE_OWASP_MAX_FINDINGS", "500"))

    result = OWASPScanResult(enabled=True, scanner="semgrep")

    try:
        cmd = [
            scanner_path, "scan",
            f"--config={ruleset}",
            "--json",
            "--quiet",
            repo_path,
        ]
        logger.info("owasp_scan_started", repo_path=repo_path, ruleset=ruleset)
        proc = subprocess.run(
            cmd,
            capture_output=True, text=True,
            timeout=timeout,
        )
        if proc.returncode not in (0, 1):  # 1 = findings found (expected)
            result.error = proc.stderr[:500] if proc.stderr else f"Scanner exited with code {proc.returncode}"
            return result

        data = json.loads(proc.stdout) if proc.stdout else {}
    except subprocess.TimeoutExpired:
        result.error = f"Scan timed out after {timeout}s"
        return result
    except (json.JSONDecodeError, OSError) as e:
        result.error = str(e)
        return result

    # Parse Semgrep JSON output
    results = data.get("results", [])
    for finding in results[:max_findings]:
        severity = finding.get("extra", {}).get("severity", "WARNING").upper()
        if severity in ("ERROR", "CRITICAL"):
            result.critical += 1
        elif severity in ("WARNING", "HIGH"):
            result.high += 1
        elif severity == "INFO":
            result.low += 1
        else:
            result.medium += 1

        result.findings.append({
            "rule_id": finding.get("check_id", ""),
            "severity": severity,
            "message": finding.get("extra", {}).get("message", ""),
            "file": finding.get("path", ""),
            "line_start": finding.get("start", {}).get("line", 0),
            "line_end": finding.get("end", {}).get("line", 0),
        })

    result.finding_count = len(result.findings)
    logger.info("owasp_scan_completed", findings=result.finding_count, repo_path=repo_path)
    return result
