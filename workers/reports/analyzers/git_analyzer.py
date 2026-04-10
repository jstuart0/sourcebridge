# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Git history and workflow analysis."""

from __future__ import annotations

import os
import subprocess
from dataclasses import dataclass, field
from pathlib import Path


@dataclass
class GitAnalysisResult:
    contributor_count: int = 0
    contributors: list[dict[str, str]] = field(default_factory=list)
    total_commits: int = 0
    uses_pull_requests: bool = False
    uses_branches: bool = False
    branch_count: int = 0
    has_codeowners: bool = False
    has_protected_branches: bool = False
    recent_commit_count: int = 0  # last 30 days
    commit_frequency: str = ""  # "daily", "weekly", "monthly", "sporadic"

    def to_dict(self) -> dict:
        return {
            "contributor_count": self.contributor_count,
            "contributors": self.contributors[:20],
            "total_commits": self.total_commits,
            "uses_pull_requests": self.uses_pull_requests,
            "uses_branches": self.uses_branches,
            "branch_count": self.branch_count,
            "has_codeowners": self.has_codeowners,
            "commit_frequency": self.commit_frequency,
        }


def analyze_git(repo_path: str) -> GitAnalysisResult:
    """Analyze a repository's git history and workflow."""
    result = GitAnalysisResult()
    root = Path(repo_path)

    if not (root / ".git").exists():
        return result

    def _run(cmd: list[str]) -> str:
        try:
            out = subprocess.run(
                cmd, cwd=str(root), capture_output=True, text=True, timeout=30
            )
            return out.stdout.strip()
        except (subprocess.TimeoutExpired, OSError):
            return ""

    # Contributors
    log = _run(["git", "shortlog", "-sne", "--all"])
    if log:
        for line in log.splitlines():
            parts = line.strip().split("\t", 1)
            if len(parts) == 2:
                count = parts[0].strip()
                name_email = parts[1].strip()
                result.contributors.append({"name": name_email, "commits": count})
        result.contributor_count = len(result.contributors)

    # Total commits
    count_str = _run(["git", "rev-list", "--count", "HEAD"])
    if count_str.isdigit():
        result.total_commits = int(count_str)

    # Branches
    branches = _run(["git", "branch", "-a"])
    if branches:
        branch_lines = [b.strip() for b in branches.splitlines() if b.strip()]
        result.branch_count = len(branch_lines)
        result.uses_branches = result.branch_count > 1

    # PR detection (look for merge commits with PR-style messages)
    merge_log = _run(["git", "log", "--oneline", "--merges", "-20"])
    if merge_log:
        pr_indicators = ["Merge pull request", "Merge branch", "PR #", "pull request"]
        for line in merge_log.splitlines():
            if any(ind in line for ind in pr_indicators):
                result.uses_pull_requests = True
                break

    # CODEOWNERS
    result.has_codeowners = (
        (root / "CODEOWNERS").exists() or
        (root / ".github" / "CODEOWNERS").exists() or
        (root / "docs" / "CODEOWNERS").exists()
    )

    # Recent commits (last 30 days)
    recent = _run(["git", "log", "--oneline", "--since=30 days ago"])
    if recent:
        result.recent_commit_count = len(recent.splitlines())

    # Commit frequency
    if result.total_commits == 0:
        result.commit_frequency = "none"
    elif result.recent_commit_count >= 20:
        result.commit_frequency = "daily"
    elif result.recent_commit_count >= 5:
        result.commit_frequency = "weekly"
    elif result.recent_commit_count >= 1:
        result.commit_frequency = "monthly"
    else:
        result.commit_frequency = "sporadic"

    return result
