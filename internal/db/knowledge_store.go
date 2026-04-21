// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/knowledge"
)

// Verify at compile time that *SurrealStore satisfies knowledge.KnowledgeStore.
var _ knowledge.KnowledgeStore = (*SurrealStore)(nil)

// ---------------------------------------------------------------------------
// SurrealDB DTO types for knowledge tables
// ---------------------------------------------------------------------------

type surrealKnowledgeArtifact struct {
	ID                      *models.RecordID `json:"id,omitempty"`
	RepoID                  string           `json:"repo_id"`
	Type                    string           `json:"type"`
	Audience                string           `json:"audience"`
	Depth                   string           `json:"depth"`
	ScopeType               string           `json:"scope_type"`
	ScopeKey                string           `json:"scope_key"`
	ScopePath               string           `json:"scope_path"`
	ScopeSymbolName         string           `json:"scope_symbol_name"`
	Status                  string           `json:"status"`
	Progress                float64          `json:"progress"`
	ProgressPhase           string           `json:"progress_phase"`
	ProgressMessage         string           `json:"progress_message"`
	ErrorCode               string           `json:"error_code"`
	ErrorMessage            string           `json:"error_message"`
	SourceRevisionCommit    string           `json:"source_revision_commit"`
	SourceRevisionBranch    string           `json:"source_revision_branch"`
	SourceRevisionContentFP string           `json:"source_revision_content_fp"`
	SourceRevisionDocsFP    string           `json:"source_revision_docs_fp"`
	UnderstandingID         string           `json:"understanding_id"`
	UnderstandingRevisionFP string           `json:"understanding_revision_fp"`
	RendererVersion         string           `json:"renderer_version"`
	GenerationMode          string           `json:"generation_mode"`
	Stale                   bool             `json:"stale"`
	StaleReasonJSON         string           `json:"stale_reason_json"`
	StaleReportID           string           `json:"stale_report_id"`
	GeneratedAt             surrealTime      `json:"generated_at"`
	CreatedAt               surrealTime      `json:"created_at"`
	UpdatedAt               surrealTime      `json:"updated_at"`
}

func (r *surrealKnowledgeArtifact) toArtifact() *knowledge.Artifact {
	return &knowledge.Artifact{
		ID:           recordIDString(r.ID),
		RepositoryID: r.RepoID,
		Type:         knowledge.ArtifactType(r.Type),
		Audience:     knowledge.Audience(r.Audience),
		Depth:        knowledge.Depth(r.Depth),
		Scope: knowledge.ArtifactScope{
			ScopeType:  knowledge.ScopeType(r.ScopeType),
			ScopePath:  r.ScopePath,
			SymbolName: r.ScopeSymbolName,
		}.NormalizePtr(),
		Status:          knowledge.ArtifactStatus(r.Status),
		Progress:        r.Progress,
		ProgressPhase:   r.ProgressPhase,
		ProgressMessage: r.ProgressMessage,
		ErrorCode:       r.ErrorCode,
		ErrorMessage:    r.ErrorMessage,
		Stale:           r.Stale,
		SourceRevision: knowledge.SourceRevision{
			CommitSHA:          r.SourceRevisionCommit,
			Branch:             r.SourceRevisionBranch,
			ContentFingerprint: r.SourceRevisionContentFP,
			DocsFingerprint:    r.SourceRevisionDocsFP,
		},
		UnderstandingID:         r.UnderstandingID,
		UnderstandingRevisionFP: r.UnderstandingRevisionFP,
		RendererVersion:         r.RendererVersion,
		GenerationMode:          knowledge.GenerationMode(r.GenerationMode),
		StaleReasonJSON:         r.StaleReasonJSON,
		StaleReportID:           r.StaleReportID,
		GeneratedAt:             r.GeneratedAt.Time,
		CreatedAt:               r.CreatedAt.Time,
		UpdatedAt:               r.UpdatedAt.Time,
	}
}

type surrealRepositoryUnderstanding struct {
	ID           *models.RecordID `json:"id,omitempty"`
	RepoID       string           `json:"repo_id"`
	ScopeType    string           `json:"scope_type"`
	ScopeKey     string           `json:"scope_key"`
	ScopePath    string           `json:"scope_path"`
	CorpusID     string           `json:"corpus_id"`
	RevisionFP   string           `json:"revision_fp"`
	Strategy     string           `json:"strategy"`
	Stage        string           `json:"stage"`
	TreeStatus   string           `json:"tree_status"`
	CachedNodes  int              `json:"cached_nodes"`
	TotalNodes   int              `json:"total_nodes"`
	ModelUsed    string           `json:"model_used"`
	Metadata     string           `json:"metadata"`
	ErrorCode    string           `json:"error_code"`
	ErrorMessage string           `json:"error_message"`
	CreatedAt    surrealTime      `json:"created_at"`
	UpdatedAt    surrealTime      `json:"updated_at"`
}

