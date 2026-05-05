# Architecture: Benchmarks Sub-Page

## Overview

This sub-page covers the benchmarks directory layout and its build targets.

## Structure

The benchmarks package is organized into three top-level directories by workload type.

## Configuration

Each benchmark suite is configured through a YAML file at the package root.

## Execution

Benchmarks run as standalone binaries and write results to the output directory.

## Output Format

Results are written in JSON Lines format with one measurement object per line.

```go
// Run a single benchmark suite.
cmd.Execute()
```
