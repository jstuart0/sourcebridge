# API Reference: Code Example Fixture

## Overview

The metrics collection API (internal/metrics/collector.go:1-90) receives
structured telemetry events from instrumented services (internal/metrics/types.go:1-50).
Events are batched in memory and flushed to the storage backend at a
configurable interval to reduce write amplification.

## Authentication

All API calls require a valid service token issued by the identity provider.
The token is validated at the gateway layer before the request reaches the
collector (internal/metrics/auth.go:1-40). Tokens scoped to read-only access
are rejected at write endpoints with a 403 response.

## Event Schema

Each event carries a required source identifier, a required event name,
a Unix timestamp in milliseconds, and an optional map of string attributes.
The collector rejects events with a timestamp older than 72 hours to
prevent backfilling of historical data that would distort live dashboards.

## Batching

The collector groups events by source identifier and flushes each group
as a single write operation to the storage backend (internal/metrics/store.go:1-60).
Batch size and flush interval are independently configurable. The collector
exposes a synchronous flush endpoint for testing contexts where asynchronous
delivery would complicate assertion timing.

## Rate Limiting

Rate limiting is enforced per service token with a sliding window algorithm.
Requests that exceed the limit receive a 429 response with a Retry-After header
indicating the number of seconds until the window resets (internal/metrics/ratelimit.go:1-35).
