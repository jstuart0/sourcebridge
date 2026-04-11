#!/usr/bin/env python3
"""Generate and evaluate a live enterprise report run.

This harness is intentionally self-contained and uses only the Python standard
library so it can run in OSS-safe environments without extra dependencies.
It does four things:

1. Mints a short-lived JWT for the live GraphQL/report APIs.
2. Looks up the target repository by name.
3. Creates a report, waits for completion, and downloads markdown/evidence.
4. Writes a timestamped run directory plus a rolling index for comparison.
"""

from __future__ import annotations

import argparse
import base64
import datetime as dt
import hashlib
import hmac
import json
import os
import re
import subprocess
import sys
import time
import urllib.parse
from pathlib import Path
from typing import Any


def _b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def mint_hs256_jwt(secret: str, issuer: str, user_id: str, email: str, role: str, org_id: str = "") -> str:
    now = int(time.time())
    header = {"alg": "HS256", "typ": "JWT"}
    payload = {
        "iss": issuer,
        "sub": user_id,
        "exp": now + 24 * 60 * 60,
        "nbf": now,
        "iat": now,
        "uid": user_id,
        "email": email,
        "role": role,
    }
    if org_id:
        payload["org"] = org_id
    signing_input = f"{_b64url(json.dumps(header, separators=(',', ':')).encode())}.{_b64url(json.dumps(payload, separators=(',', ':')).encode())}"
    signature = hmac.new(secret.encode("utf-8"), signing_input.encode("ascii"), hashlib.sha256).digest()
    return f"{signing_input}.{_b64url(signature)}"


def infer_issuer(base_url: str) -> str:
    host = urllib.parse.urlparse(base_url).netloc
    if "enterprise" in host:
        return "sourcebridge-enterprise"
    return "sourcebridge"


def http_json(
    method: str,
    url: str,
    *,
    token: str | None = None,
    payload: dict[str, Any] | None = None,
    timeout: int = 60,
) -> Any:
    cmd = [
        "curl",
        "-sS",
        "-X",
        method,
        url,
        "-H",
        "Content-Type: application/json",
        "-H",
        "Accept: application/json",
        "-H",
        "User-Agent: sourcebridge-report-quality-harness/1.0",
        "--max-time",
        str(timeout),
    ]
    if token:
        cmd.extend(["-H", f"Authorization: Bearer {token}"])
    if payload is not None:
        cmd.extend(["--data", json.dumps(payload, separators=(",", ":"))])
    raw = subprocess.check_output(cmd)
    if not raw:
        return None
    return json.loads(raw.decode("utf-8"))


def graphql(base_url: str, token: str, query: str, variables: dict[str, Any] | None = None) -> Any:
    body = {"query": query}
    if variables:
        body["variables"] = variables
    resp = http_json("POST", f"{base_url.rstrip('/')}/api/v1/graphql", token=token, payload=body)
    if resp.get("errors"):
        raise RuntimeError(f"GraphQL error: {resp['errors']}")
    return resp["data"]


def slugify(value: str) -> str:
    return re.sub(r"[^a-z0-9]+", "-", value.lower()).strip("-")


def evaluate_markdown(markdown: str, evidence: list[dict[str, Any]]) -> dict[str, Any]:
    lines = [line.rstrip() for line in markdown.splitlines()]
    nonempty_lines = [line for line in lines if line.strip()]
    headings = [line.strip() for line in lines if re.match(r"^#{1,6}\s+", line.strip())]
    counts: dict[str, int] = {}
    for line in nonempty_lines:
        counts[line] = counts.get(line, 0) + 1
    repeated_lines = {line: count for line, count in counts.items() if count >= 3 and len(line) >= 16}

    placeholder_count = markdown.count("Insufficient data available.")
    deep_analysis_prompt_count = markdown.count("Action: Run deep analysis")
    fake_evidence_marker_count = len(re.findall(r"\[E-[A-Z]+-\d+\]", markdown))
    dollar_amount_count = len(re.findall(r"\$\d[\d,]*(?:\.\d+)?", markdown))
    removed_repeat_block_markers = len(re.findall(r"\(Removed \d+ repeated content blocks\)", markdown))

    duplicate_headings = {}
    for heading in headings:
        duplicate_headings[heading] = duplicate_headings.get(heading, 0) + 1
    duplicate_headings = {heading: count for heading, count in duplicate_headings.items() if count > 1}

    quality_score = 100
    quality_score -= placeholder_count * 3
    quality_score -= deep_analysis_prompt_count * 2
    quality_score -= fake_evidence_marker_count * 5
    quality_score -= dollar_amount_count * 4
    quality_score -= removed_repeat_block_markers * 10
    quality_score -= min(len(repeated_lines) * 4, 24)
    quality_score -= min(sum(count - 1 for count in duplicate_headings.values()) * 5, 25)
    quality_score += min(len(evidence), 10)
    quality_score = max(0, min(100, quality_score))

    top_repeated = [
        {"line": line, "count": count}
        for line, count in sorted(repeated_lines.items(), key=lambda item: (-item[1], item[0]))[:10]
    ]

    return {
        "word_count": len(markdown.split()),
        "line_count": len(lines),
        "heading_count": len(headings),
        "duplicate_heading_count": len(duplicate_headings),
        "duplicate_headings": duplicate_headings,
        "placeholder_count": placeholder_count,
        "deep_analysis_prompt_count": deep_analysis_prompt_count,
        "fake_evidence_marker_count": fake_evidence_marker_count,
        "dollar_amount_count": dollar_amount_count,
        "removed_repeat_block_markers": removed_repeat_block_markers,
        "repeated_line_count": len(repeated_lines),
        "top_repeated_lines": top_repeated,
        "evidence_count": len(evidence),
        "quality_score": quality_score,
    }


