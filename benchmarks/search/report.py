#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 SourceBridge Contributors

"""
Compute quality + latency deltas between a baseline and candidate
benchmark run.

Inputs:
  - baseline.jsonl — runner output for the legacy impl
  - candidate.jsonl — runner output for the hybrid impl
  - labels.yaml — adjudicated relevance labels

Metrics reported:
  MRR@10, NDCG@10, Recall@10, Top1 exact-hit rate, Top3 useful-hit
  rate (label>=2), latency p50/p95/p99.

Slicing:
  overall, by query_class, by requirement_link_cohort.

The script refuses to summarize from incomplete input — if either
jsonl file is missing records for queries present in labels.yaml, it
surfaces the missing IDs and exits non-zero.

Usage:
  python benchmarks/search/report.py \
    --baseline benchmarks/search/reports/RUN/baseline.jsonl \
    --candidate benchmarks/search/reports/RUN/candidate.jsonl \
    --labels benchmarks/search/labels.yaml \
    --output benchmarks/search/reports/RUN/report.md
"""
from __future__ import annotations

import argparse
import json
import math
import statistics
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any

import yaml


def load_jsonl(path: Path) -> list[dict[str, Any]]:
    out = []
    with path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            out.append(json.loads(line))
    return out


def load_labels(path: Path) -> dict[str, dict[tuple[str, str], int]]:
    """Return {query_id: {(entity_type, entity_id): relevance}}."""
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    labels: dict[str, dict[tuple[str, str], int]] = {}
    for row in data.get("labels", []):
        qid = row["query_id"]
        targets = {}
        for t in row.get("targets", []):
            targets[(t["entity_type"], t["entity_id"])] = int(t["relevance"])
        labels[qid] = targets
    return labels


def rr(results: list[dict[str, Any]], targets: dict[tuple[str, str], int], k: int = 10) -> float:
    for i, r in enumerate(results[:k]):
        key = (r.get("entity_type"), r.get("entity_id"))
        if targets.get(key, 0) >= 2:
            return 1.0 / (i + 1)
    return 0.0


def ndcg(results: list[dict[str, Any]], targets: dict[tuple[str, str], int], k: int = 10) -> float:
    gains = []
    for r in results[:k]:
        key = (r.get("entity_type"), r.get("entity_id"))
        gains.append(targets.get(key, 0))
    dcg = sum((g / math.log2(i + 2)) for i, g in enumerate(gains))
    ideal = sorted(targets.values(), reverse=True)[:k]
    idcg = sum((g / math.log2(i + 2)) for i, g in enumerate(ideal))
    return (dcg / idcg) if idcg > 0 else 0.0


def recall(results: list[dict[str, Any]], targets: dict[tuple[str, str], int], k: int = 10) -> float:
    relevant = {key for key, rel in targets.items() if rel >= 2}
    if not relevant:
        return 0.0
    hits = 0
    for r in results[:k]:
        key = (r.get("entity_type"), r.get("entity_id"))
        if key in relevant:
            hits += 1
    return hits / len(relevant)


def top1_exact(results: list[dict[str, Any]], targets: dict[tuple[str, str], int]) -> int:
    if not results:
        return 0
    first = results[0]
    key = (first.get("entity_type"), first.get("entity_id"))
    return 1 if targets.get(key, 0) == 3 else 0


def top3_useful(results: list[dict[str, Any]], targets: dict[tuple[str, str], int]) -> int:
    for r in results[:3]:
        key = (r.get("entity_type"), r.get("entity_id"))
        if targets.get(key, 0) >= 2:
            return 1
    return 0


def percentile(values: list[float], q: float) -> float:
    if not values:
        return 0.0
    s = sorted(values)
    idx = min(len(s) - 1, int(len(s) * q))
    return s[idx]


def summarize(records: list[dict[str, Any]], labels: dict[str, dict[tuple[str, str], int]]) -> dict[str, Any]:
    mrrs: list[float] = []
    ndcgs: list[float] = []
    recalls: list[float] = []
    top1s: list[int] = []
    top3s: list[int] = []
    latencies: list[float] = []
    by_class: dict[str, list[float]] = defaultdict(list)
    by_cohort: dict[str, list[float]] = defaultdict(list)
    missing = []
    for rec in records:
        qid = rec["query_id"]
        targets = labels.get(qid)
        if targets is None:
            missing.append(qid)
            continue
        results = rec.get("results") or []
        m = rr(results, targets)
        mrrs.append(m)
        ndcgs.append(ndcg(results, targets))
        recalls.append(recall(results, targets))
        top1s.append(top1_exact(results, targets))
        top3s.append(top3_useful(results, targets))
        if rec.get("latency_ms"):
            latencies.append(float(rec["latency_ms"]))
        by_class[rec.get("query_class", "unknown")].append(m)
        by_cohort[rec.get("requirement_link_cohort", "unknown")].append(m)
    out = {
        "n": len(records),
        "n_labeled": len(mrrs),
        "mrr10": statistics.mean(mrrs) if mrrs else 0.0,
        "ndcg10": statistics.mean(ndcgs) if ndcgs else 0.0,
        "recall10": statistics.mean(recalls) if recalls else 0.0,
        "top1_exact_rate": statistics.mean(top1s) if top1s else 0.0,
        "top3_useful_rate": statistics.mean(top3s) if top3s else 0.0,
        "latency_p50": percentile(latencies, 0.50),
        "latency_p95": percentile(latencies, 0.95),
        "latency_p99": percentile(latencies, 0.99),
        "by_class": {k: statistics.mean(v) for k, v in by_class.items()},
        "by_cohort": {k: statistics.mean(v) for k, v in by_cohort.items()},
        "missing_labels": missing,
    }
    return out


