# Architecture: Reading Level Fixture

## Overview

The transaction manager (internal/txn/manager.go:1-60) coordinates
distributed operations across the persistence layer. Each operation
begins with a context that carries a deadline and a cancellation signal.
The coordinator tracks in-progress operations to support rollback when
a partial failure occurs during multi-step processing.

## Lifecycle

Transactions are created through the manager factory method and must be
committed or cancelled before the context deadline expires. The manager
registers each transaction in an internal table so that in-flight operations
can be observed by the monitoring subsystem (internal/txn/monitor.go:1-45).
Uncommitted transactions are automatically rolled back when the deadline
passes or when the parent context is cancelled by the calling component.

## Isolation

The transaction manager enforces snapshot isolation by default (internal/txn/isolation.go:1-50).
Two concurrent operations that modify overlapping data sets are serialised
through a conflict detector that compares their read and write sets. When
a conflict is found, the later operation is rolled back and the caller
receives a retriable error indicating that the operation should be retried
with updated data from the current snapshot.

## Configuration

The manager is configured with a maximum concurrency limit and a default
timeout value applied to transactions that do not carry an explicit deadline.
These values are set at construction time and cannot be changed while the
manager is running. The limit and timeout are shared across all transaction
contexts created through the same manager instance.
