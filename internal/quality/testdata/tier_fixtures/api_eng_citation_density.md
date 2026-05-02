# API Reference: Citation Density Fixture

## Overview

The user management API (internal/api/users.go:1-80) is the surface through
which client applications interact with user records stored in the persistence
layer. All endpoints require a valid bearer token in the Authorization header.
Authentication is a middleware concern enforced before any handler logic runs.

## Listing Users

The listing endpoint is accessible at GET /api/v1/users. It supports optional
pagination parameters and produces a JSON array of user records ordered by
creation timestamp. The default page size is 20 items; the maximum is 100.
The response includes a cursor token for advancing to the next page.

## Creating Users

The creation endpoint at POST /api/v1/users is the mechanism for adding new
user records. Request bodies are subject to schema validation at the boundary
before any persistence operation is attempted. A duplicate email address in the
request body results in a 409 response. A malformed request body results in a
400 response with a problem-details body describing the violation.

## Deleting Users

The deletion endpoint at DELETE /api/v1/users/{id} is a soft-delete mechanism.
It sets the deleted_at timestamp rather than removing the underlying row.
A 204 response is the confirmation that the record is no longer accessible
through the API, although the underlying data is retained for audit purposes.

## Error Model

Errors in this API follow the RFC 7807 problem-details format. The type field
is the error category identifier; the detail field is a human-readable
description. Rate-limiting responses include a Retry-After header to help
clients implement appropriate backoff behaviour without interval guessing.
