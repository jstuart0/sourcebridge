# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Dependency license scanning."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class LicenseScanResult:
    total_dependencies: int = 0
    licenses: dict[str, int] = field(default_factory=dict)  # license -> count
    copyleft: list[str] = field(default_factory=list)  # packages with GPL/AGPL
    unknown: list[str] = field(default_factory=list)  # packages with no license

    def to_dict(self) -> dict:
        return {
            "total_dependencies": self.total_dependencies,
            "licenses": self.licenses,
            "copyleft": self.copyleft[:20],
            "unknown": self.unknown[:20],
        }


_COPYLEFT_LICENSES = {"GPL", "AGPL", "LGPL", "MPL", "EUPL", "CPAL", "OSL", "SSPL"}


def scan_licenses(repo_path: str) -> LicenseScanResult:
    """Scan a repository's dependencies for license information."""
    result = LicenseScanResult()
    root = Path(repo_path)

    if not root.exists():
        return result

    # Check package.json for Node dependencies
    pkg_json = root / "package.json"
    if pkg_json.exists():
        try:
            pkg = json.loads(pkg_json.read_text(errors="ignore"))
            deps = {**pkg.get("dependencies", {}), **pkg.get("devDependencies", {})}
            result.total_dependencies = len(deps)

            # Check node_modules for license info
            nm = root / "node_modules"
            if nm.exists():
                for dep_name in deps:
                    dep_dir = nm / dep_name
                    dep_pkg = dep_dir / "package.json"
                    if dep_pkg.exists():
                        try:
                            dp = json.loads(dep_pkg.read_text(errors="ignore"))
                            lic = dp.get("license", "")
                            if isinstance(lic, dict):
                                lic = lic.get("type", "")
                            if lic:
                                result.licenses[lic] = result.licenses.get(lic, 0) + 1
                                for copyleft in _COPYLEFT_LICENSES:
                                    if copyleft in lic.upper():
                                        result.copyleft.append(f"{dep_name} ({lic})")
                                        break
                            else:
                                result.unknown.append(dep_name)
                        except (OSError, json.JSONDecodeError):
                            result.unknown.append(dep_name)
        except (OSError, json.JSONDecodeError):
            pass

    # Check requirements.txt for Python dependencies (license info not embedded — would need pip show)
    reqs = root / "requirements.txt"
    if reqs.exists():
        try:
            lines = reqs.read_text(errors="ignore").splitlines()
            py_deps = [l.strip().split("==")[0].split(">=")[0].split("<")[0].strip()
                       for l in lines if l.strip() and not l.startswith("#") and not l.startswith("-")]
            result.total_dependencies += len(py_deps)
        except OSError:
            pass

    return result
