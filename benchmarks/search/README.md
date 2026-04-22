# Search Benchmark Scaffold

This directory holds the curated benchmark inputs and report artifacts for the
hybrid retrieval/search plan.

It is intentionally simple:

- `queries.yaml` defines the frozen query set
- `labels.yaml` defines adjudicated relevance labels
- `reports/` stores before/after benchmark reports and raw outputs

This scaffold does **not** define runner code yet. The first goal is to make
the benchmark data model concrete so every implementation phase can report
against the same inputs.

## Required workflow

1. Curate the query set in `queries.yaml`.
2. Curate and adjudicate labels in `labels.yaml`.
3. Freeze both files before running a before/after comparison.
4. Save raw run artifacts beside the report in `reports/<date>_<name>/`.

No quality conclusion should be made from ad hoc manual queries once this
benchmark exists.

## Layout

```text
benchmarks/search/
  README.md
  queries.yaml
  labels.yaml
  reports/
    .gitkeep
```

## Query-set rules

Keep the benchmark aligned with the plan:

- minimum 120 queries
- at least 3 repositories
- at least 30 queries from a zero-link repo
- at least 30 queries from a linked repo
- include identifier, phrase, natural-language, structural, and mixed queries

## Labeling rules

Use the same relevance scale as the plan:

- `3` = ideal / exact target
- `2` = clearly useful
- `1` = marginally relevant
- `0` = irrelevant

Each query should be labeled by at least two humans initially, then adjudicated
to one frozen label set before any benchmark comparison is treated as valid.

## Suggested report artifact layout

```text
benchmarks/search/reports/2026-04-22_phase3-baseline-vs-hybrid/
  report.md
  baseline.jsonl
  candidate.jsonl
  environment.yaml
```

The report shape itself is defined in:

- [thoughts/shared/plans/2026-04-22-hybrid-retrieval-search.md](/Users/jaystuart/dev/sourcebridge/thoughts/shared/plans/2026-04-22-hybrid-retrieval-search.md)
