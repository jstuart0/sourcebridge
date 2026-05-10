// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import "context"

// KnowledgeStore is the persistence interface for knowledge artifacts.
// Both the in-memory graph.Store and the SurrealDB db.SurrealStore implement
// this interface. It is defined here (in the knowledge package) rather than in
// graph to avoid an import cycle — graph depends on knowledge for model types,
// and the assembler depends on graph for GraphStore.
type KnowledgeStore interface {
	StoreKnowledgeArtifact(ctx context.Context, artifact *Artifact) (*Artifact, error)
	ClaimArtifact(ctx context.Context, key ArtifactKey, sourceRevision SourceRevision) (*Artifact, bool, error)
	ClaimArtifactWithMode(ctx context.Context, key ArtifactKey, sourceRevision SourceRevision, mode GenerationMode) (*Artifact, bool, error)
	GetKnowledgeArtifact(ctx context.Context, id string) *Artifact
	GetArtifactByKey(ctx context.Context, key ArtifactKey) *Artifact
	GetArtifactByKeyAndMode(ctx context.Context, key ArtifactKey, mode GenerationMode) *Artifact
	GetKnowledgeArtifacts(ctx context.Context, repoID string) []*Artifact
	UpdateKnowledgeArtifactStatus(ctx context.Context, id string, status ArtifactStatus) error
	SetArtifactFailed(ctx context.Context, id string, code string, message string) error
	UpdateKnowledgeArtifactProgress(ctx context.Context, id string, progress float64) error
	// UpdateKnowledgeArtifactProgressWithPhase sets progress + phase label + message
	// in one write. Used by the Phase 5 streaming progress path so the frontend
	// can display a meaningful phase label under the progress bar.
	UpdateKnowledgeArtifactProgressWithPhase(ctx context.Context, id string, progress float64, phase, message string) error
	MarkKnowledgeArtifactStale(ctx context.Context, id string, stale bool) error
	// MarkKnowledgeArtifactStaleWithReason is the per-artifact invalidation
	// path used by selective reindex. It atomically sets stale=true, records
	// the JSON-serialized invalidation reason (symbols/files/blanket) and the
	// triggering ImpactReport.ID. Used so the "why" explanation survives
	// later reindexes that replace the repository-level latest report.
	MarkKnowledgeArtifactStaleWithReason(ctx context.Context, id string, reasonJSON string, reportID string) error
	// GetArtifactsForSources returns ready artifacts whose persisted evidence
	// references any of the given (source_type, source_id) pairs. Results are
	// deduped by artifact ID.
	GetArtifactsForSources(ctx context.Context, repoID string, sources []SourceRef) []*Artifact
	// GetArtifactsForFiles returns ready artifacts whose persisted evidence
	// references any of the given file paths. Used to catch evidence rows
	// that capture a file_path without a symbol-level source_id.
	GetArtifactsForFiles(ctx context.Context, repoID string, filePaths []string) []*Artifact
	DeleteKnowledgeArtifact(ctx context.Context, id string) error
	SupersedeArtifact(ctx context.Context, id string, sections []Section) error

	StoreKnowledgeSections(ctx context.Context, artifactID string, sections []Section) error
	GetKnowledgeSections(ctx context.Context, artifactID string) []Section
	StoreRefinementUnits(ctx context.Context, artifactID string, units []RefinementUnit) error
	GetRefinementUnits(ctx context.Context, artifactID string) []RefinementUnit

	StoreKnowledgeEvidence(ctx context.Context, sectionID string, evidence []Evidence) error
	GetKnowledgeEvidence(ctx context.Context, sectionID string) []Evidence

	StoreRepositoryUnderstanding(ctx context.Context, u *RepositoryUnderstanding) (*RepositoryUnderstanding, error)
	GetRepositoryUnderstanding(ctx context.Context, repoID string, scope ArtifactScope) *RepositoryUnderstanding
	GetRepositoryUnderstandings(ctx context.Context, repoID string) []*RepositoryUnderstanding
	MarkRepositoryUnderstandingNeedsRefresh(ctx context.Context, repoID string) error
	// MarkRepositoryUnderstandingFailed transitions a running understanding row
	// (stage BUILDING_TREE or DEEPENING) to FAILED, zeroes progress fields, and
	// records the job error code and message. The gate on running stages is
	// intentional: a late callback must not clobber a READY row from a successful
	// concurrent retry. Rows already in a terminal state are silently left alone.
	MarkRepositoryUnderstandingFailed(ctx context.Context, understandingID string, errorCode, errorMessage string) error
	// UpdateRepositoryUnderstandingProgress sets the in-progress percentage,
	// phase label, and human-readable message on the understanding row so the
	// UI can surface live motion during the build (analogous to
	// UpdateKnowledgeArtifactProgressWithPhase for artifact rows).
	UpdateRepositoryUnderstandingProgress(ctx context.Context, id string, progress float64, phase, message string) error
	AttachArtifactUnderstanding(ctx context.Context, artifactID, understandingID, revisionFP string) error
	StoreArtifactDependencies(ctx context.Context, artifactID string, dependencies []ArtifactDependency) error
	GetArtifactDependencies(ctx context.Context, artifactID string) []ArtifactDependency
}
