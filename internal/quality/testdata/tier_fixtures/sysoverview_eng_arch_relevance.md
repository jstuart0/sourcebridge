# System Overview: Architectural Relevance Fixture

## Overview

The notification subsystem (internal/notify/notify.go:1-80) delivers
asynchronous event messages to registered subscribers (internal/notify/sub.go:1-40).
Each subscriber defines a channel type and a delivery endpoint. The subsystem
batches outbound messages and retries failed deliveries with exponential backoff.

## Design

Delivery state is tracked in the persistence layer (internal/notify/store.go:1-60).
Subscribers register through the administrative API and receive a unique
identifier used for all subsequent operations. The subsystem is intentionally
decoupled from the event sources that feed it, communicating only through
a typed message interface rather than direct package imports.
