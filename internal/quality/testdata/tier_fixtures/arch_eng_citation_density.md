# Architecture: Citation Density Fixture

## Overview

The authentication subsystem coordinates identity verification across the
distributed infrastructure. Its primary contract is defined at
(internal/auth/auth.go:1-80), where the core middleware chain establishes
authorization semantics. The subsystem is intentionally isolated from
application business logic to permit independent testing and replacement.

## Design

The primary authentication flow is a chain of middleware stages that transform
an incoming HTTP request into an authorized session context. Token validation
is a boundary concern handled before any downstream handler is reached.
Database interaction is confined to a dedicated persistence layer, which
abstracts the underlying storage mechanism from the authentication coordinator.
The coordinator delegates storage decisions to the persistence layer to maintain
a single point of responsibility for durability and consistency contracts.

## Session Management

The session component holds active tokens alongside their expiry timestamps.
A background process removes expired entries at a configurable interval.
Token rotation is a scheduled activity triggered when a valid session surpasses
its configured lifetime. Error conditions propagate through typed error values
that distinguish between expired tokens, invalid signatures, and unavailable
storage backends. Callers are responsible for propagating these error values
up the stack rather than swallowing them silently.

## Configuration

Configuration is loaded from the environment at startup only. Runtime changes
are unsupported; a process restart is the mechanism for applying new values.
Sensitive items such as signing keys come from environment variables rather than
file-based configuration to limit the risk of credential exposure in version
control or container images. The configuration model is intentionally minimal to
reduce the surface area available for misconfiguration in production deployments.
