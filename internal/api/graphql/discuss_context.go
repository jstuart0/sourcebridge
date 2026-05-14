// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package graphql

import (
	"context"
	"fmt"
	"strings"

	commonv1 "github.com/sourcebridge/sourcebridge/gen/go/common/v1"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// assembleDiscussionContext builds the context code and context symbols for a
// DiscussCode call, honouring the priority order:
//
//	conversationHistory → artifactID → requirementID → symbolID → filePath
//
// It also computes the effective file path (which may be derived from a
// matched symbol when the caller did not provide one) and the proto language
// enum so the DiscussCode resolver does not need to repeat that derivation.
//
// Extracted from the DiscussCode resolver (formerly schema.resolvers.go ~688–771)
// to reduce its body size and make context-resolution unit-testable.
// GQL-4 (dexter H-3) — Phase 1 Slice 3.
func assembleDiscussionContext(
	ctx context.Context,
	r *Resolver,
	input DiscussCodeInput,
) (contextCode string, contextSymbols []*commonv1.CodeSymbol, lang commonv1.Language, effectiveFilePath string, err error) {
	contextParts := make([]string, 0, 4)
	if len(input.ConversationHistory) > 0 {
		contextParts = append(contextParts, "Recent follow-up context:\n"+strings.Join(input.ConversationHistory, "\n\n"))
	}
	if input.ArtifactID != nil && *input.ArtifactID != "" && r.Deps.KnowledgeStore != nil {
		if artifact := r.Deps.KnowledgeStore.GetKnowledgeArtifact(ctx, *input.ArtifactID); artifact != nil {
			contextParts = append(contextParts, knowledgepkg.DiscussionContextFromArtifact(artifact))
		}
	}
	if input.RequirementID != nil && *input.RequirementID != "" {
		if req := r.getStore(ctx).GetRequirement(ctx, *input.RequirementID); req != nil {
			contextParts = append(contextParts, fmt.Sprintf(
				"Requirement context:\nID: %s\nTitle: %s\nDescription: %s",
				req.ExternalID, req.Title, req.Description,
			))
		}
	}

	// Prefer provided code; otherwise use indexed symbol context; finally fall back to file reads.
	if input.Code != nil && *input.Code != "" {
		contextParts = append(contextParts, *input.Code)
	} else if input.SymbolID != nil && *input.SymbolID != "" {
		if sym := r.getStore(ctx).GetSymbol(ctx, *input.SymbolID); sym != nil {
			contextParts = append(contextParts, discussionContextFromStoredSymbol(sym))
			// Derive file path from the symbol when the caller did not supply one.
			if input.FilePath == nil || *input.FilePath == "" {
				fp := sym.FilePath
				input.FilePath = &fp
			}
			// Also read the actual source file so the LLM has full code context, not just metadata.
			if input.FilePath != nil && *input.FilePath != "" {
				repo := r.getStore(ctx).GetRepository(ctx, input.RepositoryID)
				repoRoot, readErr := resolveRepoSourcePath(repo)
				if readErr == nil {
					content, readErr := readSourceFile(repoRoot, *input.FilePath)
					if readErr == nil && content != "" {
						contextParts = append(contextParts, content)
					}
				}
			}
		}
	}
	if len(contextParts) == 0 && input.FilePath != nil && *input.FilePath != "" {
		repo := r.getStore(ctx).GetRepository(ctx, input.RepositoryID)
		repoRoot, readErr := resolveRepoSourcePath(repo)
		if readErr != nil {
			return "", nil, 0, "", fmt.Errorf("source unavailable: %w", readErr)
		}
		content, readErr := readSourceFile(repoRoot, *input.FilePath)
		if readErr != nil {
			return "", nil, 0, "", fmt.Errorf("reading source: %w", readErr)
		}
		contextParts = append(contextParts, content)
	}

	contextCode = strings.TrimSpace(strings.Join(contextParts, "\n\n"))
	if contextCode == "" {
		return "", nil, 0, "", fmt.Errorf("provide code, filePath, artifactId, or symbolId")
	}

	// Derive language from explicit input first; fall back to file extension.
	lang = commonv1.Language_LANGUAGE_UNSPECIFIED
	if input.Language != nil {
		lang = languageToProto(input.Language.String())
	} else if input.FilePath != nil && *input.FilePath != "" {
		lang = deriveLanguage(*input.FilePath)
	}

	// Collect context symbols: the named symbol + all file-level symbols.
	if input.SymbolID != nil && *input.SymbolID != "" {
		if sym := r.getStore(ctx).GetSymbol(ctx, *input.SymbolID); sym != nil {
			contextSymbols = append(contextSymbols, protoCodeSymbolFromStored(sym))
		}
	}
	// Capture the (possibly derived) file path before returning.
	if input.FilePath != nil {
		effectiveFilePath = *input.FilePath
		syms := r.getStore(ctx).GetSymbolsByFile(ctx, input.RepositoryID, *input.FilePath)
		for _, s := range syms {
			contextSymbols = append(contextSymbols, &commonv1.CodeSymbol{
				Id:   s.ID,
				Name: s.Name,
			})
		}
	}

	return contextCode, contextSymbols, lang, effectiveFilePath, nil
}
