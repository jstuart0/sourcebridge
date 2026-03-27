// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"

	"google.golang.org/grpc/metadata"
)

// withModelMetadata enriches a context with gRPC metadata for per-operation
// model selection. The worker reads the x-sb-model key to override its default
// model for a single call.
//
// In simple mode (AdvancedMode=false) no metadata is attached — the worker
// uses its configured default.
func (r *Resolver) withModelMetadata(ctx context.Context, operationGroup string) context.Context {
	if r.Config == nil || !r.Config.LLM.AdvancedMode {
		return ctx
	}

	model := r.Config.LLM.ModelForOperation(operationGroup)
	if model == "" {
		return ctx
	}

	md := metadata.Pairs(
		"x-sb-model", model,
		"x-sb-operation", operationGroup,
	)
	return metadata.NewOutgoingContext(ctx, md)
}
