# Architecture: Benchmarks Sub-Page

## Overview

This sub-page covers the benchmarks directory layout, its organization by workload type, and the relevant build targets for each category.

## Structure

The benchmarks package is split into three top-level directories, one per workload category, each with a dedicated runner and scenario definitions.

## Configuration

Each benchmark suite has a YAML file at the package root governing concurrency, iteration count, warm-up duration, and output format.

## Execution

Benchmark binaries are standalone executables built per subdirectory; the CI pipeline runs them on a dedicated node to avoid interference from shared workloads.

## Output Format

Results land in JSON Lines format with one measurement object per line, timestamped and tagged with the scenario name and host profile.

```go
// Run a single benchmark suite.
cmd.Execute()
```
