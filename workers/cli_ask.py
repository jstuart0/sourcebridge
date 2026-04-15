"""CLI entry point for code discussion — invoked by Go CLI."""

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
from workers.reasoning.discussion import discuss_code  # noqa: E402


async def main() -> None:
    question = sys.argv[1] if len(sys.argv) > 1 else ""
    repo_path = os.environ.get("SOURCEBRIDGE_REPO_PATH", ".")

    if not question:
        print(json.dumps({"error": "No question provided"}))
        sys.exit(1)

    # Gather code context from the repo
    context_parts = []
    for root, _dirs, files in os.walk(repo_path):
        for f in files:
            ext = os.path.splitext(f)[1].lower()
            if ext in (".go", ".py", ".ts", ".js", ".java", ".rs"):
                fpath = os.path.join(root, f)
                try:
                    with open(fpath) as fh:
                        content = fh.read()
                    if len(content) < 10000:  # Skip very large files
                        context_parts.append(f"--- {fpath} ---\n{content}")
                except Exception:
                    continue
            if len(context_parts) >= 20:  # Limit context files
                break

    context_code = "\n\n".join(context_parts) if context_parts else "No code files found."

    config = WorkerConfig()
    provider = create_llm_provider(config)
    with contextlib.redirect_stdout(sys.stderr):
        answer, usage = await discuss_code(provider, question, context_code)

    output = {
        "answer": answer.answer,
        "references": answer.references,
        "related_requirements": answer.related_requirements,
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
