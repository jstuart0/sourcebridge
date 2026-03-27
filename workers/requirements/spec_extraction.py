"""Spec extraction pipeline: structural scanning -> LLM refinement -> deduplication."""

from __future__ import annotations

import re

import structlog

from workers.common.llm.parse import parse_json_response
from workers.common.llm.provider import LLMProvider
from workers.requirements.scanners.comment_scanner import DocCommentScanner
from workers.requirements.scanners.schema_scanner import APISchemaScanner
from workers.requirements.scanners.test_scanner import TestFileScanner
from workers.requirements.spec_models import (
    CandidateSpec,
    ExtractionResult,
    LLMUsageRecord,
    RefinedSpec,
)

log = structlog.get_logger()

# Language detection by file extension
EXTENSION_TO_LANGUAGE: dict[str, str] = {
    ".go": "go",
    ".py": "python",
    ".ts": "typescript",
    ".tsx": "typescript",
    ".js": "javascript",
    ".jsx": "javascript",
    ".java": "java",
    ".rs": "rust",
    ".cs": "csharp",
    ".rb": "ruby",
    ".php": "php",
    ".proto": "protobuf",
    ".graphql": "graphql",
    ".graphqls": "graphql",
    ".yaml": "yaml",
    ".yml": "yaml",
    ".json": "json",
}

SPEC_REFINEMENT_SYSTEM = """You are a requirements analyst. Given source code artifacts \
(tests, API schemas, or documentation comments), extract the implicit behavioral \
requirement that the code enforces.

Rules:
- Output a single clear requirement statement (1-3 sentences)
- Focus on WHAT the system must do, not HOW the code works
- Use "shall" or "must" language where appropriate
- Include specific data types, validation rules, or error conditions mentioned in the source
- If multiple sources describe the same behavior, synthesize into one requirement
- Return ONLY valid JSON"""

SPEC_REFINEMENT_USER = """Analyze the following source artifacts and extract the behavioral requirement they encode.

Source type: {source_type}
File: {source_file}
Language: {language}

{artifacts_block}

Return JSON:
{{
  "requirement_text": "The system shall/must ...",
  "confidence_rationale": "Brief explanation of why this is a clear/ambiguous requirement",
  "keywords": ["list", "of", "domain", "keywords"]
}}"""


def detect_language(path: str) -> str:
    """Detect programming language from file extension."""
    for ext, lang in EXTENSION_TO_LANGUAGE.items():
        if path.endswith(ext):
            return lang
    return "unknown"


async def extract_specs_structural(
    files: list,
    file_contents: dict[str, str],
) -> list[CandidateSpec]:
    """Run all structural scanners and return combined candidates."""
    candidates: list[CandidateSpec] = []

    test_scanner = TestFileScanner()
    schema_scanner = APISchemaScanner()
    comment_scanner = DocCommentScanner()

    for file_entry in files:
        content = file_contents.get(file_entry.path, "")
        if not content:
            continue

        lang = file_entry.language or detect_language(file_entry.path)

        if test_scanner.is_test_file(file_entry.path, lang):
            candidates.extend(
                test_scanner.extract(file_entry.path, content, lang, files)
            )
        elif schema_scanner.is_schema_file(file_entry.path, content):
            candidates.extend(
                schema_scanner.extract(file_entry.path, content)
            )

        # Doc comments are extracted from all non-test source files
        if not test_scanner.is_test_file(file_entry.path, lang):
            candidates.extend(
                comment_scanner.extract(file_entry.path, content, lang)
            )

    return candidates


def humanize_test_name(name: str) -> str:
    """Convert a test function name to a human-readable description.

    TestCreateUser_ValidInput_ReturnsUser -> "Create user valid input returns user"
    test_create_user_valid_input -> "Create user valid input"
    """
    # Strip common prefixes
    for prefix in ("Test", "test_", "test"):
        if name.startswith(prefix):
            name = name[len(prefix):]
            break

    # Split on _ and camelCase boundaries
    parts: list[str] = []
    for segment in name.split("_"):
        # Split camelCase
        sub_parts = re.sub(r"([a-z])([A-Z])", r"\1 \2", segment).split()
        parts.extend(sub_parts)

    return " ".join(p.lower() for p in parts if p).strip()


def humanize_candidate(c: CandidateSpec) -> RefinedSpec:
    """Convert a raw candidate to a refined spec without LLM."""
    if c.source == "test":
        text = humanize_test_name(c.raw_text)
    else:
        text = c.raw_text

    confidence = compute_confidence([c])

    return RefinedSpec(
        source=c.source,
        source_file=c.source_file,
        source_line=c.source_line,
        text=text,
        raw_text=c.raw_text,
        group_key=c.group_key,
        language=c.language,
        keywords=[],
        confidence=confidence,
        llm_refined=False,
    )


