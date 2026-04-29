// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"fmt"
	"strings"
	"time"
)

// ArtifactType identifies the kind of knowledge artifact.
type ArtifactType string

const (
	ArtifactCliffNotes          ArtifactType = "cliff_notes"
	ArtifactArchitectureDiagram ArtifactType = "architecture_diagram"
	ArtifactLearningPath        ArtifactType = "learning_path"
	ArtifactCodeTour            ArtifactType = "code_tour"
	ArtifactWorkflowStory       ArtifactType = "workflow_story"
	// Reserved but deferred to enterprise.
	ArtifactSlideOutline        ArtifactType = "slide_outline"
	ArtifactAudioBriefingScript ArtifactType = "audio_briefing_script"
)

// ArtifactStatus represents the lifecycle state of an artifact.
type ArtifactStatus string

const (
	StatusPending    ArtifactStatus = "pending"
	StatusGenerating ArtifactStatus = "generating"
	StatusReady      ArtifactStatus = "ready"
	StatusFailed     ArtifactStatus = "failed"
	StatusStale      ArtifactStatus = "stale"
)

// Audience identifies the target reader of a knowledge artifact.
type Audience string

const (
	AudienceBeginner  Audience = "beginner"
	AudienceDeveloper Audience = "developer"
	// Deferred enterprise audiences.
	AudienceArchitect      Audience = "architect"
	AudienceProductManager Audience = "product_manager"
	AudienceExecutive      Audience = "executive"
)

// OSSAudiences returns the audiences available in the OSS edition.
func OSSAudiences() []Audience {
	return []Audience{AudienceBeginner, AudienceDeveloper}
}

// IsOSSAudience returns true if the audience is available in the OSS edition.
func IsOSSAudience(a Audience) bool {
	return a == AudienceBeginner || a == AudienceDeveloper
}

// Depth controls the level of detail in a generated artifact.
type Depth string

const (
	DepthSummary Depth = "summary"
	DepthMedium  Depth = "medium"
	DepthDeep    Depth = "deep"
)

// ValidDepths returns all valid depth values.
func ValidDepths() []Depth {
	return []Depth{DepthSummary, DepthMedium, DepthDeep}
}

// IsValidDepth returns true if d is a recognized depth level.
func IsValidDepth(d Depth) bool {
	return d == DepthSummary || d == DepthMedium || d == DepthDeep
}

// ScopeType identifies the repository slice an artifact is about.
type ScopeType string

const (
	ScopeRepository  ScopeType = "repository"
	ScopeModule      ScopeType = "module"
	ScopeFile        ScopeType = "file"
	ScopeSymbol      ScopeType = "symbol"
	ScopeRequirement ScopeType = "requirement"
)

// ArtifactScope identifies a repo/module/file/symbol target for an artifact.
type ArtifactScope struct {
	ScopeType  ScopeType `json:"scope_type"`
	ScopePath  string    `json:"scope_path,omitempty"`
	ModulePath string    `json:"module_path,omitempty"`
	FilePath   string    `json:"file_path,omitempty"`
	SymbolName string    `json:"symbol_name,omitempty"`
}

// Normalize returns the canonical representation used for storage and lookup.
func (s ArtifactScope) Normalize() ArtifactScope {
	out := s
	if out.ScopeType == "" {
		out.ScopeType = ScopeRepository
	}
	out.ScopePath = normalizeScopePath(out.ScopeType, out.ScopePath)
	switch out.ScopeType {
	case ScopeRepository:
		out.ScopePath = ""
		out.ModulePath = ""
		out.FilePath = ""
		out.SymbolName = ""
	case ScopeModule:
		out.ModulePath = out.ScopePath
		out.FilePath = ""
		out.SymbolName = ""
	case ScopeFile:
		out.FilePath = out.ScopePath
		out.ModulePath = modulePathForFile(out.FilePath)
		out.SymbolName = ""
	case ScopeSymbol:
		out.FilePath, out.SymbolName = splitSymbolScopePath(out.ScopePath)
		out.ModulePath = modulePathForFile(out.FilePath)
	case ScopeRequirement:
		// ScopePath is the requirement ID.
		out.ModulePath = ""
		out.FilePath = ""
		out.SymbolName = ""
	}
	return out
}

// NormalizePtr returns a normalized copy pointer.
func (s ArtifactScope) NormalizePtr() *ArtifactScope {
	norm := s.Normalize()
	return &norm
}

