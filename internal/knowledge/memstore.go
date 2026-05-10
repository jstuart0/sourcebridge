// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemStore is an in-memory implementation of KnowledgeStore.
type MemStore struct {
	mu             sync.RWMutex
	artifacts      map[string]*Artifact                // artifactID -> Artifact
	sections       map[string][]Section                // artifactID -> []Section
	evidence       map[string][]Evidence               // sectionID -> []Evidence
	understandings map[string]*RepositoryUnderstanding // understandingID -> RepositoryUnderstanding
	dependencies   map[string][]ArtifactDependency     // artifactID -> []ArtifactDependency
	refinements    map[string][]RefinementUnit         // artifactID -> []RefinementUnit
}

// NewMemStore creates a new in-memory knowledge store.
func NewMemStore() *MemStore {
	return &MemStore{
		artifacts:      make(map[string]*Artifact),
		sections:       make(map[string][]Section),
		evidence:       make(map[string][]Evidence),
		understandings: make(map[string]*RepositoryUnderstanding),
		dependencies:   make(map[string][]ArtifactDependency),
		refinements:    make(map[string][]RefinementUnit),
	}
}

// Verify at compile time that *MemStore satisfies KnowledgeStore.
var _ KnowledgeStore = (*MemStore)(nil)

func (s *MemStore) StoreKnowledgeArtifact(_ context.Context, artifact *Artifact) (*Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if artifact.ID == "" {
		artifact.ID = uuid.New().String()
	}
	if artifact.Scope != nil {
		norm := artifact.Scope.Normalize()
		artifact.Scope = &norm
	}
	now := time.Now()
	artifact.CreatedAt = now
	artifact.UpdatedAt = now

	stored := *artifact
	s.artifacts[stored.ID] = &stored
	return &stored, nil
}

func (s *MemStore) StoreRepositoryUnderstanding(_ context.Context, u *RepositoryUnderstanding) (*RepositoryUnderstanding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if u.ID == "" {
		u.ID = uuid.New().String()
	}
	if u.Scope != nil {
		norm := u.Scope.Normalize()
		u.Scope = &norm
	}
	now := time.Now()
	if existing := s.findUnderstandingLocked(u.RepositoryID, u.Scope); existing != nil {
		u.ID = existing.ID
		u.CreatedAt = existing.CreatedAt
	} else if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = now

	stored := *u
	// See knowledge_store.go: progress fields are only meaningful while a
	// build is actively running. Mirror that invariant in the in-memory store
	// so unit tests and the OSS no-DB path behave identically to SurrealDB.
	if !stored.Stage.IsRunning() {
		stored.Progress = 0
		stored.ProgressPhase = ""
		stored.ProgressMessage = ""
	}
	s.understandings[stored.ID] = &stored
	return &stored, nil
}

func (s *MemStore) ClaimArtifact(ctx context.Context, key ArtifactKey, sourceRevision SourceRevision) (*Artifact, bool, error) {
	return s.ClaimArtifactWithMode(ctx, key, sourceRevision, "")
}