def write_json(path: Path, payload: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def write_text(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def append_index(index_path: Path, record: dict[str, Any]) -> None:
    existing: list[dict[str, Any]]
    if index_path.exists():
        existing = json.loads(index_path.read_text(encoding="utf-8"))
    else:
        existing = []
    existing.append(record)
    write_json(index_path, existing)


def poll_report(base_url: str, report_id: str, token: str | None, timeout_seconds: int, poll_interval_seconds: int) -> dict[str, Any]:
    deadline = time.time() + timeout_seconds
    last = None
    while time.time() < deadline:
        last = http_json("GET", f"{base_url.rstrip('/')}/api/v1/reports/{report_id}", token=token)
        status = str(last.get("status", "")).lower()
        if status in {"ready", "failed"}:
            return last
        time.sleep(poll_interval_seconds)
    raise TimeoutError(f"report {report_id} did not complete within {timeout_seconds}s; last state={last}")


def main() -> int:
    parser = argparse.ArgumentParser(description="Create and evaluate a live SourceBridge enterprise report.")
    parser.add_argument("--base-url", default="https://sourcebridge-enterprise.xmojo.net")
    parser.add_argument("--repo-name", required=True)
    parser.add_argument("--audience", default="c_suite")
    parser.add_argument("--report-type", default="architecture_baseline")
    parser.add_argument("--analysis-depth", default="deep")
    parser.add_argument("--results-dir", default="benchmarks/results/report-quality-live")
    parser.add_argument("--jwt-secret", default=os.environ.get("SOURCEBRIDGE_SECURITY_JWT_SECRET", ""))
    parser.add_argument("--issuer", default="")
    parser.add_argument("--user-id", default="codex")
    parser.add_argument("--email", default="codex@example.com")
    parser.add_argument("--role", default="admin")
    parser.add_argument("--org-id", default="")
    parser.add_argument("--timeout-seconds", type=int, default=900)
    parser.add_argument("--poll-interval-seconds", type=int, default=5)
    parser.add_argument("--include-loe", action="store_true")
    parser.add_argument("--include-diagrams", action="store_true")
    parser.add_argument("--include-recommendations", action="store_true")
    parser.add_argument("--style-id", default="")
    args = parser.parse_args()

    if not args.jwt_secret:
        print("missing --jwt-secret or SOURCEBRIDGE_SECURITY_JWT_SECRET", file=sys.stderr)
        return 2

    issuer = args.issuer or infer_issuer(args.base_url)
    token = mint_hs256_jwt(args.jwt_secret, issuer, args.user_id, args.email, args.role, args.org_id)

    repos_data = graphql(
        args.base_url,
        token,
        "query RepositoriesLight { repositories { id name status fileCount } }",
    )
    repos = repos_data["repositories"]
    target_repo = next((repo for repo in repos if repo["name"] == args.repo_name), None)
    if target_repo is None:
        raise RuntimeError(f"repository {args.repo_name!r} not found; available={', '.join(repo['name'] for repo in repos)}")

    section_data = http_json(
        "GET",
        f"{args.base_url.rstrip('/')}/api/v1/reports/sections?report_type={urllib.parse.quote(args.report_type)}&audience={urllib.parse.quote(args.audience)}",
        token=token,
    )
    selected_sections = section_data["defaultSections"]
    started_at = dt.datetime.now(dt.timezone.utc)
    run_name = started_at.strftime("%Y%m%dT%H%M%SZ")
    run_slug = f"{run_name}-{slugify(args.repo_name)}-{args.analysis_depth}"
    run_dir = Path(args.results_dir) / run_slug
    run_dir.mkdir(parents=True, exist_ok=True)

    create_payload = {
        "name": f"Automated {args.report_type.replace('_', ' ').title()} {started_at.strftime('%Y-%m-%d %H:%M:%S UTC')}",
        "reportType": args.report_type,
        "audience": args.audience,
        "repositoryIds": [target_repo["id"]],
        "selectedSections": selected_sections,
        "includeDiagrams": args.include_diagrams,
        "outputFormats": ["markdown"],
        "loeMode": "human_hours",
        "analysisDepth": args.analysis_depth,
        "includeRecommendations": args.include_recommendations,
        "includeLoe": args.include_loe,
    }
    if args.style_id:
        create_payload["styleId"] = args.style_id

    created = http_json("POST", f"{args.base_url.rstrip('/')}/api/v1/reports", token=token, payload=create_payload)
    report_id = created["id"]
    final_report = poll_report(
        args.base_url,
        report_id,
        token,
        timeout_seconds=args.timeout_seconds,
        poll_interval_seconds=args.poll_interval_seconds,
    )

    markdown_payload = http_json("GET", f"{args.base_url.rstrip('/')}/api/v1/reports/{report_id}/markdown", token=token)
    evidence_payload = http_json("GET", f"{args.base_url.rstrip('/')}/api/v1/reports/{report_id}/evidence", token=token) or []
    report_list = http_json("GET", f"{args.base_url.rstrip('/')}/api/v1/reports", token=token) or []
    list_contains_report = any(item.get("id") == report_id for item in report_list)

    markdown = markdown_payload.get("markdown", "")
    metrics = evaluate_markdown(markdown, evidence_payload)
    metrics["report_id"] = report_id
    metrics["report_status"] = final_report.get("status")
    metrics["report_progress"] = final_report.get("progress")
    metrics["report_error_code"] = final_report.get("errorCode")
    metrics["report_error_message"] = final_report.get("errorMessage")
    metrics["report_list_contains_report"] = list_contains_report
    metrics["selected_section_count"] = len(selected_sections)
    metrics["repo_id"] = target_repo["id"]
    metrics["repo_name"] = target_repo["name"]
    metrics["analysis_depth"] = args.analysis_depth
    metrics["base_url"] = args.base_url
    metrics["started_at"] = started_at.isoformat()
    metrics["completed_at"] = dt.datetime.now(dt.timezone.utc).isoformat()

    write_json(run_dir / "create_request.json", create_payload)
    write_json(run_dir / "created_report.json", created)
    write_json(run_dir / "final_report.json", final_report)
    write_json(run_dir / "evidence.json", evidence_payload)
    write_json(run_dir / "metrics.json", metrics)
    write_text(run_dir / "report.md", markdown)

    summary = "\n".join(
        [
            f"# Live Report Evaluation: {run_slug}",
            "",
            f"- Report ID: `{report_id}`",
            f"- Repository: `{target_repo['name']}` (`{target_repo['id']}`)",
            f"- Status: `{final_report.get('status')}`",
            f"- Evidence items: `{metrics['evidence_count']}`",
            f"- Quality score: `{metrics['quality_score']}`",
            f"- Placeholders: `{metrics['placeholder_count']}`",
            f"- Deep-analysis prompts: `{metrics['deep_analysis_prompt_count']}`",
            f"- Fake evidence markers: `{metrics['fake_evidence_marker_count']}`",
            f"- Dollar amounts: `{metrics['dollar_amount_count']}`",
            f"- Duplicate headings: `{metrics['duplicate_heading_count']}`",
            f"- Report list contains report: `{list_contains_report}`",
            "",
            "## Top repeated lines",
            "",
        ]
    )
    repeated = metrics["top_repeated_lines"]
    if repeated:
        summary += "\n".join(f"- `{item['count']}x` {item['line']}" for item in repeated)
    else:
        summary += "- none"
    summary += "\n"
    write_text(run_dir / "summary.md", summary)

    append_index(
        Path(args.results_dir) / "index.json",
        {
            "run": run_slug,
            "report_id": report_id,
            "repo_id": target_repo["id"],
            "repo_name": target_repo["name"],
            "analysis_depth": args.analysis_depth,
            "quality_score": metrics["quality_score"],
            "evidence_count": metrics["evidence_count"],
            "placeholder_count": metrics["placeholder_count"],
            "deep_analysis_prompt_count": metrics["deep_analysis_prompt_count"],
            "fake_evidence_marker_count": metrics["fake_evidence_marker_count"],
            "duplicate_heading_count": metrics["duplicate_heading_count"],
            "report_list_contains_report": list_contains_report,
            "status": final_report.get("status"),
            "started_at": metrics["started_at"],
            "completed_at": metrics["completed_at"],
            "run_dir": str(run_dir),
        },
    )

    print(json.dumps({"run_dir": str(run_dir), "report_id": report_id, "metrics": metrics}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
