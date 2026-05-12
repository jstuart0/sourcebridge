package graphql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

const knowledgeWorkerUnavailableMessage = "AI features are unavailable — worker not connected"

// ErrWorkerUnavailable is returned by requireKnowledgeGenerationSupport when
// no AI worker is connected. Callers can use errors.Is to distinguish this
// from other errors.
var ErrWorkerUnavailable = errors.New("worker unavailable")

// ErrKnowledgeStoreUnavailable is returned by requireKnowledgeGenerationSupport
// when the knowledge store has not been configured.
var ErrKnowledgeStoreUnavailable = errors.New("knowledge store not configured")

func (r *Resolver) requireKnowledgeGenerationSupport() error {
	if r.Deps.Worker == nil {
		return fmt.Errorf("%s: %w", knowledgeWorkerUnavailableMessage, ErrWorkerUnavailable)
	}
	if r.Deps.KnowledgeStore == nil {
		// Return the sentinel directly to avoid the duplicated-message render
		// ("knowledge store not configured: knowledge store not configured")
		// surfaced by xander on the CA-320 Phase 2 mid-build review.
		return ErrKnowledgeStoreUnavailable
	}
	return nil
}

func (r *Resolver) loadKnowledgeRepository(ctx context.Context, repositoryID string) (*graphstore.Repository, error) {
	repo := r.getStore(ctx).GetRepository(ctx, repositoryID)
	if repo == nil {
		return nil, fmt.Errorf("repository %s not found", repositoryID)
	}
	return repo, nil
}

func protoAudience(audience knowledgepkg.Audience) knowledgev1.Audience {
	switch audience {
	case knowledgepkg.AudienceBeginner:
		return knowledgev1.Audience_AUDIENCE_BEGINNER
	case knowledgepkg.AudienceDeveloper:
		return knowledgev1.Audience_AUDIENCE_DEVELOPER
	default:
		// A new Audience enum value added on the domain side without
		// updating this switch silently degrades to UNSPECIFIED; log so
		// the regression is observable in operator-facing structured logs.
		slog.Warn("protoAudience received unknown Audience value", "value", string(audience))
		return knowledgev1.Audience_AUDIENCE_UNSPECIFIED
	}
}

func protoDepth(depth knowledgepkg.Depth) knowledgev1.Depth {
	switch depth {
	case knowledgepkg.DepthSummary:
		return knowledgev1.Depth_DEPTH_SUMMARY
	case knowledgepkg.DepthMedium:
		return knowledgev1.Depth_DEPTH_MEDIUM
	case knowledgepkg.DepthDeep:
		return knowledgev1.Depth_DEPTH_DEEP
	default:
		// Same rationale as protoAudience: a new Depth enum value silently
		// degrading to UNSPECIFIED is a class of bug that observability
		// surfaces immediately.
		slog.Warn("protoDepth received unknown Depth value", "value", string(depth))
		return knowledgev1.Depth_DEPTH_UNSPECIFIED
	}
}
