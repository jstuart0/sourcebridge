# API Reference: Authentication Service

## Overview

The authentication service (internal/auth/service.go:1-60) is the central authority
for identity verification across all request paths. Every incoming request is checked
against the token registry (internal/auth/registry.go:10-45) before handler execution
proceeds. The service uses a rotating key set to validate signatures without requiring
external round-trips during normal operation.

## TokenValidator

The TokenValidator type (internal/auth/validator.go:12-55) accepts a signed JWT string
and returns the decoded Claims struct or an error when validation fails. The validator
checks the signature, the expiry field, and the issuer claim in that order before
returning. Callers receive a typed Claims value and do not interact with raw token bytes
after this boundary.

Example usage:

```go
claims, err := validator.Validate(ctx, rawToken)
if err != nil {
    return nil, err
}
```

## SessionStore

The session store interface (internal/auth/session.go:8-30) accepts a Claims value and
returns an opaque session handle backed by the configured persistence layer. The store
validates that the subject claim maps to an active user record and returns an error when
the account is suspended. The returned handle is goroutine-safe without external
synchronization.

## RevokeSession

The revoke endpoint marks a session handle as invalid in the backing store and ensures
that subsequent requests using the same handle are rejected without a round-trip to the
issuer. The revocation takes effect before the function returns and is immediately
visible to all nodes sharing the same store backend. No grace period is applied.

## ErrorCodes

Errors produced by this service follow a structured format (internal/auth/errors.go:5-28)
with a machine-readable code and a human-readable detail field. Client applications
should branch on the code field rather than parsing the detail string, as detail
messages are subject to change between releases.