func (s *MemStore) ClaimArtifactWithMode(_ context.Context, key ArtifactKey, sourceRevision SourceRevision, mode GenerationMode) (*Artifact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key = key.Normalized()
	normalizedMode := NormalizeGenerationMode(mode)
	var matched *Artifact
	for _, existing := range s.artifacts {
		if existing.RepositoryID != key.RepositoryID || existing.Type != key.Type || existing.Audience != key.Audience || existing.Depth != key.Depth {
			continue
		}
		if artifactScopeKey(existing.Scope) != key.ScopeKey() {
			continue
		}
		if mode != "" && NormalizeGenerationMode(existing.GenerationMode) != normalizedMode {
			continue
		}
		if matched == nil || existing.CreatedAt.After(matched.CreatedAt) {
			matched = existing
		}
	}
	if matched != nil {
		out := *matched
		out.Sections = s.loadSectionsLocked(matched.ID)
		return &out, false, nil
	}

	now := time.Now()
	scope := key.Scope.Normalize()
	artifact := &Artifact{
		ID:             uuid.New().String(),
		RepositoryID:   key.RepositoryID,
		Type:           key.Type,
		Audience:       key.Audience,
		Depth:          key.Depth,
		Scope:          &scope,
		Status:         StatusGenerating,
		Progress:       0,
		SourceRevision: sourceRevision,
		GenerationMode: normalizedMode,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	stored := *artifact
	s.artifacts[stored.ID] = &stored
	return artifact, true, nil
}

func (s *MemStore) GetKnowledgeArtifact(_ context.Context, id string) *Artifact {
	s.mu.RLock()
	defer s.mu.RUnlock()

	a := s.artifacts[id]
	if a == nil {
		return nil
	}
	out := *a
	out.Sections = s.loadSectionsLocked(id)
	return &out
}

func (s *MemStore) GetArtifactByKey(ctx context.Context, key ArtifactKey) *Artifact {
	return s.GetArtifactByKeyAndMode(ctx, key, "")
}

func (s *MemStore) GetArtifactByKeyAndMode(_ context.Context, key ArtifactKey, mode GenerationMode) *Artifact {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key = key.Normalized()
	normalizedMode := NormalizeGenerationMode(mode)
	var matched *Artifact
	for _, existing := range s.artifacts {
		if existing.RepositoryID != key.RepositoryID || existing.Type != key.Type || existing.Audience != key.Audience || existing.Depth != key.Depth {
			continue
		}
		if artifactScopeKey(existing.Scope) != key.ScopeKey() {
			continue
		}
		if mode != "" && NormalizeGenerationMode(existing.GenerationMode) != normalizedMode {
			continue
		}
		if matched == nil || existing.CreatedAt.After(matched.CreatedAt) {
			matched = existing
		}
	}
	if matched == nil {
		return nil
	}
	out := *matched
	out.Sections = s.loadSectionsLocked(matched.ID)
	return &out
}

func (s *MemStore) GetKnowledgeArtifacts(_ context.Context, repoID string) []*Artifact {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*Artifact
	for _, a := range s.artifacts {
		if a.RepositoryID == repoID {
			out := *a
			out.Sections = s.loadSectionsLocked(a.ID)
			results = append(results, &out)
		}
	}
	return results
}

func (s *MemStore) GetRepositoryUnderstanding(_ context.Context, repoID string, scope ArtifactScope) *RepositoryUnderstanding {
	s.mu.RLock()
	defer s.mu.RUnlock()

	existing := s.findUnderstandingLocked(repoID, &scope)
	if existing == nil {
		return nil
	}
	out := *existing
	return &out
}

func (s *MemStore) GetRepositoryUnderstandings(_ context.Context, repoID string) []*RepositoryUnderstanding {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*RepositoryUnderstanding
	for _, u := range s.understandings {
		if u.RepositoryID != repoID {
			continue
		}
		out := *u
		results = append(results, &out)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})
	return results
}

func (s *MemStore) UpdateKnowledgeArtifactStatus(_ context.Context, id string, status ArtifactStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.artifacts[id]
	if a == nil {
		return fmt.Errorf("artifact %s not found", id)
	}
	a.Status = status
	if status != StatusFailed {
		a.ErrorCode = ""
		a.ErrorMessage = ""
	}
	a.UpdatedAt = time.Now()
	if status == StatusReady {
		a.Progress = 1.0
		a.GeneratedAt = time.Now()
	}
	return nil
}

func (s *MemStore) SetArtifactFailed(_ context.Context, id string, code string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.artifacts[id]
	if a == nil {
		return fmt.Errorf("artifact %s not found", id)
	}
	// Idempotency gate: only transition from a non-terminal status so a late
	// OnJobFailed callback cannot clobber an artifact that has already reached
	// ready (from a successful concurrent retry) or is already failed. Mirrors
	// the WHERE clause added to the SurrealDB implementation.
	if a.Status != StatusPending && a.Status != StatusGenerating {
		return nil
	}
	a.Status = StatusFailed
	a.ErrorCode = code
	a.ErrorMessage = message
	a.UpdatedAt = time.Now()
	return nil
}

func (s *MemStore) UpdateKnowledgeArtifactProgress(ctx context.Context, id string, progress float64) error {
	return s.UpdateKnowledgeArtifactProgressWithPhase(ctx, id, progress, "", "")
}

func (s *MemStore) UpdateKnowledgeArtifactProgressWithPhase(_ context.Context, id string, progress float64, phase, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.artifacts[id]
	if a == nil {
		return fmt.Errorf("artifact %s not found", id)
	}
	// CA-122 codex r2 M1: terminal artifacts must not have their
	// progress / phase / message re-stamped by a late stream-driver
	// flush or a stalled goroutine. Mirrors the surreal store guard.
	if a.Status == StatusReady || a.Status == StatusFailed {
		return nil
	}
	a.Progress = progress
	if phase != "" {
		a.ProgressPhase = phase
	}
	if message != "" {
		a.ProgressMessage = message
	}
	a.UpdatedAt = time.Now()
	return nil
}