func (r *surrealRepositoryUnderstanding) toRepositoryUnderstanding() *knowledge.RepositoryUnderstanding {
	scope := knowledge.ArtifactScope{
		ScopeType: knowledge.ScopeType(r.ScopeType),
		ScopePath: r.ScopePath,
	}.Normalize()
	return &knowledge.RepositoryUnderstanding{
		ID:           recordIDString(r.ID),
		RepositoryID: r.RepoID,
		Scope:        &scope,
		CorpusID:     r.CorpusID,
		RevisionFP:   r.RevisionFP,
		Strategy:     r.Strategy,
		Stage:        knowledge.RepositoryUnderstandingStage(r.Stage),
		TreeStatus:   knowledge.RepositoryUnderstandingTreeStatus(r.TreeStatus),
		CachedNodes:  r.CachedNodes,
		TotalNodes:   r.TotalNodes,
		ModelUsed:    r.ModelUsed,
		Metadata:     r.Metadata,
		ErrorCode:    r.ErrorCode,
		ErrorMessage: r.ErrorMessage,
		CreatedAt:    r.CreatedAt.Time,
		UpdatedAt:    r.UpdatedAt.Time,
	}
}

type surrealKnowledgeSection struct {
	ID               *models.RecordID `json:"id,omitempty"`
	ArtifactID       string           `json:"artifact_id"`
	SectionKey       string           `json:"section_key"`
	Title            string           `json:"title"`
	Content          string           `json:"content"`
	Summary          string           `json:"summary"`
	Metadata         string           `json:"metadata"`
	Confidence       string           `json:"confidence"`
	Inferred         bool             `json:"inferred"`
	OrderIndex       int              `json:"order_index"`
	RefinementStatus string           `json:"refinement_status"`
}

func (r *surrealKnowledgeSection) toSection() knowledge.Section {
	return knowledge.Section{
		ID:               recordIDString(r.ID),
		ArtifactID:       r.ArtifactID,
		SectionKey:       r.SectionKey,
		Title:            r.Title,
		Content:          r.Content,
		Summary:          r.Summary,
		Metadata:         r.Metadata,
		Confidence:       knowledge.ConfidenceLevel(r.Confidence),
		Inferred:         r.Inferred,
		OrderIndex:       r.OrderIndex,
		RefinementStatus: r.RefinementStatus,
	}
}

type surrealArtifactDependency struct {
	ID               *models.RecordID `json:"id,omitempty"`
	ArtifactID       string           `json:"artifact_id"`
	DependencyType   string           `json:"dependency_type"`
	TargetID         string           `json:"target_id"`
	TargetRevisionFP string           `json:"target_revision_fp"`
	Metadata         string           `json:"metadata"`
	CreatedAt        surrealTime      `json:"created_at"`
}

func (r *surrealArtifactDependency) toDependency() knowledge.ArtifactDependency {
	return knowledge.ArtifactDependency{
		ID:               recordIDString(r.ID),
		ArtifactID:       r.ArtifactID,
		DependencyType:   knowledge.ArtifactDependencyType(r.DependencyType),
		TargetID:         r.TargetID,
		TargetRevisionFP: r.TargetRevisionFP,
		Metadata:         r.Metadata,
		CreatedAt:        r.CreatedAt.Time,
	}
}

type surrealRefinementUnit struct {
	ID                 *models.RecordID `json:"id,omitempty"`
	ArtifactID         string           `json:"artifact_id"`
	SectionKey         string           `json:"section_key"`
	SectionTitle       string           `json:"section_title"`
	RefinementType     string           `json:"refinement_type"`
	Status             string           `json:"status"`
	AttemptCount       int              `json:"attempt_count"`
	UnderstandingID    string           `json:"understanding_id"`
	EvidenceRevisionFP string           `json:"evidence_revision_fp"`
	RendererVersion    string           `json:"renderer_version"`
	LastError          string           `json:"last_error"`
	Metadata           string           `json:"metadata"`
	CreatedAt          surrealTime      `json:"created_at"`
	UpdatedAt          surrealTime      `json:"updated_at"`
}

func (r *surrealRefinementUnit) toRefinementUnit() knowledge.RefinementUnit {
	return knowledge.RefinementUnit{
		ID:                 recordIDString(r.ID),
		ArtifactID:         r.ArtifactID,
		SectionKey:         r.SectionKey,
		SectionTitle:       r.SectionTitle,
		RefinementType:     r.RefinementType,
		Status:             knowledge.RefinementStatus(r.Status),
		AttemptCount:       r.AttemptCount,
		UnderstandingID:    r.UnderstandingID,
		EvidenceRevisionFP: r.EvidenceRevisionFP,
		RendererVersion:    r.RendererVersion,
		LastError:          r.LastError,
		Metadata:           r.Metadata,
		CreatedAt:          r.CreatedAt.Time,
		UpdatedAt:          r.UpdatedAt.Time,
	}
}

type surrealKnowledgeEvidence struct {
	ID         *models.RecordID `json:"id,omitempty"`
	SectionID  string           `json:"section_id"`
	SourceType string           `json:"source_type"`
	SourceID   string           `json:"source_id"`
	FilePath   string           `json:"file_path"`
	LineStart  int              `json:"line_start"`
	LineEnd    int              `json:"line_end"`
	Rationale  string           `json:"rationale"`
	Metadata   string           `json:"metadata"` // JSON string
}

func (r *surrealKnowledgeEvidence) toEvidence() knowledge.Evidence {
	ev := knowledge.Evidence{
		ID:         recordIDString(r.ID),
		SectionID:  r.SectionID,
		SourceType: knowledge.EvidenceSourceType(r.SourceType),
		SourceID:   r.SourceID,
		FilePath:   r.FilePath,
		LineStart:  r.LineStart,
		LineEnd:    r.LineEnd,
		Rationale:  r.Rationale,
	}
	if r.Metadata != "" {
		_ = json.Unmarshal([]byte(r.Metadata), &ev.Metadata)
	}
	return ev
}

