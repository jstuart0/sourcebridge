// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package orchestrator

import (
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// RetryPolicy controls how the orchestrator retries transiently-failing jobs.
// Zero values produce a no-retry policy (MaxAttempts = 1 = try once).
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the first.
	// MaxAttempts = 1 means no retries.
	MaxAttempts int
	// InitialBackoff is the delay before the first retry.
	InitialBackoff time.Duration
	// MaxBackoff caps the exponential backoff.
	MaxBackoff time.Duration
	// Multiplier is the exponential growth factor between attempts.
	// Defaults to 2.0 when zero.
	Multiplier float64
}

// DefaultRetryPolicy matches the Phase 2 plan: two attempts total (one
// retry), 5-second initial backoff, capped at 30 seconds, 2x multiplier.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    2,
		InitialBackoff: 5 * time.Second,
		MaxBackoff:     30 * time.Second,
		Multiplier:     2.0,
	}
}

// BackoffFor returns the delay to wait before attempt number n (1-indexed
// so BackoffFor(2) is the delay before the second attempt). Returns zero
// for the first attempt since there is no wait before it.
func (p RetryPolicy) BackoffFor(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	multiplier := p.Multiplier
	if multiplier <= 0 {
		multiplier = 2.0
	}
	backoff := float64(p.InitialBackoff)
	for i := 2; i < attempt; i++ {
		backoff *= multiplier
	}
	if p.MaxBackoff > 0 && time.Duration(backoff) > p.MaxBackoff {
		return p.MaxBackoff
	}
	return time.Duration(backoff)
}

// ShouldRetry reports whether the orchestrator should attempt this job
// again after the supplied error, given the current attempt count. The
// returned boolean is informational for logging; the orchestrator also
// checks attempt < MaxAttempts before actually retrying.
func (p RetryPolicy) ShouldRetry(attempt int, err error) bool {
	return p.ShouldRetryWithMax(attempt, p.MaxAttempts, err)
}

// ShouldRetryWithMax is like ShouldRetry but uses a caller-supplied max
// attempts instead of the policy default. This allows per-job max attempts
// to override the global retry policy.
func (p RetryPolicy) ShouldRetryWithMax(attempt, maxAttempts int, err error) bool {
	if err == nil {
		return false
	}
	if attempt >= maxAttempts {
		return false
	}
	return IsRetryable(err)
}

// IsRetryable classifies errors as retryable or terminal. The rules match
// the Phase 4 plan:
//
//   - DeadlineExceeded, Unavailable, LLM_EMPTY → retryable (transient)
//   - SNAPSHOT_TOO_LARGE, InvalidArgument, NotFound, PermissionDenied → terminal
//   - Unknown / default → treat as retryable once (assume transient)
//
// Callers that want stricter classification can wrap this with their own
// allowlist.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := grpcstatus.FromError(err); ok {
		switch st.Code() {
		case codes.DeadlineExceeded, codes.Unavailable, codes.ResourceExhausted, codes.Aborted:
			return true
		case codes.InvalidArgument, codes.NotFound, codes.PermissionDenied,
			codes.Unauthenticated, codes.AlreadyExists, codes.FailedPrecondition:
			return false
		}
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "snapshot too large"), strings.Contains(msg, "exceeds budget"):
		return false
	case strings.Contains(msg, "llm returned empty content"):
		return true
	case strings.Contains(msg, "compute error"), strings.Contains(msg, "server_error"):
		return true
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "context deadline"):
		return true
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "unavailable"):
		return true
	}
	return true
}

// ErrMaxAttemptsExceeded is returned by the orchestrator when a job has
// exhausted its retry budget. It wraps the last underlying error so
// callers can still inspect it via errors.Is / errors.As.
type ErrMaxAttemptsExceeded struct {
	Attempts int
	Err      error
}

func (e *ErrMaxAttemptsExceeded) Error() string {
	return "exceeded max retry attempts: " + e.Err.Error()
}

func (e *ErrMaxAttemptsExceeded) Unwrap() error { return e.Err }

// Ensure the sentinel fits the standard error interface.
var _ error = (*ErrMaxAttemptsExceeded)(nil)

// Sentinel error returned when the caller asks to cancel a job before
// it starts. The orchestrator translates this into StatusCancelled and
// never retries it.
var ErrJobCancelled = errors.New("job cancelled")