def compute_confidence(candidates: list[CandidateSpec]) -> str:
    """Compute confidence level for a group of candidates."""
    sources = {c.source for c in candidates}
    has_test = "test" in sources
    has_schema = "schema" in sources
    has_comment = "comment" in sources
    source_count = len(sources)

    # HIGH: Multiple independent sources agree
    if source_count >= 2 and (has_test or has_schema):
        return "high"

    # HIGH: Schema with description
    if has_schema and any(c.metadata.get("description") for c in candidates):
        return "high"

    # MEDIUM: Single authoritative source
    if has_test:
        return "medium"
    if has_schema:
        return "medium"

    # MEDIUM: Comment with behavioral keywords
    if has_comment:
        keyword_count = sum(
            len(c.metadata.get("behavioral_keywords", []))
            for c in candidates
        )
        if keyword_count >= 3:
            return "medium"

    # LOW: Comment-only with weak signals
    return "low"


def _confidence_rank(c: str) -> int:
    return {"high": 3, "medium": 2, "low": 1}.get(c, 0)


async def refine_with_llm(
    candidates: list[CandidateSpec],
    llm_provider: LLMProvider,
) -> tuple[list[RefinedSpec], LLMUsageRecord]:
    """Refine candidates using LLM and return refined specs + usage."""
    # Group candidates by group_key
    groups: dict[str, list[CandidateSpec]] = {}
    for c in candidates:
        groups.setdefault(c.group_key, []).append(c)

    refined: list[RefinedSpec] = []
    total_input = 0
    total_output = 0
    model_name = ""

    for group_key, group_candidates in groups.items():
        primary = group_candidates[0]

        # Build artifacts block
        artifacts_parts: list[str] = []
        for c in group_candidates:
            if c.source == "test":
                artifacts_parts.append(f"Test: {c.raw_text}")
                assertions = c.metadata.get("assertions", [])
                if assertions:
                    artifacts_parts.append(f"  Assertions: {', '.join(assertions)}")
            elif c.source == "schema":
                artifacts_parts.append(f"Schema: {c.raw_text}")
            else:
                artifacts_parts.append(f"Comment: {c.raw_text}")

        artifacts_block = "\n".join(artifacts_parts)
        prompt = SPEC_REFINEMENT_USER.format(
            source_type=primary.source,
            source_file=primary.source_file,
            language=primary.language,
            artifacts_block=artifacts_block,
        )

        try:
            response = await llm_provider.complete(
                prompt,
                system=SPEC_REFINEMENT_SYSTEM,
                temperature=0.1,
                max_tokens=512,
            )
            total_input += response.input_tokens
            total_output += response.output_tokens
            model_name = response.model

            data = parse_json_response(response.content)
            if data and isinstance(data, dict):
                text = data.get("requirement_text", primary.raw_text)
                keywords = data.get("keywords", [])
                if not isinstance(keywords, list):
                    keywords = []
            else:
                text = primary.raw_text
                keywords = []

        except Exception as exc:
            log.warning("llm_refinement_failed", group_key=group_key, error=str(exc))
            text = primary.raw_text
            keywords = []

        confidence = compute_confidence(group_candidates)

        source_files = list({c.source_file for c in group_candidates if c.source_file != primary.source_file})

        refined.append(
            RefinedSpec(
                source=primary.source,
                source_file=primary.source_file,
                source_line=primary.source_line,
                source_files=source_files,
                text=text,
                raw_text=primary.raw_text,
                group_key=group_key,
                language=primary.language,
                keywords=keywords,
                confidence=confidence,
                llm_refined=True,
            )
        )

    usage = LLMUsageRecord(
        model=model_name,
        input_tokens=total_input,
        output_tokens=total_output,
    )

    return refined, usage


async def deduplicate_specs(
    specs: list[RefinedSpec],
) -> list[RefinedSpec]:
    """Remove duplicate specs by exact group_key match.

    Embedding-based dedup is deferred to when an embedding provider is available.
    """
    seen: dict[str, RefinedSpec] = {}
    for spec in specs:
        if spec.group_key in seen:
            existing = seen[spec.group_key]
            if _confidence_rank(spec.confidence) > _confidence_rank(existing.confidence):
                seen[spec.group_key] = spec
        else:
            seen[spec.group_key] = spec
    return list(seen.values())


async def extract_specs_pipeline(
    files: list,
    llm_provider: LLMProvider | None = None,
) -> ExtractionResult:
    """Three-stage pipeline: structural extraction -> LLM refinement -> deduplication."""
    warnings: list[str] = []

    # Build content lookup
    file_contents = {f.path: f.content for f in files}

    # Stage 1: Structural extraction
    candidates = await extract_specs_structural(files, file_contents)
    total_candidates = len(candidates)
    log.info("structural_extraction_complete", candidates=total_candidates)

    if total_candidates == 0:
        return ExtractionResult(
            specs=[], total_candidates=0, usage=None,
            warnings=["No specs found in source files"],
        )

    # Stage 2: LLM refinement (optional)
    usage = None
    if llm_provider is not None:
        refined, usage = await refine_with_llm(candidates, llm_provider)
    else:
        refined = [humanize_candidate(c) for c in candidates]
        warnings.append("LLM not configured; specs are unrefined structural extractions")

    # Stage 3: Deduplication
    deduped = await deduplicate_specs(refined)
    log.info("deduplication_complete", before=len(refined), after=len(deduped))

    return ExtractionResult(
        specs=deduped,
        total_candidates=total_candidates,
        usage=usage,
        warnings=warnings,
    )