// ---------------------------------------------------------------------------
// Knowledge artifact operations
// ---------------------------------------------------------------------------

func (s *SurrealStore) StoreKnowledgeArtifact(artifact *knowledge.Artifact) (*knowledge.Artifact, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	id := artifact.ID
	if id == "" {
		id = uuid.New().String()
	}
	scope := knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}
	if artifact.Scope != nil {
		scope = artifact.Scope.Normalize()
	}

	// Build SQL dynamically — only include generated_at when set, because
	// SurrealDB's option<datetime> rejects both NULL and Go zero-time;
	// it expects NONE (field absent) or a valid datetime.
	metadataSQL := `CREATE ca_knowledge_artifact SET
		id = type::thing('ca_knowledge_artifact', $id),
		repo_id = $repo_id,
		type = $type,
		audience = $audience,
		depth = $depth,
		scope_type = $scope_type,
		scope_key = $scope_key,
		scope_path = $scope_path,
		scope_symbol_name = $scope_symbol_name,
		status = $status,
		source_revision_commit = $src_commit,
		source_revision_branch = $src_branch,
		source_revision_content_fp = $src_content_fp,
		source_revision_docs_fp = $src_docs_fp,
		understanding_id = $understanding_id,
		understanding_revision_fp = $understanding_revision_fp,
		renderer_version = $renderer_version,
		generation_mode = $generation_mode,
		stale = $stale,
		progress = $progress,
		created_at = time::now(),
		updated_at = time::now()`

	vars := map[string]any{
		"id":                        id,
		"repo_id":                   artifact.RepositoryID,
		"type":                      string(artifact.Type),
		"audience":                  string(artifact.Audience),
		"depth":                     string(artifact.Depth),
		"scope_type":                string(scope.ScopeType),
		"scope_key":                 scope.ScopeKey(),
		"scope_path":                scope.ScopePath,
		"scope_symbol_name":         scope.SymbolName,
		"status":                    string(artifact.Status),
		"progress":                  artifact.Progress,
		"src_commit":                artifact.SourceRevision.CommitSHA,
		"src_branch":                artifact.SourceRevision.Branch,
		"src_content_fp":            artifact.SourceRevision.ContentFingerprint,
		"src_docs_fp":               artifact.SourceRevision.DocsFingerprint,
		"understanding_id":          artifact.UnderstandingID,
		"understanding_revision_fp": artifact.UnderstandingRevisionFP,
		"renderer_version":          artifact.RendererVersion,
		"generation_mode":           string(artifact.GenerationMode),
		"stale":                     artifact.Stale,
	}

	if !artifact.GeneratedAt.IsZero() {
		metadataSQL += `, generated_at = $generated_at`
		vars["generated_at"] = artifact.GeneratedAt.Format(time.RFC3339Nano)
	}

	// CREATE returns an array — use untyped query then SELECT back.
	_, err := surrealdb.Query[interface{}](ctx(), db, metadataSQL, vars)
	if err != nil {
		return nil, fmt.Errorf("store knowledge artifact: %w", err)
	}

	return s.GetKnowledgeArtifact(id), nil
}

func (s *SurrealStore) ClaimArtifact(key knowledge.ArtifactKey, sourceRevision knowledge.SourceRevision) (*knowledge.Artifact, bool, error) {
	return s.ClaimArtifactWithMode(key, sourceRevision, "")
}

func (s *SurrealStore) ClaimArtifactWithMode(key knowledge.ArtifactKey, sourceRevision knowledge.SourceRevision, mode knowledge.GenerationMode) (*knowledge.Artifact, bool, error) {
	db := s.client.DB()
	if db == nil {
		return nil, false, fmt.Errorf("database not connected")
	}

	key = key.Normalized()
	if err := key.Validate(); err != nil {
		return nil, false, err
	}
	normalizedMode := knowledge.NormalizeGenerationMode(mode)

	id := uuid.New().String()
	scope := key.Scope.Normalize()
	_, err := surrealdb.Query[interface{}](ctx(), db,
		`CREATE ca_knowledge_artifact SET
			id = type::thing('ca_knowledge_artifact', $id),
			repo_id = $repo_id,
			type = $type,
			audience = $audience,
			depth = $depth,
			scope_type = $scope_type,
			scope_key = $scope_key,
			scope_path = $scope_path,
			scope_symbol_name = $scope_symbol_name,
			status = "generating",
			progress = 0,
			error_code = '',
			error_message = '',
			source_revision_commit = $src_commit,
			source_revision_branch = $src_branch,
			source_revision_content_fp = $src_content_fp,
			source_revision_docs_fp = $src_docs_fp,
			understanding_id = '',
			understanding_revision_fp = '',
			renderer_version = $renderer_version,
			generation_mode = $generation_mode,
			stale = false,
			created_at = time::now(),
			updated_at = time::now()`,
		map[string]any{
			"id":                id,
			"repo_id":           key.RepositoryID,
			"type":              string(key.Type),
			"audience":          string(key.Audience),
			"depth":             string(key.Depth),
			"scope_type":        string(scope.ScopeType),
			"scope_key":         key.ScopeKey(),
			"scope_path":        scope.ScopePath,
			"scope_symbol_name": scope.SymbolName,
			"src_commit":        sourceRevision.CommitSHA,
			"src_branch":        sourceRevision.Branch,
			"src_content_fp":    sourceRevision.ContentFingerprint,
			"src_docs_fp":       sourceRevision.DocsFingerprint,
			"renderer_version":  knowledge.RendererVersionForArtifact(key.Type),
			"generation_mode":   string(normalizedMode),
		})
	if err != nil {
		existing := s.GetArtifactByKeyAndMode(key, normalizedMode)
		if existing != nil {
			return existing, false, nil
		}
		return nil, false, fmt.Errorf("claim knowledge artifact: %w", err)
	}
	return s.GetKnowledgeArtifact(id), true, nil
}

