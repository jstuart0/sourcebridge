// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package llm

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestClassifyLLMError_NilReturnsUnknown(t *testing.T) {
	if got := ClassifyLLMError(nil); got != ErrUnknown {
		t.Errorf("nil error: got %v, want ErrUnknown", got)
	}
}

func TestClassifyLLMError_GRPCCodes(t *testing.T) {
	cases := []struct {
		code codes.Code
		want ErrorClass
	}{
		{codes.DeadlineExceeded, ErrTimeout},
		{codes.Unavailable, ErrUnavailable},
		{codes.ResourceExhausted, ErrTransient},
		{codes.Aborted, ErrTransient},
		{codes.InvalidArgument, ErrTerminal},
		{codes.NotFound, ErrTerminal},
		{codes.PermissionDenied, ErrTerminal},
		{codes.Unauthenticated, ErrTerminal},
		{codes.AlreadyExists, ErrTerminal},
		{codes.FailedPrecondition, ErrTerminal},
	}
	for _, tc := range cases {
		err := grpcstatus.Error(tc.code, tc.code.String())
		if got := ClassifyLLMError(err); got != tc.want {
			t.Errorf("gRPC code %v: got %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestClassifyLLMError_StringPatterns(t *testing.T) {
	cases := []struct {
		msg  string
		want ErrorClass
	}{
		{"snapshot too large: 1mb exceeds budget", ErrTerminal},
		{"exceeds budget limit", ErrTerminal},
		{"orchestrator is shutting down", ErrTerminal},
		{"LLM returned empty content", ErrTransient},
		{"empty content from provider", ErrTransient},
		{"compute error: provider failed", ErrComputeError},
		{"received server_error from api", ErrComputeError},
		{"context deadline exceeded", ErrTimeout},
		{"deadline exceeded after 30s", ErrTimeout},
		{"connection refused to worker", ErrUnavailable},
		{"transport is closing", ErrUnavailable},
		{"service unavailable", ErrUnavailable},
		{"some unexpected error", ErrUnknown},
	}
	for _, tc := range cases {
		err := errors.New(tc.msg)
		if got := ClassifyLLMError(err); got != tc.want {
			t.Errorf("msg %q: got %v, want %v", tc.msg, got, tc.want)
		}
	}
}
