// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

// KnowledgeStore is the persistence interface for knowledge artifacts.
// Both the in-memory graph.Store and the SurrealDB db.SurrealStore implement
// this interface. It is defined here (in the knowledge package) rather than in
// graph to avoid an import cycle — graph depends on knowledge for model types,
// and the assembler depends on graph for GraphStore.
type KnowledgeStore interface {
	StoreKnowledgeArtifact(artifact *Artifact) (*Artifact, error)
	ClaimArtifact(key ArtifactKey, sourceRevision SourceRevision) (*Artifact, bool, error)
	ClaimArtifactWithMode(key ArtifactKey, sourceRevision SourceRevision, mode GenerationMode) (*Artifact, bool, error)
	GetKnowledgeArtifact(id string) *Artifact
	GetArtifactByKey(key ArtifactKey) *Artifact
	GetArtifactByKeyAndMode(key ArtifactKey, mode GenerationMode) *Artifact
	GetKnowledgeArtifacts(repoID string) []*Artifact
	UpdateKnowledgeArtifactStatus(id string, status ArtifactStatus) error
	SetArtifactFailed(id string, code string, message string) error
	UpdateKnowledgeArtifactProgress(id string, progress float64) error
	// UpdateKnowledgeArtifactProgressWithPhase sets progress + phase label + message
	// in one write. Used by the Phase 5 streaming progress path so the frontend
	// can display a meaningful phase label under the progress bar.
	UpdateKnowledgeArtifactProgressWithPhase(id string, progress float64, phase, message string) error
	MarkKnowledgeArtifactStale(id string, stale bool) error
	// MarkKnowledgeArtifactStaleWithReason is the per-artifact invalidation
	// path used by selective reindex. It atomically sets stale=true, records
	// the JSON-serialized invalidation reason (symbols/files/blanket) and the
	// triggering ImpactReport.ID. Used so the "why" explanation survives
	// later reindexes that replace the repository-level latest report.
	MarkKnowledgeArtifactStaleWithReason(id string, reasonJSON string, reportID string) error
	// GetArtifactsForSources returns ready artifacts whose persisted evidence
	// references any of the given (source_type, source_id) pairs. Results are
	// deduped by artifact ID.
	GetArtifactsForSources(repoID string, sources []SourceRef) []*Artifact
	// GetArtifactsForFiles returns ready artifacts whose persisted evidence
	// references any of the given file paths. Used to catch evidence rows
	// that capture a file_path without a symbol-level source_id.
	GetArtifactsForFiles(repoID string, filePaths []string) []*Artifact
	DeleteKnowledgeArtifact(id string) error
	SupersedeArtifact(id string, sections []Section) error

	StoreKnowledgeSections(artifactID string, sections []Section) error
	GetKnowledgeSections(artifactID string) []Section
	StoreRefinementUnits(artifactID string, units []RefinementUnit) error
	GetRefinementUnits(artifactID string) []RefinementUnit

	StoreKnowledgeEvidence(sectionID string, evidence []Evidence) error
	GetKnowledgeEvidence(sectionID string) []Evidence

	StoreRepositoryUnderstanding(u *RepositoryUnderstanding) (*RepositoryUnderstanding, error)
	GetRepositoryUnderstanding(repoID string, scope ArtifactScope) *RepositoryUnderstanding
	GetRepositoryUnderstandings(repoID string) []*RepositoryUnderstanding
	MarkRepositoryUnderstandingNeedsRefresh(repoID string) error
	// UpdateRepositoryUnderstandingProgress sets the in-progress percentage,
	// phase label, and human-readable message on the understanding row so the
	// UI can surface live motion during the build (analogous to
	// UpdateKnowledgeArtifactProgressWithPhase for artifact rows).
	UpdateRepositoryUnderstandingProgress(id string, progress float64, phase, message string) error
	AttachArtifactUnderstanding(artifactID, understandingID, revisionFP string) error
	StoreArtifactDependencies(artifactID string, dependencies []ArtifactDependency) error
	GetArtifactDependencies(artifactID string) []ArtifactDependency
}