func (s *SurrealStore) GetKnowledgeArtifact(id string) *knowledge.Artifact {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	row, err := queryOne[[]surrealKnowledgeArtifact](ctx(), db,
		"SELECT * FROM type::thing('ca_knowledge_artifact', $id) WHERE deleted_at IS NONE",
		map[string]any{"id": id})
	if err != nil || len(row) == 0 {
		return nil
	}

	a := row[0].toArtifact()
	a.Sections = s.loadSections(a.ID)
	return a
}

func (s *SurrealStore) GetArtifactByKey(key knowledge.ArtifactKey) *knowledge.Artifact {
	return s.GetArtifactByKeyAndMode(key, "")
}

func (s *SurrealStore) GetArtifactByKeyAndMode(key knowledge.ArtifactKey, mode knowledge.GenerationMode) *knowledge.Artifact {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	key = key.Normalized()
	query := `SELECT * FROM ca_knowledge_artifact
		 WHERE repo_id = $repo_id
		   AND type = $type
		   AND audience = $audience
		   AND depth = $depth
		   AND scope_key = $scope_key
		   AND deleted_at IS NONE`
	vars := map[string]any{
		"repo_id":   key.RepositoryID,
		"type":      string(key.Type),
		"audience":  string(key.Audience),
		"depth":     string(key.Depth),
		"scope_key": key.ScopeKey(),
	}
	if mode != "" {
		query += `
		   AND generation_mode = $generation_mode`
		vars["generation_mode"] = string(knowledge.NormalizeGenerationMode(mode))
	}
	query += `
		 ORDER BY created_at DESC
		 LIMIT 1`
	rows, err := queryOne[[]surrealKnowledgeArtifact](ctx(), db, query, vars)
	if err != nil || len(rows) == 0 {
		return nil
	}
	a := rows[0].toArtifact()
	a.Sections = s.loadSections(a.ID)
	return a
}

func (s *SurrealStore) GetKnowledgeArtifacts(repoID string) []*knowledge.Artifact {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealKnowledgeArtifact](ctx(), db,
		"SELECT * FROM ca_knowledge_artifact WHERE repo_id = $repo_id AND deleted_at IS NONE ORDER BY created_at DESC",
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil
	}

	results := make([]*knowledge.Artifact, 0, len(rows))
	for _, r := range rows {
		a := r.toArtifact()
		a.Sections = s.loadSections(a.ID)
		results = append(results, a)
	}
	return results
}

func (s *SurrealStore) UpdateKnowledgeArtifactStatus(id string, status knowledge.ArtifactStatus) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	sql := `UPDATE type::thing('ca_knowledge_artifact', $id) SET status = $status, error_code = '', error_message = '', updated_at = time::now()`
	vars := map[string]any{"id": id, "status": string(status)}

	if status == knowledge.StatusReady {
		sql = `UPDATE type::thing('ca_knowledge_artifact', $id) SET status = $status, progress = 1.0, error_code = '', error_message = '', generated_at = time::now(), updated_at = time::now()`
	}

	_, err := queryOne[interface{}](ctx(), db, sql, vars)
	return err
}

func (s *SurrealStore) SetArtifactFailed(id string, code string, message string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_knowledge_artifact', $id)
		 SET status = $status,
		     progress = 0,
		     progress_phase = '',
		     progress_message = '',
		     error_code = $error_code,
		     error_message = $error_message,
		     updated_at = time::now()`,
		map[string]any{
			"id":            id,
			"status":        string(knowledge.StatusFailed),
			"error_code":    code,
			"error_message": message,
		})
	return err
}

func (s *SurrealStore) UpdateKnowledgeArtifactProgress(id string, progress float64) error {
	return s.UpdateKnowledgeArtifactProgressWithPhase(id, progress, "", "")
}

func (s *SurrealStore) UpdateKnowledgeArtifactProgressWithPhase(id string, progress float64, phase, message string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	sql := `UPDATE type::thing('ca_knowledge_artifact', $id)
		SET progress = $progress, updated_at = time::now()`
	vars := map[string]any{"id": id, "progress": progress}
	if phase != "" {
		sql += `, progress_phase = $phase`
		vars["phase"] = phase
	}
	if message != "" {
		sql += `, progress_message = $message`
		vars["message"] = message
	}
	_, err := queryOne[interface{}](ctx(), db, sql, vars)
	return err
}

func (s *SurrealStore) MarkKnowledgeArtifactStale(id string, stale bool) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	if stale {
		_, err := queryOne[interface{}](ctx(), db,
			`UPDATE type::thing('ca_knowledge_artifact', $id)
			 SET stale = $stale, updated_at = time::now()`,
			map[string]any{"id": id, "stale": stale})
		return err
	}
	// Clearing stale drops the reason as well so the UI doesn't show a stale
	// explanation for a fresh artifact.
	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_knowledge_artifact', $id)
		 SET stale = false,
		     stale_reason_json = '',
		     stale_report_id = '',
		     updated_at = time::now()`,
		map[string]any{"id": id})
	return err
}