func (s *MemStore) MarkKnowledgeArtifactStale(_ context.Context, id string, stale bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.artifacts[id]
	if a == nil {
		return fmt.Errorf("artifact %s not found", id)
	}
	a.Stale = stale
	if !stale {
		// Clearing stale → drop the reason too; a later refresh will
		// overwrite it cleanly.
		a.StaleReasonJSON = ""
		a.StaleReportID = ""
	}
	a.UpdatedAt = time.Now()
	return nil
}

func (s *MemStore) MarkKnowledgeArtifactStaleWithReason(_ context.Context, id string, reasonJSON string, reportID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.artifacts[id]
	if a == nil {
		return fmt.Errorf("artifact %s not found", id)
	}
	a.Stale = true
	a.StaleReasonJSON = reasonJSON
	a.StaleReportID = reportID
	a.UpdatedAt = time.Now()
	return nil
}

func (s *MemStore) GetArtifactsForSources(_ context.Context, repoID string, sources []SourceRef) []*Artifact {
	if len(sources) == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	wanted := make(map[string]struct{}, len(sources))
	for _, ref := range sources {
		if ref.SourceID == "" {
			continue
		}
		wanted[string(ref.SourceType)+"\x00"+ref.SourceID] = struct{}{}
	}
	if len(wanted) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var out []*Artifact
	for _, a := range s.artifacts {
		if a.RepositoryID != repoID {
			continue
		}
		if _, dup := seen[a.ID]; dup {
			continue
		}
		// Walk this artifact's sections -> evidence, testing each row.
		if s.artifactMatchesSourceLocked(a.ID, wanted) {
			seen[a.ID] = struct{}{}
			clone := *a
			clone.Sections = s.loadSectionsLocked(a.ID)
			out = append(out, &clone)
		}
	}
	return out
}

func (s *MemStore) GetArtifactsForFiles(_ context.Context, repoID string, filePaths []string) []*Artifact {
	if len(filePaths) == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	wanted := make(map[string]struct{}, len(filePaths))
	for _, p := range filePaths {
		if p == "" {
			continue
		}
		wanted[p] = struct{}{}
	}
	if len(wanted) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var out []*Artifact
	for _, a := range s.artifacts {
		if a.RepositoryID != repoID {
			continue
		}
		if _, dup := seen[a.ID]; dup {
			continue
		}
		if s.artifactMatchesFileLocked(a.ID, wanted) {
			seen[a.ID] = struct{}{}
			clone := *a
			clone.Sections = s.loadSectionsLocked(a.ID)
			out = append(out, &clone)
		}
	}
	return out
}

// artifactMatchesSourceLocked returns true if any evidence row on any of the
// artifact's sections matches the (source_type, source_id) set. Caller must
// hold s.mu.
func (s *MemStore) artifactMatchesSourceLocked(artifactID string, wanted map[string]struct{}) bool {
	for _, sec := range s.sections[artifactID] {
		for _, ev := range s.evidence[sec.ID] {
			key := string(ev.SourceType) + "\x00" + ev.SourceID
			if _, ok := wanted[key]; ok {
				return true
			}
		}
	}
	return false
}

// artifactMatchesFileLocked returns true if any evidence row on any of the
// artifact's sections carries one of the given file paths. Caller must hold
// s.mu.
func (s *MemStore) artifactMatchesFileLocked(artifactID string, wanted map[string]struct{}) bool {
	for _, sec := range s.sections[artifactID] {
		for _, ev := range s.evidence[sec.ID] {
			if ev.FilePath == "" {
				continue
			}
			if _, ok := wanted[ev.FilePath]; ok {
				return true
			}
		}
	}
	return false
}

func (s *MemStore) MarkRepositoryUnderstandingNeedsRefresh(_ context.Context, repoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, u := range s.understandings {
		if u.RepositoryID != repoID {
			continue
		}
		if u.Stage == UnderstandingReady || u.Stage == UnderstandingFirstPassReady {
			u.Stage = UnderstandingNeedsRefresh
			u.Progress = 0
			u.ProgressPhase = ""
			u.ProgressMessage = ""
			u.UpdatedAt = now
		}
	}
	return nil
}

