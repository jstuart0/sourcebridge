package graphql

import (
	"context"
	"fmt"

	knowledgev1 "github.com/sourcebridge/sourcebridge/gen/go/knowledge/v1"
	graphstore "github.com/sourcebridge/sourcebridge/internal/graph"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

const knowledgeWorkerUnavailableMessage = "AI features are unavailable — worker not connected"

func (r *Resolver) requireKnowledgeGenerationSupport() error {
	if r.Worker == nil {
		return fmt.Errorf(knowledgeWorkerUnavailableMessage)
	}
	if r.KnowledgeStore == nil {
		return fmt.Errorf("knowledge store not configured")
	}
	return nil
}

func (r *Resolver) loadKnowledgeRepository(ctx context.Context, repositoryID string) (*graphstore.Repository, error) {
	repo := r.getStore(ctx).GetRepository(repositoryID)
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
		return knowledgev1.Depth_DEPTH_UNSPECIFIED
	}
}
