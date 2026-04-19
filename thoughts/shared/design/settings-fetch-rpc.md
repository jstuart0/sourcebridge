# Settings Fetch RPC Design

Date: 2026-04-19
Status: Proposed
Owner: Platform / Architecture

## Goal

Define how worker-consumed runtime settings move from the Go API to Python workers without relying on deployment env vars as the source of truth.

## Decision Summary

Use a split model:

- Per-request execution knobs continue to flow through gRPC metadata.
- Shared runtime settings move to a new settings-fetch RPC with short-lived worker caching.
- Workspace scoping is represented explicitly in both metadata and RPC requests.

This keeps hot-path overrides cheap while giving workers a canonical source for settings that are too large, too volatile, or too cross-cutting for metadata headers.

## Classification

### Keep in gRPC metadata

Use metadata for values that are:

- needed for exactly one worker call
- small scalar values
- safe to attach to every request
- expected to vary by request rather than by deployment

Metadata-owned fields:

- `model`
- `draft_model`
- `timeout_seconds`
- `max_prompt_tokens`
- `workspace_id`
- `job_id`
- `repo_id`
- `artifact_id`
- job/render hints already in use today

### Move to settings-fetch RPC

Use RPC-fetched settings for values that are:

- reused across many calls
- likely to grow into structured config
- expensive or noisy to duplicate on every request
- shared across multiple worker subsystems

RPC-owned fields:

- hierarchical comprehension thresholds
- provider fallback policy
- model policy bundles by operation group
- artifact-specific quality thresholds
- kill switches and rollout flags that workers must honor
- workspace-scoped defaults that are not request-specific

## Proposed API

Add a lightweight unary RPC in `KnowledgeService` or a sibling worker-settings service:

```proto
rpc GetRuntimeSettings(GetRuntimeSettingsRequest) returns (GetRuntimeSettingsResponse);

message GetRuntimeSettingsRequest {
  string workspace_id = 1;
  string repository_id = 2;
  string operation_group = 3;
}

message GetRuntimeSettingsResponse {
  string etag = 1;
  int32 ttl_seconds = 2;
  RuntimeSettings settings = 3;
}
```

`RuntimeSettings` should carry structured settings, not generic JSON strings. The worker cache key should be `(workspace_id, repository_id, operation_group)`.

## Metadata Contract

Extend the existing metadata contract with:

- `x-sb-workspace-id`
- `x-sb-llm-max-prompt-tokens`

Keep existing keys:

- `x-sb-llm-provider`
- `x-sb-llm-base-url`
- `x-sb-llm-api-key`
- `x-sb-model`
- `x-sb-llm-draft-model`
- `x-sb-llm-timeout-seconds`
- job metadata keys

Rule: if a setting is needed before the worker can decide whether to call `GetRuntimeSettings`, it belongs in metadata.

## Workspace Propagation

Workspace ID should exist in two places:

- gRPC metadata on every worker request as `x-sb-workspace-id`
- `GetRuntimeSettingsRequest.workspace_id`

Do not hide workspace identity inside auth headers. The worker already consumes explicit request metadata, and workspace needs to participate in cache keys and logs.

Repository ID remains in both the proto request payload and metadata because it is already part of job identity and tracing.

## Cache Policy

Use category-specific TTLs:

- Kill switches / rollout flags: `15s`
- LLM execution policy: `60s`
- Comprehension thresholds / artifact defaults: `300s`

Worker behavior:

- cache by key plus `etag`
- refresh synchronously on cache miss
- refresh asynchronously near TTL expiry when possible
- emit a fallback counter/log when serving stale cached settings

## Failure Policy

If the settings RPC is unavailable:

- Per-request metadata still applies and is treated as authoritative for request-scoped knobs.
- Workers may use cached RPC settings until TTL expiry plus a short grace window.
- If there is no cached RPC value, fail closed for worker behaviors that affect correctness or isolation.
- Fail open only for non-critical presentation/quality thresholds where a built-in default is explicitly defined.

Fail-closed cases:

- workspace isolation
- kill switches
- provider allow/deny policy

Fail-open cases:

- confidence thresholds
- rendering heuristics with documented defaults

## Migration Plan

1. Keep env vars as bootstrap defaults only.
2. Add missing metadata plumbing for `workspace_id` and `max_prompt_tokens`.
3. Add worker-side metadata readers and tests.
4. Introduce `GetRuntimeSettings` with cache and logs, initially unused for request-critical settings.
5. Move structured worker settings from env/bootstrap into RPC-backed values.
6. Keep metadata for per-request overrides permanently.
7. Remove worker reliance on deployment env vars except for startup/bootstrap and local-dev fallback.

## Operational Notes

- Log settings fetches with `workspace_id`, `repository_id`, `operation_group`, cache hit/miss, and `etag`.
- Add counters for:
  - `runtime_settings_fetch_total{result=hit|miss|stale|error}`
  - `runtime_settings_fallback_used_total{reason=cache_stale|default|string_metadata}`
- The Phase 6 dual-field proto migration should reuse this contract rather than inventing a second settings path.

## Open Follow-ups

- Decide whether settings RPC lives on `KnowledgeService` or a separate `RuntimeSettingsService`.
- Confirm whether repository-scoped overrides are real product requirements or just future-proofing.
- Define the exact worker grace window for stale cached settings.