func (s *MemStore) MarkRepositoryUnderstandingFailed(_ context.Context, understandingID, errorCode, errorMessage string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	u := s.understandings[understandingID]
	if u == nil {
		return nil // nothing to fail; treat as a no-op
	}
	// Gate matches the SurrealDB WHERE clause: only running stages may
	// transition to FAILED. A late callback on an already-terminal row
	// (e.g. READY from a successful concurrent retry) must not clobber it.
	if !u.Stage.IsRunning() {
		return nil
	}
	u.Stage = UnderstandingFailed
	u.Progress = 0
	u.ProgressPhase = ""
	u.ProgressMessage = ""
	u.ErrorCode = errorCode
	u.ErrorMessage = errorMessage
	u.UpdatedAt = time.Now()
	return nil
}

func (s *MemStore) UpdateRepositoryUnderstandingProgress(_ context.Context, id string, progress float64, phase, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	u := s.understandings[id]
	if u == nil {
		return fmt.Errorf("understanding %s not found", id)
	}
	// Mirror the SurrealDB heartbeat gate: a late heartbeat that arrives
	// after the build has already moved to a terminal stage must not
	// re-stamp progress text.
	if !u.Stage.IsRunning() {
		return nil
	}
	u.Progress = progress
	if phase != "" {
		u.ProgressPhase = phase
	}
	if message != "" {
		u.ProgressMessage = message
	}
	u.UpdatedAt = time.Now()
	return nil
}

func (s *MemStore) AttachArtifactUnderstanding(_ context.Context, artifactID, understandingID, revisionFP string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.artifacts[artifactID]
	if a == nil {
		return fmt.Errorf("artifact %s not found", artifactID)
	}
	a.UnderstandingID = understandingID
	a.UnderstandingRevisionFP = revisionFP
	a.UpdatedAt = time.Now()
	return nil
}

func (s *MemStore) DeleteKnowledgeArtifact(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.artifacts[id]; !ok {
		return fmt.Errorf("artifact %s not found", id)
	}

	for _, sec := range s.sections[id] {
		delete(s.evidence, sec.ID)
	}
	delete(s.sections, id)
	delete(s.refinements, id)
	delete(s.artifacts, id)
	return nil
}

func (s *MemStore) SupersedeArtifact(_ context.Context, id string, sections []Section) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.artifacts[id]
	if a == nil {
		return fmt.Errorf("artifact %s not found", id)
	}
	for _, sec := range s.sections[id] {
		delete(s.evidence, sec.ID)
	}
	stored := make([]Section, len(sections))
	for i, sec := range sections {
		if sec.ID == "" {
			sec.ID = uuid.New().String()
		}
		sec.ArtifactID = id
		sec.OrderIndex = i
		if sec.SectionKey == "" {
			sec.SectionKey = SectionKeyForTitle(sec.Title)
		}
		if sec.RefinementStatus == "" {
			sec.RefinementStatus = "light"
		}
		stored[i] = sec
	}
	s.sections[id] = stored
	for _, sec := range stored {
		if len(sec.Evidence) == 0 {
			delete(s.evidence, sec.ID)
			continue
		}
		evs := make([]Evidence, len(sec.Evidence))
		for i, ev := range sec.Evidence {
			ev.ID = uuid.New().String()
			ev.SectionID = sec.ID
			evs[i] = ev
		}
		s.evidence[sec.ID] = evs
	}
	a.Status = StatusReady
	a.Progress = 1
	a.GeneratedAt = time.Now()
	a.UpdatedAt = a.GeneratedAt
	return nil
}

func (s *MemStore) StoreKnowledgeSections(_ context.Context, artifactID string, sections []Section) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sec := range s.sections[artifactID] {
		delete(s.evidence, sec.ID)
	}
	stored := make([]Section, len(sections))
	for i, sec := range sections {
		if sec.ID == "" {
			sec.ID = uuid.New().String()
		}
		sec.ArtifactID = artifactID
		sec.OrderIndex = i
		if sec.SectionKey == "" {
			sec.SectionKey = SectionKeyForTitle(sec.Title)
		}
		if sec.RefinementStatus == "" {
			sec.RefinementStatus = "light"
		}
		stored[i] = sec
	}
	s.sections[artifactID] = stored
	return nil
}

func (s *MemStore) GetKnowledgeSections(_ context.Context, artifactID string) []Section {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadSectionsLocked(artifactID)
}