// ScopeKey returns the canonical DB key for the scope.
func (s ArtifactScope) ScopeKey() string {
	norm := s.Normalize()
	if norm.ScopeType == ScopeRepository {
		return "repository:"
	}
	return string(norm.ScopeType) + ":" + norm.ScopePath
}

// ArtifactKey is the canonical deduplication key for a knowledge artifact.
type ArtifactKey struct {
	RepositoryID string
	Type         ArtifactType
	Audience     Audience
	Depth        Depth
	Scope        ArtifactScope
}

// Normalized returns a key with canonical values.
func (k ArtifactKey) Normalized() ArtifactKey {
	k.Audience = Audience(strings.ToLower(string(k.Audience)))
	k.Depth = Depth(strings.ToLower(string(k.Depth)))
	k.Scope = k.Scope.Normalize()
	if k.Scope.ScopeType == "" {
		k.Scope.ScopeType = ScopeRepository
	}
	return k
}

// ScopeKey returns the canonical scope key string for persistence lookups.
func (k ArtifactKey) ScopeKey() string {
	return k.Normalized().Scope.ScopeKey()
}

// Validate returns an error when the scope is incomplete for its type.
func (k ArtifactKey) Validate() error {
	norm := k.Normalized()
	if norm.RepositoryID == "" {
		return fmt.Errorf("repository id is required")
	}
	if norm.Type == "" {
		return fmt.Errorf("artifact type is required")
	}
	switch norm.Scope.ScopeType {
	case ScopeRepository:
		return nil
	case ScopeModule, ScopeFile, ScopeSymbol, ScopeRequirement:
		if norm.Scope.ScopePath == "" {
			return fmt.Errorf("scope path is required for scope type %s", norm.Scope.ScopeType)
		}
		return nil
	default:
		return fmt.Errorf("invalid scope type %q", norm.Scope.ScopeType)
	}
}

// ConfidenceLevel expresses how well-supported a generated section is.
type ConfidenceLevel string

const (
	ConfidenceHigh   ConfidenceLevel = "high"
	ConfidenceMedium ConfidenceLevel = "medium"
	ConfidenceLow    ConfidenceLevel = "low"
)

// ConfidenceRules:
//   high   — directly supported by multiple strong evidence refs
//   medium — supported by limited but concrete evidence
//   low    — weakly supported or substantially inferred

// EvidenceSourceType identifies what kind of source artifact an evidence record points to.
type EvidenceSourceType string

const (
	EvidenceRepository    EvidenceSourceType = "repository"
	EvidenceFile          EvidenceSourceType = "file"
	EvidenceSymbol        EvidenceSourceType = "symbol"
	EvidenceRequirement   EvidenceSourceType = "requirement"
	EvidenceLink          EvidenceSourceType = "link"
	EvidenceTest          EvidenceSourceType = "test"
	EvidenceCommit        EvidenceSourceType = "commit"
	EvidenceDocumentation EvidenceSourceType = "documentation"
)

// SourceRevision is a composite repository snapshot marker used for freshness tracking.
type SourceRevision struct {
	CommitSHA          string `json:"commit_sha,omitempty"`
	Branch             string `json:"branch,omitempty"`
	ContentFingerprint string `json:"content_fingerprint,omitempty"`
	DocsFingerprint    string `json:"docs_fingerprint,omitempty"`
}

// SourceRef identifies an evidence source (e.g. a specific symbol or file)
// referenced by a knowledge artifact. Used by selective invalidation to
// look up artifacts whose provenance points at a changed entity.
type SourceRef struct {
	SourceType EvidenceSourceType
	SourceID   string
}

