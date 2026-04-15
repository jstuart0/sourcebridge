"""CLI entry point for code review — invoked by Go CLI."""

from __future__ import annotations

import asyncio
import contextlib
import json
import os
import sys

# Ensure workers package is importable when run from workers/ dir
_here = os.path.dirname(os.path.abspath(__file__))
_parent = os.path.dirname(_here)
if _parent not in sys.path:
    sys.path.insert(0, _parent)

from workers.common.config import WorkerConfig  # noqa: E402
from workers.common.llm.config import create_llm_provider  # noqa: E402
from workers.reasoning.cache import UsageTracker  # noqa: E402
from workers.reasoning.reviewer import review_code  # noqa: E402


async def main() -> None:
    file_path = sys.argv[1] if len(sys.argv) > 1 else ""
    template = os.environ.get("SOURCEBRIDGE_REVIEW_TEMPLATE", "security")

    if not file_path or not os.path.exists(file_path):
        print(json.dumps({"error": f"File not found: {file_path}"}))
        sys.exit(1)

    with open(file_path) as fh:
        content = fh.read()

    # Detect language from extension
    ext = os.path.splitext(file_path)[1].lower()
    lang_map = {
        ".go": "go",
        ".py": "python",
        ".ts": "typescript",
        ".js": "javascript",
        ".java": "java",
        ".rs": "rust",
        ".cs": "csharp",
        ".cpp": "cpp",
        ".rb": "ruby",
    }
    language = lang_map.get(ext, "unknown")

    config = WorkerConfig()
    provider = create_llm_provider(config)

    tracker = UsageTracker()
    with contextlib.redirect_stdout(sys.stderr):
        result, usage = await review_code(provider, file_path, language, content, template=template)
    tracker.record(usage)

    output = {
        "template": result.template,
        "findings": [
            {
                "category": f.category,
                "severity": f.severity,
                "message": f.message,
                "file_path": f.file_path,
                "start_line": f.start_line,
                "end_line": f.end_line,
                "suggestion": f.suggestion,
            }
            for f in result.findings
        ],
        "score": result.score,
        "usage": {
            "provider": usage.provider,
            "model": usage.model,
            "input_tokens": usage.input_tokens,
            "output_tokens": usage.output_tokens,
        },
    }
    print(json.dumps(output, indent=2))


if __name__ == "__main__":
    asyncio.run(main())
