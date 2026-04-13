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
	DeleteKnowledgeArtifact(id string) error
	SupersedeArtifact(id string, sections []Section) error

	StoreKnowledgeSections(artifactID string, sections []Section) error
	GetKnowledgeSections(artifactID string) []Section

	StoreKnowledgeEvidence(sectionID string, evidence []Evidence) error
	GetKnowledgeEvidence(sectionID string) []Evidence

	StoreRepositoryUnderstanding(u *RepositoryUnderstanding) (*RepositoryUnderstanding, error)
	GetRepositoryUnderstanding(repoID string, scope ArtifactScope) *RepositoryUnderstanding
	GetRepositoryUnderstandings(repoID string) []*RepositoryUnderstanding
	MarkRepositoryUnderstandingNeedsRefresh(repoID string) error
	AttachArtifactUnderstanding(artifactID, understandingID, revisionFP string) error
	StoreArtifactDependencies(artifactID string, dependencies []ArtifactDependency) error
	GetArtifactDependencies(artifactID string) []ArtifactDependency
}