func (s *SurrealStore) MarkKnowledgeArtifactStaleWithReason(id string, reasonJSON string, reportID string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_knowledge_artifact', $id)
		 SET stale = true,
		     stale_reason_json = $reason_json,
		     stale_report_id = $report_id,
		     updated_at = time::now()`,
		map[string]any{
			"id":          id,
			"reason_json": reasonJSON,
			"report_id":   reportID,
		})
	return err
}

// GetArtifactsForSources returns artifacts whose evidence references any of
// the given (source_type, source_id) pairs. Implemented as a two-step lookup:
// resolve section_ids from evidence, then artifacts from sections.
func (s *SurrealStore) GetArtifactsForSources(repoID string, sources []knowledge.SourceRef) []*knowledge.Artifact {
	db := s.client.DB()
	if db == nil || len(sources) == 0 {
		return nil
	}

	// Build parallel arrays for the IN-tuple lookup. SurrealDB doesn't
	// support compound IN syntax directly, so we union per source type.
	bySourceType := make(map[string][]string)
	for _, ref := range sources {
		if ref.SourceID == "" {
			continue
		}
		st := string(ref.SourceType)
		bySourceType[st] = append(bySourceType[st], ref.SourceID)
	}
	if len(bySourceType) == 0 {
		return nil
	}

	sectionIDSet := make(map[string]struct{})
	type evRow struct {
		SectionID string `json:"section_id"`
	}
	for sourceType, ids := range bySourceType {
		rows, err := queryOne[[]evRow](ctx(), db,
			`SELECT section_id FROM ca_knowledge_evidence
			 WHERE source_type = $st AND source_id IN $ids`,
			map[string]any{"st": sourceType, "ids": ids})
		if err != nil {
			continue
		}
		for _, r := range rows {
			if r.SectionID == "" {
				continue
			}
			sectionIDSet[r.SectionID] = struct{}{}
		}
	}
	if len(sectionIDSet) == 0 {
		return nil
	}

	return s.artifactsFromSectionIDs(repoID, sectionIDSet)
}

// GetArtifactsForFiles returns artifacts whose evidence references any of
// the given file paths.
func (s *SurrealStore) GetArtifactsForFiles(repoID string, filePaths []string) []*knowledge.Artifact {
	db := s.client.DB()
	if db == nil || len(filePaths) == 0 {
		return nil
	}

	nonEmpty := make([]string, 0, len(filePaths))
	for _, p := range filePaths {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) == 0 {
		return nil
	}

	type evRow struct {
		SectionID string `json:"section_id"`
	}
	rows, err := queryOne[[]evRow](ctx(), db,
		`SELECT section_id FROM ca_knowledge_evidence
		 WHERE file_path IN $paths AND file_path != ''`,
		map[string]any{"paths": nonEmpty})
	if err != nil {
		return nil
	}
	sectionIDSet := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if r.SectionID == "" {
			continue
		}
		sectionIDSet[r.SectionID] = struct{}{}
	}
	if len(sectionIDSet) == 0 {
		return nil
	}

	return s.artifactsFromSectionIDs(repoID, sectionIDSet)
}

// artifactsFromSectionIDs resolves a set of section IDs to their parent
// artifacts, scoped to the given repo and deduped.
func (s *SurrealStore) artifactsFromSectionIDs(repoID string, sectionIDSet map[string]struct{}) []*knowledge.Artifact {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	sectionIDs := make([]string, 0, len(sectionIDSet))
	for id := range sectionIDSet {
		sectionIDs = append(sectionIDs, id)
	}

	type secRow struct {
		ArtifactID string `json:"artifact_id"`
	}
	secRows, err := queryOne[[]secRow](ctx(), db,
		`SELECT artifact_id FROM ca_knowledge_section
		 WHERE id IN $ids.map(|$v| type::thing('ca_knowledge_section', $v))`,
		map[string]any{"ids": sectionIDs})
	if err != nil {
		return nil
	}
	artifactIDSet := make(map[string]struct{}, len(secRows))
	for _, sr := range secRows {
		if sr.ArtifactID == "" {
			continue
		}
		artifactIDSet[sr.ArtifactID] = struct{}{}
	}
	if len(artifactIDSet) == 0 {
		return nil
	}

	artifactIDs := make([]string, 0, len(artifactIDSet))
	for id := range artifactIDSet {
		artifactIDs = append(artifactIDs, id)
	}
	rows, err := queryOne[[]surrealKnowledgeArtifact](ctx(), db,
		`SELECT * FROM ca_knowledge_artifact
		 WHERE repo_id = $repo_id
		   AND deleted_at IS NONE
		   AND id IN $ids.map(|$v| type::thing('ca_knowledge_artifact', $v))`,
		map[string]any{"repo_id": repoID, "ids": artifactIDs})
	if err != nil {
		return nil
	}
	out := make([]*knowledge.Artifact, 0, len(rows))
	for _, r := range rows {
		a := r.toArtifact()
		a.Sections = s.loadSections(a.ID)
		out = append(out, a)
	}
	return out
}

func (s *SurrealStore) DeleteKnowledgeArtifact(id string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	// Delete evidence for all sections of this artifact.
	sections := s.GetKnowledgeSections(id)
	for _, sec := range sections {
		_, _ = queryOne[interface{}](ctx(), db,
			"DELETE ca_knowledge_evidence WHERE section_id = $section_id",
			map[string]any{"section_id": sec.ID})
	}

	// Delete sections.
	_, _ = queryOne[interface{}](ctx(), db,
		"DELETE ca_knowledge_section WHERE artifact_id = $artifact_id",
		map[string]any{"artifact_id": id})

	// Delete artifact.
	_, err := queryOne[interface{}](ctx(), db,
		"DELETE type::thing('ca_knowledge_artifact', $id)",
		map[string]any{"id": id})
	return err
}

func (s *SurrealStore) SupersedeArtifact(id string, sections []knowledge.Section) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	if s.GetKnowledgeArtifact(id) == nil {
		return fmt.Errorf("artifact %s not found", id)
	}
	for _, sec := range s.GetKnowledgeSections(id) {
		_, _ = queryOne[interface{}](ctx(), db,
			"DELETE ca_knowledge_evidence WHERE section_id = $section_id",
			map[string]any{"section_id": sec.ID})
	}
	_, _ = queryOne[interface{}](ctx(), db,
		"DELETE ca_knowledge_section WHERE artifact_id = $artifact_id",
		map[string]any{"artifact_id": id})
	if err := s.StoreKnowledgeSections(id, sections); err != nil {
		return err
	}
	for _, sec := range s.GetKnowledgeSections(id) {
		for _, incoming := range sections {
			if incoming.Title != sec.Title || incoming.Content != sec.Content {
				continue
			}
			if len(incoming.Evidence) > 0 {
				if err := s.StoreKnowledgeEvidence(sec.ID, incoming.Evidence); err != nil {
					return err
				}
			}
			break
		}
	}
	return s.UpdateKnowledgeArtifactStatus(id, knowledge.StatusReady)
}

// ---------------------------------------------------------------------------
// Knowledge section operations
// ---------------------------------------------------------------------------

func (s *SurrealStore) StoreKnowledgeSections(artifactID string, sections []knowledge.Section) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	// Remove old sections first.
	_, _ = queryOne[interface{}](ctx(), db,
		"DELETE ca_knowledge_section WHERE artifact_id = $artifact_id",
		map[string]any{"artifact_id": artifactID})

	for i, sec := range sections {
		secID := sec.ID
		if secID == "" {
			secID = uuid.New().String()
		}
		if sec.SectionKey == "" {
			sec.SectionKey = knowledge.SectionKeyForTitle(sec.Title)
		}
		if sec.RefinementStatus == "" {
			sec.RefinementStatus = "light"
		}

		_, err := surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_knowledge_section SET
				id = type::thing('ca_knowledge_section', $id),
				artifact_id = $artifact_id,
				section_key = $section_key,
				title = $title,
				content = $content,
				summary = $summary,
				metadata = $metadata,
				confidence = $confidence,
				inferred = $inferred,
				order_index = $order_index,
				refinement_status = $refinement_status`,
			map[string]any{
				"id":                secID,
				"artifact_id":       artifactID,
				"section_key":       sec.SectionKey,
				"title":             sec.Title,
				"content":           sec.Content,
				"summary":           sec.Summary,
				"metadata":          sec.Metadata,
				"confidence":        string(sec.Confidence),
				"inferred":          sec.Inferred,
				"order_index":       i,
				"refinement_status": sec.RefinementStatus,
			})
		if err != nil {
			return fmt.Errorf("store knowledge section %d: %w", i, err)
		}
	}
	return nil
}