// Artifact is a persisted knowledge artifact (Cliff Notes, learning path, code tour).
type Artifact struct {
	ID                      string         `json:"id"`
	RepositoryID            string         `json:"repository_id"`
	Type                    ArtifactType   `json:"type"`
	Audience                Audience       `json:"audience"`
	Depth                   Depth          `json:"depth"`
	GenerationMode          GenerationMode `json:"generation_mode,omitempty"`
	Scope                   *ArtifactScope `json:"scope,omitempty"`
	Status                  ArtifactStatus `json:"status"`
	Progress                float64        `json:"progress"`
	ProgressPhase           string         `json:"progress_phase,omitempty"`
	ProgressMessage         string         `json:"progress_message,omitempty"`
	ErrorCode               string         `json:"error_code,omitempty"`
	ErrorMessage            string         `json:"error_message,omitempty"`
	SourceRevision          SourceRevision `json:"source_revision"`
	UnderstandingID         string         `json:"understanding_id,omitempty"`
	UnderstandingRevisionFP string         `json:"understanding_revision_fp,omitempty"`
	RendererVersion         string         `json:"renderer_version,omitempty"`
	Stale                   bool           `json:"stale"`
	// StaleReasonJSON is the JSON-serialized graph.StaleArtifactReason that
	// caused the most recent stale mark (if any). Persisted on the artifact
	// so the "why" explanation survives later reindexes that overwrite the
	// repository-level latest ImpactReport.
	StaleReasonJSON string `json:"stale_reason_json,omitempty"`
	// StaleReportID is the ImpactReport.ID that caused the stale mark.
	StaleReportID string    `json:"stale_report_id,omitempty"`
	GeneratedAt   time.Time `json:"generated_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Sections      []Section `json:"sections,omitempty"`
}

// GenerationMode determines which orchestration path should be used to
// generate or refresh a knowledge artifact.
type GenerationMode string

const (
	GenerationModeClassic            GenerationMode = "classic"
	GenerationModeUnderstandingFirst GenerationMode = "understanding_first"
)

func NormalizeGenerationMode(mode GenerationMode) GenerationMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(GenerationModeClassic):
		return GenerationModeClassic
	default:
		return GenerationModeUnderstandingFirst
	}
}

// RepositoryUnderstandingStage represents the lifecycle of the shared
// repository understanding artifact.
type RepositoryUnderstandingStage string

const (
	UnderstandingBuildingTree   RepositoryUnderstandingStage = "building_tree"
	UnderstandingFirstPassReady RepositoryUnderstandingStage = "first_pass_ready"
	UnderstandingNeedsRefresh   RepositoryUnderstandingStage = "needs_refresh"
	UnderstandingDeepening      RepositoryUnderstandingStage = "deepening"
	UnderstandingReady          RepositoryUnderstandingStage = "ready"
	UnderstandingFailed         RepositoryUnderstandingStage = "failed"
)

// IsRunning reports whether work is actively in progress for this stage.
// Only running stages may carry meaningful progress / progress_phase /
// progress_message values; any write that lands a non-running stage must
// zero those fields so the UI never shows leftover heartbeat text.
func (s RepositoryUnderstandingStage) IsRunning() bool {
	switch s {
	case UnderstandingBuildingTree, UnderstandingDeepening:
		return true
	default:
		return false
	}
}

// RepositoryUnderstandingTreeStatus captures whether the underlying summary
// tree exists and how complete it is.
type RepositoryUnderstandingTreeStatus string

const (
	UnderstandingTreeMissing  RepositoryUnderstandingTreeStatus = "missing"
	UnderstandingTreePartial  RepositoryUnderstandingTreeStatus = "partial"
	UnderstandingTreeComplete RepositoryUnderstandingTreeStatus = "complete"
)

// RepositoryUnderstanding is the first-class persisted understanding record
// backing cliff notes and later downstream artifact generation.
type RepositoryUnderstanding struct {
	ID              string                            `json:"id"`
	RepositoryID    string                            `json:"repository_id"`
	Scope           *ArtifactScope                    `json:"scope,omitempty"`
	CorpusID        string                            `json:"corpus_id,omitempty"`
	RevisionFP      string                            `json:"revision_fp,omitempty"`
	Strategy        string                            `json:"strategy,omitempty"`
	Stage           RepositoryUnderstandingStage      `json:"stage"`
	TreeStatus      RepositoryUnderstandingTreeStatus `json:"tree_status"`
	CachedNodes     int                               `json:"cached_nodes"`
	TotalNodes      int                               `json:"total_nodes"`
	ModelUsed       string                            `json:"model_used,omitempty"`
	Metadata        string                            `json:"metadata,omitempty"`
	ErrorCode       string                            `json:"error_code,omitempty"`
	ErrorMessage    string                            `json:"error_message,omitempty"`
	Progress        float64                           `json:"progress"`
	ProgressPhase   string                            `json:"progress_phase,omitempty"`
	ProgressMessage string                            `json:"progress_message,omitempty"`
	CreatedAt       time.Time                         `json:"created_at"`
	UpdatedAt       time.Time                         `json:"updated_at"`
}

// Section is an ordered component of a knowledge artifact.
type Section struct {
	ID               string          `json:"id"`
	ArtifactID       string          `json:"artifact_id"`
	SectionKey       string          `json:"section_key,omitempty"`
	Title            string          `json:"title"`
	Content          string          `json:"content"`
	Summary          string          `json:"summary,omitempty"`
	Metadata         string          `json:"metadata,omitempty"`
	Confidence       ConfidenceLevel `json:"confidence"`
	Inferred         bool            `json:"inferred"`
	OrderIndex       int             `json:"order_index"`
	RefinementStatus string          `json:"refinement_status,omitempty"`
	Evidence         []Evidence      `json:"evidence,omitempty"`
}

type RefinementStatus string

const (
	RefinementQueued    RefinementStatus = "queued"
	RefinementRunning   RefinementStatus = "running"
	RefinementCompleted RefinementStatus = "completed"
	RefinementFailed    RefinementStatus = "failed"
)

// RefinementUnit is a durable unit of artifact-improvement work. The first
// implementation tracks section-level cliff-notes refinement so retries and
// background deepening can resume selectively instead of restarting blindly.
type RefinementUnit struct {
	ID                 string           `json:"id"`
	ArtifactID         string           `json:"artifact_id"`
	SectionKey         string           `json:"section_key"`
	SectionTitle       string           `json:"section_title"`
	RefinementType     string           `json:"refinement_type"`
	Status             RefinementStatus `json:"status"`
	AttemptCount       int              `json:"attempt_count"`
	UnderstandingID    string           `json:"understanding_id,omitempty"`
	EvidenceRevisionFP string           `json:"evidence_revision_fp,omitempty"`
	RendererVersion    string           `json:"renderer_version,omitempty"`
	LastError          string           `json:"last_error,omitempty"`
	Metadata           string           `json:"metadata,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

// Evidence is a traceable reference from a section back to a source artifact.
type Evidence struct {
	ID         string             `json:"id"`
	SectionID  string             `json:"section_id"`
	SourceType EvidenceSourceType `json:"source_type"`
	SourceID   string             `json:"source_id"`
	FilePath   string             `json:"file_path,omitempty"`
	LineStart  int                `json:"line_start,omitempty"`
	LineEnd    int                `json:"line_end,omitempty"`
	Rationale  string             `json:"rationale,omitempty"`
	Metadata   map[string]string  `json:"metadata,omitempty"`
}

// ArtifactDependencyType describes the relationship between an artifact and
// one of the durable assets it depends on.
type ArtifactDependencyType string

const (
	DependencyRepositoryUnderstanding ArtifactDependencyType = "repository_understanding"
)

// ArtifactDependency links an artifact to a prerequisite artifact/state that
// it was derived from.
type ArtifactDependency struct {
	ID               string                 `json:"id"`
	ArtifactID       string                 `json:"artifact_id"`
	DependencyType   ArtifactDependencyType `json:"dependency_type"`
	TargetID         string                 `json:"target_id"`
	TargetRevisionFP string                 `json:"target_revision_fp,omitempty"`
	Metadata         string                 `json:"metadata,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
}

func normalizeScopePath(scopeType ScopeType, scopePath string) string {
	scopePath = strings.TrimSpace(scopePath)
	scopePath = strings.TrimPrefix(scopePath, "/")
	scopePath = strings.TrimSuffix(scopePath, "/")
	if scopeType == ScopeRepository {
		return ""
	}
	return scopePath
}

func splitSymbolScopePath(scopePath string) (string, string) {
	// Support both # and : as file/symbol separators.
	// Try # first (canonical), then fall back to : only if the part
	// after : doesn't look like a file path (i.e., doesn't contain /
	// or end with a known extension).
	filePath, symbolName, found := strings.Cut(scopePath, "#")
	if found {
		return filePath, symbolName
	}
	filePath, symbolName, found = strings.Cut(scopePath, ":")
	if found && !strings.Contains(symbolName, "/") && !strings.Contains(symbolName, ".") {
		return filePath, symbolName
	}
	return scopePath, ""
}

func modulePathForFile(filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return ""
	}
	idx := strings.LastIndex(filePath, "/")
	if idx < 0 {
		return ""
	}
	return filePath[:idx]
}
