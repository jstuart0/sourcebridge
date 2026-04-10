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
	Stale                   bool             `json:"stale"`
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
		GeneratedAt: r.GeneratedAt.Time,
		CreatedAt:   r.CreatedAt.Time,
		UpdatedAt:   r.UpdatedAt.Time,
	}
}

type surrealKnowledgeSection struct {
	ID         *models.RecordID `json:"id,omitempty"`
	ArtifactID string           `json:"artifact_id"`
	Title      string           `json:"title"`
	Content    string           `json:"content"`
	Summary    string           `json:"summary"`
	Confidence string           `json:"confidence"`
	Inferred   bool             `json:"inferred"`
	OrderIndex int              `json:"order_index"`
}

func (r *surrealKnowledgeSection) toSection() knowledge.Section {
	return knowledge.Section{
		ID:         recordIDString(r.ID),
		ArtifactID: r.ArtifactID,
		Title:      r.Title,
		Content:    r.Content,
		Summary:    r.Summary,
		Confidence: knowledge.ConfidenceLevel(r.Confidence),
		Inferred:   r.Inferred,
		OrderIndex: r.OrderIndex,
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
		stale = $stale,
		progress = $progress,
		created_at = time::now(),
		updated_at = time::now()`

	vars := map[string]any{
		"id":                id,
		"repo_id":           artifact.RepositoryID,
		"type":              string(artifact.Type),
		"audience":          string(artifact.Audience),
		"depth":             string(artifact.Depth),
		"scope_type":        string(scope.ScopeType),
		"scope_key":         scope.ScopeKey(),
		"scope_path":        scope.ScopePath,
		"scope_symbol_name": scope.SymbolName,
		"status":            string(artifact.Status),
		"progress":          artifact.Progress,
		"src_commit":        artifact.SourceRevision.CommitSHA,
		"src_branch":        artifact.SourceRevision.Branch,
		"src_content_fp":    artifact.SourceRevision.ContentFingerprint,
		"src_docs_fp":       artifact.SourceRevision.DocsFingerprint,
		"stale":             artifact.Stale,
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
	db := s.client.DB()
	if db == nil {
		return nil, false, fmt.Errorf("database not connected")
	}

	key = key.Normalized()
	if err := key.Validate(); err != nil {
		return nil, false, err
	}

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
		})
	if err != nil {
		existing := s.GetArtifactByKey(key)
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
		"SELECT * FROM type::thing('ca_knowledge_artifact', $id)",
		map[string]any{"id": id})
	if err != nil || len(row) == 0 {
		return nil
	}

	a := row[0].toArtifact()
	a.Sections = s.loadSections(a.ID)
	return a
}

func (s *SurrealStore) GetArtifactByKey(key knowledge.ArtifactKey) *knowledge.Artifact {
	db := s.client.DB()
	if db == nil {
		return nil
	}

	key = key.Normalized()
	rows, err := queryOne[[]surrealKnowledgeArtifact](ctx(), db,
		`SELECT * FROM ca_knowledge_artifact
		 WHERE repo_id = $repo_id
		   AND type = $type
		   AND audience = $audience
		   AND depth = $depth
		   AND scope_key = $scope_key
		 LIMIT 1`,
		map[string]any{
			"repo_id":   key.RepositoryID,
			"type":      string(key.Type),
			"audience":  string(key.Audience),
			"depth":     string(key.Depth),
			"scope_key": key.ScopeKey(),
		})
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
		"SELECT * FROM ca_knowledge_artifact WHERE repo_id = $repo_id ORDER BY created_at DESC",
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
		 SET status = $status, error_code = $error_code, error_message = $error_message, updated_at = time::now()`,
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

	_, err := queryOne[interface{}](ctx(), db,
		`UPDATE type::thing('ca_knowledge_artifact', $id) SET stale = $stale, updated_at = time::now()`,
		map[string]any{"id": id, "stale": stale})
	return err
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

		_, err := surrealdb.Query[interface{}](ctx(), db,
			`CREATE ca_knowledge_section SET
				id = type::thing('ca_knowledge_section', $id),
				artifact_id = $artifact_id,
				title = $title,
				content = $content,
				summary = $summary,
				confidence = $confidence,
				inferred = $inferred,
				order_index = $order_index`,
			map[string]any{
				"id":          secID,
				"artifact_id": artifactID,
				"title":       sec.Title,
				"content":     sec.Content,
				"summary":     sec.Summary,
				"confidence":  string(sec.Confidence),
				"inferred":    sec.Inferred,
				"order_index": i,
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
		"SELECT * FROM ca_knowledge_section WHERE artifact_id = $artifact_id ORDER BY order_index ASC",
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
		evID := ev.ID
		if evID == "" {
			evID = uuid.New().String()
		}

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
		"SELECT * FROM ca_knowledge_evidence WHERE section_id = $section_id",
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

// loadSections returns ordered sections with nested evidence for an artifact.
func (s *SurrealStore) loadSections(artifactID string) []knowledge.Section {
	sections := s.GetKnowledgeSections(artifactID)
	sort.Slice(sections, func(i, j int) bool { return sections[i].OrderIndex < sections[j].OrderIndex })
	return sections
}