func (s *SurrealStore) GetKnowledgeSections(artifactID string) []knowledge.Section {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealKnowledgeSection](ctx(), db,
		"SELECT * FROM ca_knowledge_section WHERE artifact_id = $artifact_id AND deleted_at IS NONE ORDER BY order_index ASC",
		map[string]any{"artifact_id": artifactID})
	if err != nil {
		return nil
	}

	sections := make([]knowledge.Section, 0, len(rows))
	for _, r := range rows {
		sec := r.toSection()
		sec.Evidence = s.GetKnowledgeEvidence(sec.ID)
		sections = append(sections, sec)
	}
	return sections
}

func (s *SurrealStore) StoreRefinementUnits(artifactID string, units []knowledge.RefinementUnit) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	if s.GetKnowledgeArtifact(artifactID) == nil {
		return fmt.Errorf("artifact %s not found", artifactID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i, unit := range units {
		unitID := unit.ID
		if unitID == "" {
			unitID = uuid.New().String()
		}
		if _, err := surrealdb.Query[interface{}](ctx(), db,
			`UPSERT ca_knowledge_refinement SET
				id = type::thing('ca_knowledge_refinement', $id),
				artifact_id = $artifact_id,
				section_key = $section_key,
				section_title = $section_title,
				refinement_type = $refinement_type,
				status = $status,
				attempt_count = $attempt_count,
				understanding_id = $understanding_id,
				evidence_revision_fp = $evidence_revision_fp,
				renderer_version = $renderer_version,
				last_error = $last_error,
				metadata = $metadata,
				created_at = IF created_at = NONE OR created_at = NULL THEN <datetime>$now ELSE created_at END,
				updated_at = <datetime>$now
			WHERE artifact_id = $artifact_id
			  AND section_key = $section_key
			  AND refinement_type = $refinement_type`,
			map[string]any{
				"id":                   unitID,
				"artifact_id":          artifactID,
				"section_key":          unit.SectionKey,
				"section_title":        unit.SectionTitle,
				"refinement_type":      unit.RefinementType,
				"status":               string(unit.Status),
				"attempt_count":        unit.AttemptCount,
				"understanding_id":     unit.UnderstandingID,
				"evidence_revision_fp": unit.EvidenceRevisionFP,
				"renderer_version":     unit.RendererVersion,
				"last_error":           unit.LastError,
				"metadata":             unit.Metadata,
				"now":                  now,
			}); err != nil {
			return fmt.Errorf("store refinement unit %d: %w", i, err)
		}
	}
	return nil
}

func (s *SurrealStore) GetRefinementUnits(artifactID string) []knowledge.RefinementUnit {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	rows, err := queryOne[[]surrealRefinementUnit](ctx(), db,
		"SELECT * FROM ca_knowledge_refinement WHERE artifact_id = $artifact_id AND deleted_at IS NONE ORDER BY section_title ASC, refinement_type ASC",
		map[string]any{"artifact_id": artifactID})
	if err != nil {
		return nil
	}
	units := make([]knowledge.RefinementUnit, 0, len(rows))
	for _, row := range rows {
		units = append(units, row.toRefinementUnit())
	}
	return units
}

func (s *SurrealStore) StoreArtifactDependencies(artifactID string, dependencies []knowledge.ArtifactDependency) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	_, _ = queryOne[interface{}](ctx(), db,
		"DELETE ca_knowledge_dependency WHERE artifact_id = $artifact_id",
		map[string]any{"artifact_id": artifactID})

	for i, dep := range dependencies {
		depID := dep.ID
		if depID == "" {
			depID = uuid.New().String()
		}
		_, err := surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_knowledge_dependency SET
				id = type::thing('ca_knowledge_dependency', $id),
				artifact_id = $artifact_id,
				dependency_type = $dependency_type,
				target_id = $target_id,
				target_revision_fp = $target_revision_fp,
				metadata = $metadata,
				created_at = time::now()`,
			map[string]any{
				"id":                 depID,
				"artifact_id":        artifactID,
				"dependency_type":    string(dep.DependencyType),
				"target_id":          dep.TargetID,
				"target_revision_fp": dep.TargetRevisionFP,
				"metadata":           dep.Metadata,
			})
		if err != nil {
			return fmt.Errorf("store artifact dependency %d: %w", i, err)
		}
	}
	return nil
}

func (s *SurrealStore) GetArtifactDependencies(artifactID string) []knowledge.ArtifactDependency {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	rows, err := queryOne[[]surrealArtifactDependency](ctx(), db,
		"SELECT * FROM ca_knowledge_dependency WHERE artifact_id = $artifact_id AND deleted_at IS NONE ORDER BY created_at ASC",
		map[string]any{"artifact_id": artifactID})
	if err != nil {
		return nil
	}
	deps := make([]knowledge.ArtifactDependency, 0, len(rows))
	for _, row := range rows {
		deps = append(deps, row.toDependency())
	}
	return deps
}

// ---------------------------------------------------------------------------
// Knowledge evidence operations
// ---------------------------------------------------------------------------

func (s *SurrealStore) StoreKnowledgeEvidence(sectionID string, evidence []knowledge.Evidence) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}

	// Remove old evidence first.
	_, _ = queryOne[interface{}](ctx(), db,
		"DELETE ca_knowledge_evidence WHERE section_id = $section_id",
		map[string]any{"section_id": sectionID})

	for i, ev := range evidence {
		// Evidence IDs are persistence-row identifiers, not stable semantic keys.
		// Always mint fresh IDs so supersedes/refinements remain idempotent even when
		// callers pass through evidence copied from an existing artifact.
		evID := uuid.New().String()

		var metadataJSON string
		if len(ev.Metadata) > 0 {
			b, _ := json.Marshal(ev.Metadata)
			metadataJSON = string(b)
		}

		_, err := surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_knowledge_evidence SET
				id = type::thing('ca_knowledge_evidence', $id),
				section_id = $section_id,
				source_type = $source_type,
				source_id = $source_id,
				file_path = $file_path,
				line_start = $line_start,
				line_end = $line_end,
				rationale = $rationale,
				metadata = $metadata`,
			map[string]any{
				"id":          evID,
				"section_id":  sectionID,
				"source_type": string(ev.SourceType),
				"source_id":   ev.SourceID,
				"file_path":   ev.FilePath,
				"line_start":  ev.LineStart,
				"line_end":    ev.LineEnd,
				"rationale":   ev.Rationale,
				"metadata":    metadataJSON,
			})
		if err != nil {
			return fmt.Errorf("store knowledge evidence %d: %w", i, err)
		}
	}
	return nil
}