func (s *MemStore) StoreRefinementUnits(_ context.Context, artifactID string, units []RefinementUnit) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.artifacts[artifactID]; !ok {
		return fmt.Errorf("artifact %s not found", artifactID)
	}
	now := time.Now()
	existing := s.refinements[artifactID]
	index := make(map[string]int, len(existing))
	for i, unit := range existing {
		index[refinementKey(unit.SectionKey, unit.RefinementType)] = i
	}
	for _, unit := range units {
		if unit.ID == "" {
			unit.ID = uuid.New().String()
		}
		unit.ArtifactID = artifactID
		if unit.CreatedAt.IsZero() {
			unit.CreatedAt = now
		}
		if unit.UpdatedAt.IsZero() {
			unit.UpdatedAt = now
		}
		key := refinementKey(unit.SectionKey, unit.RefinementType)
		if idx, ok := index[key]; ok {
			if existing[idx].CreatedAt.IsZero() {
				existing[idx].CreatedAt = unit.CreatedAt
			}
			unit.CreatedAt = existing[idx].CreatedAt
			existing[idx] = unit
			continue
		}
		index[key] = len(existing)
		existing = append(existing, unit)
	}
	s.refinements[artifactID] = existing
	return nil
}

func (s *MemStore) GetRefinementUnits(_ context.Context, artifactID string) []RefinementUnit {
	s.mu.RLock()
	defer s.mu.RUnlock()
	units := s.refinements[artifactID]
	out := make([]RefinementUnit, len(units))
	copy(out, units)
	return out
}

func (s *MemStore) StoreKnowledgeEvidence(_ context.Context, sectionID string, evidence []Evidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := make([]Evidence, len(evidence))
	for i, ev := range evidence {
		ev.ID = uuid.New().String()
		ev.SectionID = sectionID
		stored[i] = ev
	}
	s.evidence[sectionID] = stored
	return nil
}

func (s *MemStore) GetKnowledgeEvidence(_ context.Context, sectionID string) []Evidence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.evidence[sectionID]
}

func (s *MemStore) StoreArtifactDependencies(_ context.Context, artifactID string, dependencies []ArtifactDependency) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.artifacts[artifactID]; !ok {
		return fmt.Errorf("artifact %s not found", artifactID)
	}
	now := time.Now()
	stored := make([]ArtifactDependency, len(dependencies))
	for i, dep := range dependencies {
		if dep.ID == "" {
			dep.ID = uuid.New().String()
		}
		dep.ArtifactID = artifactID
		if dep.CreatedAt.IsZero() {
			dep.CreatedAt = now
		}
		stored[i] = dep
	}
	s.dependencies[artifactID] = stored
	return nil
}

func (s *MemStore) GetArtifactDependencies(_ context.Context, artifactID string) []ArtifactDependency {
	s.mu.RLock()
	defer s.mu.RUnlock()

	raw := s.dependencies[artifactID]
	if len(raw) == 0 {
		return nil
	}
	out := make([]ArtifactDependency, len(raw))
	copy(out, raw)
	return out
}

func refinementKey(sectionKey, refinementType string) string {
	return sectionKey + "\x00" + refinementType
}

func (s *MemStore) loadSectionsLocked(artifactID string) []Section {
	raw := s.sections[artifactID]
	if len(raw) == 0 {
		return nil
	}
	out := make([]Section, len(raw))
	copy(out, raw)
	sort.Slice(out, func(i, j int) bool { return out[i].OrderIndex < out[j].OrderIndex })
	for i := range out {
		out[i].Evidence = s.evidence[out[i].ID]
	}
	return out
}

func (s *MemStore) findUnderstandingLocked(repoID string, scope *ArtifactScope) *RepositoryUnderstanding {
	target := ArtifactScope{ScopeType: ScopeRepository}
	if scope != nil {
		target = scope.Normalize()
	}
	targetKey := target.ScopeKey()
	for _, existing := range s.understandings {
		if existing.RepositoryID != repoID {
			continue
		}
		existingScope := ArtifactScope{ScopeType: ScopeRepository}
		if existing.Scope != nil {
			existingScope = existing.Scope.Normalize()
		}
		if existingScope.ScopeKey() == targetKey {
			return existing
		}
	}
	return nil
}

func artifactScopeKey(scope *ArtifactScope) string {
	if scope == nil {
		return ArtifactScope{ScopeType: ScopeRepository}.ScopeKey()
	}
	return scope.ScopeKey()
}