def render_markdown(baseline: dict[str, Any], candidate: dict[str, Any], base_path: Path, cand_path: Path) -> str:
    def delta(a: float, b: float) -> str:
        if a == 0:
            return f"{b:.3f} (—)"
        pct = (b - a) / a * 100.0
        return f"{b:.3f} ({pct:+.1f}%)"

    lines = []
    lines.append("# Hybrid Retrieval Benchmark Report\n")
    lines.append(f"Baseline: `{base_path}`  ")
    lines.append(f"Candidate: `{cand_path}`  \n")
    lines.append("## Topline\n")
    lines.append("| Metric | Baseline | Candidate | Delta |")
    lines.append("|--------|----------|-----------|-------|")
    for k, lbl in [
        ("mrr10", "MRR@10"),
        ("ndcg10", "NDCG@10"),
        ("recall10", "Recall@10"),
        ("top1_exact_rate", "Top1 exact-hit"),
        ("top3_useful_rate", "Top3 useful-hit"),
    ]:
        lines.append(f"| {lbl} | {baseline[k]:.3f} | {candidate[k]:.3f} | {delta(baseline[k], candidate[k])} |")
    lines.append("")
    lines.append("## Latency\n")
    lines.append("| Percentile | Baseline ms | Candidate ms | Delta |")
    lines.append("|------------|-------------|--------------|-------|")
    for k, lbl in [("latency_p50", "p50"), ("latency_p95", "p95"), ("latency_p99", "p99")]:
        lines.append(f"| {lbl} | {baseline[k]:.1f} | {candidate[k]:.1f} | {delta(baseline[k], candidate[k])} |")
    lines.append("")
    lines.append("## MRR@10 by query class\n")
    lines.append("| Class | Baseline | Candidate |")
    lines.append("|-------|----------|-----------|")
    classes = sorted(set(baseline["by_class"]) | set(candidate["by_class"]))
    for c in classes:
        b = baseline["by_class"].get(c, 0.0)
        n = candidate["by_class"].get(c, 0.0)
        lines.append(f"| {c} | {b:.3f} | {n:.3f} |")
    lines.append("")
    lines.append("## MRR@10 by requirement-link cohort\n")
    lines.append("| Cohort | Baseline | Candidate |")
    lines.append("|--------|----------|-----------|")
    cohorts = sorted(set(baseline["by_cohort"]) | set(candidate["by_cohort"]))
    for c in cohorts:
        b = baseline["by_cohort"].get(c, 0.0)
        n = candidate["by_cohort"].get(c, 0.0)
        lines.append(f"| {c} | {b:.3f} | {n:.3f} |")
    lines.append("")
    if baseline.get("missing_labels") or candidate.get("missing_labels"):
        lines.append("## Missing labels\n")
        lines.append(f"Baseline: {baseline['missing_labels']}  ")
        lines.append(f"Candidate: {candidate['missing_labels']}  \n")
    return "\n".join(lines) + "\n"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--baseline", required=True, type=Path)
    ap.add_argument("--candidate", required=True, type=Path)
    ap.add_argument("--labels", required=True, type=Path)
    ap.add_argument("--output", type=Path, help="write markdown report here")
    args = ap.parse_args()

    labels = load_labels(args.labels)
    baseline = load_jsonl(args.baseline)
    candidate = load_jsonl(args.candidate)

    base_summary = summarize(baseline, labels)
    cand_summary = summarize(candidate, labels)

    # Refuse to summarize from incomplete input.
    if base_summary["missing_labels"] or cand_summary["missing_labels"]:
        print("MISSING LABELS — refusing to summarize.", file=sys.stderr)
        print(f"  baseline missing:  {base_summary['missing_labels']}", file=sys.stderr)
        print(f"  candidate missing: {cand_summary['missing_labels']}", file=sys.stderr)

    md = render_markdown(base_summary, cand_summary, args.baseline, args.candidate)
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(md, encoding="utf-8")
        print(f"wrote {args.output}", file=sys.stderr)
    else:
        sys.stdout.write(md)


if __name__ == "__main__":
    main()