func (s *SurrealStore) GetKnowledgeEvidence(sectionID string) []knowledge.Evidence {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	rows, err := queryOne[[]surrealKnowledgeEvidence](ctx(), db,
		"SELECT * FROM ca_knowledge_evidence WHERE section_id = $section_id AND deleted_at IS NONE",
		map[string]any{"section_id": sectionID})
	if err != nil {
		return nil
	}

	evidence := make([]knowledge.Evidence, 0, len(rows))
	for _, r := range rows {
		evidence = append(evidence, r.toEvidence())
	}
	return evidence
}

func (s *SurrealStore) StoreRepositoryUnderstanding(u *knowledge.RepositoryUnderstanding) (*knowledge.RepositoryUnderstanding, error) {
	db := s.client.DB()
	if db == nil {
		return nil, fmt.Errorf("database not connected")
	}

	id := u.ID
	if id == "" {
		id = uuid.New().String()
	}
	scope := knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository}
	if u.Scope != nil {
		scope = u.Scope.Normalize()
	}

	sql := `
		LET $existing = (SELECT id, created_at FROM ca_repository_understanding WHERE repo_id = $repo_id AND scope_key = $scope_key LIMIT 1);
		IF array::len($existing) > 0 THEN
			(UPDATE ca_repository_understanding SET
				scope_type = $scope_type,
				scope_path = $scope_path,
				corpus_id = $corpus_id,
				revision_fp = $revision_fp,
				strategy = $strategy,
				stage = $stage,
				tree_status = $tree_status,
				cached_nodes = $cached_nodes,
				total_nodes = $total_nodes,
				model_used = $model_used,
				metadata = $metadata,
				error_code = $error_code,
				error_message = $error_message,
				updated_at = time::now()
			WHERE repo_id = $repo_id AND scope_key = $scope_key)
		ELSE
			(CREATE ca_repository_understanding SET
				id = type::thing('ca_repository_understanding', $id),
				repo_id = $repo_id,
				scope_type = $scope_type,
				scope_key = $scope_key,
				scope_path = $scope_path,
				corpus_id = $corpus_id,
				revision_fp = $revision_fp,
				strategy = $strategy,
				stage = $stage,
				tree_status = $tree_status,
				cached_nodes = $cached_nodes,
				total_nodes = $total_nodes,
				model_used = $model_used,
				metadata = $metadata,
				error_code = $error_code,
				error_message = $error_message,
				created_at = time::now(),
				updated_at = time::now())
		END;
	`
	vars := map[string]any{
		"id":            id,
		"repo_id":       u.RepositoryID,
		"scope_type":    string(scope.ScopeType),
		"scope_key":     scope.ScopeKey(),
		"scope_path":    scope.ScopePath,
		"corpus_id":     u.CorpusID,
		"revision_fp":   u.RevisionFP,
		"strategy":      u.Strategy,
		"stage":         string(u.Stage),
		"tree_status":   string(u.TreeStatus),
		"cached_nodes":  u.CachedNodes,
		"total_nodes":   u.TotalNodes,
		"model_used":    u.ModelUsed,
		"metadata":      u.Metadata,
		"error_code":    u.ErrorCode,
		"error_message": u.ErrorMessage,
	}
	if _, err := surrealdb.Query[interface{}](ctx(), db, sql, vars); err != nil {
		return nil, fmt.Errorf("store repository understanding: %w", err)
	}
	return s.GetRepositoryUnderstanding(u.RepositoryID, scope), nil
}

