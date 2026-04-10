# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""CI/CD and deployment pattern detection."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class CICDDetectionResult:
    tools: list[str] = field(default_factory=list)
    has_dockerfile: bool = False
    has_docker_compose: bool = False
    has_github_actions: bool = False
    has_gitlab_ci: bool = False
    has_jenkins: bool = False
    has_vercel: bool = False
    has_netlify: bool = False
    has_terraform: bool = False
    has_kubernetes: bool = False
    deployment_files: list[str] = field(default_factory=list)

    def to_dict(self) -> dict:
        return {
            "tools": self.tools,
            "has_dockerfile": self.has_dockerfile,
            "has_docker_compose": self.has_docker_compose,
            "has_github_actions": self.has_github_actions,
            "has_gitlab_ci": self.has_gitlab_ci,
            "has_vercel": self.has_vercel,
            "has_kubernetes": self.has_kubernetes,
            "deployment_files": self.deployment_files[:20],
        }


_DEPLOYMENT_MARKERS = {
    "Dockerfile": ("Docker", "has_dockerfile"),
    "docker-compose.yml": ("Docker Compose", "has_docker_compose"),
    "docker-compose.yaml": ("Docker Compose", "has_docker_compose"),
    ".gitlab-ci.yml": ("GitLab CI", "has_gitlab_ci"),
    "Jenkinsfile": ("Jenkins", "has_jenkins"),
    "vercel.json": ("Vercel", "has_vercel"),
    "netlify.toml": ("Netlify", "has_netlify"),
}


def detect_cicd(repo_path: str) -> CICDDetectionResult:
    """Scan a repository for CI/CD and deployment patterns."""
    result = CICDDetectionResult()
    root = Path(repo_path)

    if not root.exists():
        return result

    tools: set[str] = set()

    for dirpath, dirnames, filenames in os.walk(root):
        rel_dir = os.path.relpath(dirpath, root)
        if any(skip in rel_dir.split(os.sep) for skip in [
            "node_modules", ".git", "vendor", ".venv", "__pycache__", ".next",
        ]):
            continue

        for filename in filenames:
            rel_path = os.path.relpath(os.path.join(dirpath, filename), root)

            if filename in _DEPLOYMENT_MARKERS:
                tool, attr = _DEPLOYMENT_MARKERS[filename]
                tools.add(tool)
                setattr(result, attr, True)
                result.deployment_files.append(rel_path)

            # GitHub Actions
            if ".github/workflows" in rel_path and (filename.endswith(".yml") or filename.endswith(".yaml")):
                result.has_github_actions = True
                tools.add("GitHub Actions")
                result.deployment_files.append(rel_path)

            # Terraform
            if filename.endswith(".tf"):
                result.has_terraform = True
                tools.add("Terraform")
                result.deployment_files.append(rel_path)

            # Kubernetes
            if filename in ("kustomization.yaml", "kustomization.yml") or (
                filename.endswith(".yaml") and "k8s" in rel_path.lower()
            ):
                result.has_kubernetes = True
                tools.add("Kubernetes")
                result.deployment_files.append(rel_path)

    result.tools = sorted(tools)
    return result
