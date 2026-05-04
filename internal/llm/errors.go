// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package llm

import (
	"strings"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// ErrorClass is a coarse classification of an LLM-related error.
type ErrorClass int

const (
	// ErrUnknown covers errors that do not match any known pattern.
	// Callers that need a safe default should treat unknown as retryable once.
	ErrUnknown ErrorClass = iota

	// ErrTransient covers errors like "llm returned empty content" that suggest
	// the worker produced no output but may succeed on retry.
	ErrTransient

	// ErrComputeError covers provider-side compute or server_error failures.
	ErrComputeError

	// ErrTimeout covers deadline-exceeded and context-deadline errors.
	ErrTimeout

	// ErrUnavailable covers connection-refused, transport-closing, and
	// service-unavailable errors.
	ErrUnavailable

	// ErrTerminal covers errors that should never be retried: snapshot too
	// large, budget exceeded, invalid argument, not found, permission denied,
	// unauthenticated, already exists, failed precondition, and
	// orchestrator shutdown.
	ErrTerminal
)

// ClassifyLLMError inspects an error from an LLM backend and returns an
// ErrorClass. Centralizes the heuristics previously duplicated across
// internal/llm/orchestrator/retry.go, internal/llm/orchestrator/runtime.go,
// and internal/livingwiki/orchestrator/orchestrator.go.
func ClassifyLLMError(err error) ErrorClass {
	if err == nil {
		return ErrUnknown
	}

	if st, ok := grpcstatus.FromError(err); ok {
		switch st.Code() {
		case codes.DeadlineExceeded:
			return ErrTimeout
		case codes.Unavailable:
			return ErrUnavailable
		case codes.ResourceExhausted, codes.Aborted:
			return ErrTransient
		case codes.InvalidArgument, codes.NotFound, codes.PermissionDenied,
			codes.Unauthenticated, codes.AlreadyExists, codes.FailedPrecondition:
			return ErrTerminal
		}
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "snapshot too large"), strings.Contains(msg, "exceeds budget"):
		return ErrTerminal
	case strings.Contains(msg, "orchestrator is shutting down"):
		return ErrTerminal
	case strings.Contains(msg, "llm returned empty content"), strings.Contains(msg, "empty content"):
		return ErrTransient
	case strings.Contains(msg, "compute error"), strings.Contains(msg, "server_error"):
		return ErrComputeError
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "context deadline"):
		return ErrTimeout
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "transport is closing"),
		strings.Contains(msg, "unavailable"):
		return ErrUnavailable
	}
	return ErrUnknown
}