func (s *SurrealStore) GetRepositoryUnderstanding(repoID string, scope knowledge.ArtifactScope) *knowledge.RepositoryUnderstanding {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	scope = scope.Normalize()
	rows, err := queryOne[[]surrealRepositoryUnderstanding](ctx(), db,
		`SELECT * FROM ca_repository_understanding
		 WHERE repo_id = $repo_id AND scope_key = $scope_key
		 LIMIT 1`,
		map[string]any{
			"repo_id":   repoID,
			"scope_key": scope.ScopeKey(),
		})
	if err != nil || len(rows) == 0 {
		return nil
	}
	return rows[0].toRepositoryUnderstanding()
}

func (s *SurrealStore) GetRepositoryUnderstandings(repoID string) []*knowledge.RepositoryUnderstanding {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	rows, err := queryOne[[]surrealRepositoryUnderstanding](ctx(), db,
		`SELECT * FROM ca_repository_understanding
		 WHERE repo_id = $repo_id
		 ORDER BY updated_at DESC`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil
	}
	out := make([]*knowledge.RepositoryUnderstanding, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toRepositoryUnderstanding())
	}
	return out
}

func (s *SurrealStore) MarkRepositoryUnderstandingNeedsRefresh(repoID string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE ca_repository_understanding
		 SET stage = $stage, updated_at = time::now()
		 WHERE repo_id = $repo_id AND stage INSIDE ['first_pass_ready', 'ready']`,
		map[string]any{
			"repo_id": repoID,
			"stage":   string(knowledge.UnderstandingNeedsRefresh),
		})
	return err
}

func (s *SurrealStore) AttachArtifactUnderstanding(artifactID, understandingID, revisionFP string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_knowledge_artifact', $id)
		 SET understanding_id = $understanding_id,
		     understanding_revision_fp = $understanding_revision_fp,
		     updated_at = time::now()`,
		map[string]any{
			"id":                        artifactID,
			"understanding_id":          understandingID,
			"understanding_revision_fp": revisionFP,
		})
	return err
}

// loadSections returns ordered sections with nested evidence for an artifact.
func (s *SurrealStore) loadSections(artifactID string) []knowledge.Section {
	sections := s.GetKnowledgeSections(artifactID)
	sort.Slice(sections, func(i, j int) bool { return sections[i].OrderIndex < sections[j].OrderIndex })
	return sections
}
