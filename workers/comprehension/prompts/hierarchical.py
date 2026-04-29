# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""Prompt templates for the hierarchical summarization strategy.

These prompts follow a few shared principles:

1. They are deliberately short. The leaf prompt runs once per
   segment on potentially thousands of chunks, so every token in the
   prompt multiplies cost and latency.

2. They target any reasonable instruction-following model including
   small local Ollama variants. No JSON mode required at the leaf
   level — free-text summaries are easier for weak models to produce
   reliably and the structure comes from the final renderer prompt.

3. Every level's output is itself an input to the next level's
   prompt, so the output shape has to stay stable and parseable:
   a headline line, a blank, then a 2-5 sentence body.

The final render prompt (in the renderers module) is responsible for
producing the structured sections the cliff notes UI expects.
"""

from __future__ import annotations

HIERARCHICAL_SYSTEM = (
    "You are an expert software engineer writing compact, accurate summaries "
    "of source code. You write exactly what you're asked, nothing more. "
    "You must answer directly. Do not use internal reasoning, scratchpad, or <think> tags. "
    "Output ONLY the requested content."
)


LEAF_SUMMARY_TEMPLATE = """\
Summarize this code segment for a developer who is reading the rest of the repository.

Constraints:
- Output format: first line is a headline (≤100 chars), then a blank line, then a body of 2-3 short sentences.
- The headline should name what this code does, not what it's called.
- Focus on the primary responsibility first.
- Mention external dependencies, side effects, or pitfalls only if they materially matter.
- Keep the body under 120 words.
- Do NOT copy the code into the summary.
- Do NOT speculate beyond what the code shows.

Context:
- Repository: {repository_name}
- File: {file_path}
- Segment: {segment_label}
- Language: {language}

Code:
```
{code}
```

Summary:
"""


# File summaries roll up leaf summaries into a per-file narrative.
# We pass the leaf summaries (not the code) so the prompt stays small.
FILE_SUMMARY_TEMPLATE = """\
Summarize this file based on the summaries of its segments below.

Constraints:
- Output format: first line is a headline (≤100 chars), then a blank line, then a body of 3-4 short sentences.
- Explain what role this file plays in the repository, not a line-by-line walkthrough.
- Treat the structured file facts as authoritative scaffolding for the file's role, shape, and signals.
- Cross-reference segments when one depends on another.
- Flag anything unusual (complex logic, heavy side effects, test/mock status).
- Keep the body under 160 words.

Context:
- Repository: {repository_name}
- File: {file_path}
- Language: {language}
- Segment count: {segment_count}

Structured file facts:
{file_facts}

Segment summaries:
{segment_summaries}

File summary:
"""


# Package/module summaries roll up file summaries into a per-package narrative.
PACKAGE_SUMMARY_TEMPLATE = """\
Summarize this package/module based on the summaries of its files below.

Constraints:
- Output format: first line is a headline (≤100 chars), then a blank line, then a body of 4-5 short sentences.
- Explain what this package is for, how its files cooperate, and what depends on it.
- Use the structured package facts to identify the package's dominant execution role before you generalize from file summaries.
- Flag cross-package collaboration you can infer from the file summaries.
- Keep the body under 220 words.

Context:
- Repository: {repository_name}
- Package: {package_label}
- File count: {file_count}

Structured package facts:
{package_facts}

File summaries:
{file_summaries}

Package summary:
"""


# Root summary produces the repository-level one-page narrative that
# renderers consume as the top-level context for the final artifact
# prompt.
ROOT_SUMMARY_TEMPLATE = """\
Summarize this entire repository based on the package summaries below.

Constraints:
- Output format: headline (≤120 chars), blank line, then 6-8 short sentences.
- Cover: what the repo is, who it serves, the main building blocks, the
  primary execution flows, and any notable dependencies/infrastructure.
- Start from the structured repository facts so the summary reflects the indexed system shape before stylistic synthesis.
- Do NOT invent details that aren't in the package summaries.
- Do NOT include marketing language.
- Keep the body under 320 words.

Context:
- Repository: {repository_name}
- Package count: {package_count}
- Total files: {file_count}
- Total segments: {segment_count}

Structured repository facts:
{root_facts}

Package summaries:
{package_summaries}

Repository summary:
"""


def build_leaf_prompt(
    *,
    repository_name: str,
    file_path: str,
    segment_label: str,
    language: str,
    code: str,
) -> str:
    """Build the leaf-level summarization prompt for one code segment."""
    return LEAF_SUMMARY_TEMPLATE.format(
        repository_name=repository_name or "unknown",
        file_path=file_path or "unknown",
        segment_label=segment_label or "segment",
        language=language or "unknown",
        code=code,
    )


def build_file_prompt(
    *,
    repository_name: str,
    file_path: str,
    language: str,
    segment_summaries: list[str],
    file_facts: list[str],
) -> str:
    """Build a file-level summary prompt from child segment summaries."""
    formatted = "\n\n".join(f"- {s.strip()}" for s in segment_summaries if s.strip())
    facts = "\n".join(f"- {fact.strip()}" for fact in file_facts if fact.strip())
    return FILE_SUMMARY_TEMPLATE.format(
        repository_name=repository_name or "unknown",
        file_path=file_path or "unknown",
        language=language or "unknown",
        segment_count=len(segment_summaries),
        file_facts=facts or "(no structured file facts)",
        segment_summaries=formatted or "(no segment summaries)",
    )


def build_package_prompt(
    *,
    repository_name: str,
    package_label: str,
    file_summaries: list[str],
    package_facts: list[str],
) -> str:
    """Build a package-level summary prompt from child file summaries."""
    formatted = "\n\n".join(f"- {s.strip()}" for s in file_summaries if s.strip())
    facts = "\n".join(f"- {fact.strip()}" for fact in package_facts if fact.strip())
    return PACKAGE_SUMMARY_TEMPLATE.format(
        repository_name=repository_name or "unknown",
        package_label=package_label or "package",
        file_count=len(file_summaries),
        package_facts=facts or "(no structured package facts)",
        file_summaries=formatted or "(no file summaries)",
    )


def build_root_prompt(
    *,
    repository_name: str,
    package_summaries: list[str],
    file_count: int,
    segment_count: int,
    root_facts: list[str],
) -> str:
    """Build the repo-level summary prompt from child package summaries."""
    formatted = "\n\n".join(f"- {s.strip()}" for s in package_summaries if s.strip())
    facts = "\n".join(f"- {fact.strip()}" for fact in root_facts if fact.strip())
    return ROOT_SUMMARY_TEMPLATE.format(
        repository_name=repository_name or "unknown",
        package_count=len(package_summaries),
        file_count=file_count,
        segment_count=segment_count,
        root_facts=facts or "(no structured repository facts)",
        package_summaries=formatted or "(no package summaries)",
    )
