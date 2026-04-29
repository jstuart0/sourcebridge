// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package knowledge

import (
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

func (s *MemStore) StoreKnowledgeArtifact(artifact *Artifact) (*Artifact, error) {
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

func (s *MemStore) StoreRepositoryUnderstanding(u *RepositoryUnderstanding) (*RepositoryUnderstanding, error) {
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
	s.understandings[stored.ID] = &stored
	return &stored, nil
}

func (s *MemStore) ClaimArtifact(key ArtifactKey, sourceRevision SourceRevision) (*Artifact, bool, error) {
	return s.ClaimArtifactWithMode(key, sourceRevision, "")
}

func (s *MemStore) ClaimArtifactWithMode(key ArtifactKey, sourceRevision SourceRevision, mode GenerationMode) (*Artifact, bool, error) {
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

func (s *MemStore) GetKnowledgeArtifact(id string) *Artifact {
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

func (s *MemStore) GetArtifactByKey(key ArtifactKey) *Artifact {
	return s.GetArtifactByKeyAndMode(key, "")
}

func (s *MemStore) GetArtifactByKeyAndMode(key ArtifactKey, mode GenerationMode) *Artifact {
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

func (s *MemStore) GetKnowledgeArtifacts(repoID string) []*Artifact {
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

func (s *MemStore) GetRepositoryUnderstanding(repoID string, scope ArtifactScope) *RepositoryUnderstanding {
	s.mu.RLock()
	defer s.mu.RUnlock()

	existing := s.findUnderstandingLocked(repoID, &scope)
	if existing == nil {
		return nil
	}
	out := *existing
	return &out
}

func (s *MemStore) GetRepositoryUnderstandings(repoID string) []*RepositoryUnderstanding {
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

func (s *MemStore) UpdateKnowledgeArtifactStatus(id string, status ArtifactStatus) error {
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

func (s *MemStore) SetArtifactFailed(id string, code string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.artifacts[id]
	if a == nil {
		return fmt.Errorf("artifact %s not found", id)
	}
	a.Status = StatusFailed
	a.ErrorCode = code
	a.ErrorMessage = message
	a.UpdatedAt = time.Now()
	return nil
}

func (s *MemStore) UpdateKnowledgeArtifactProgress(id string, progress float64) error {
	return s.UpdateKnowledgeArtifactProgressWithPhase(id, progress, "", "")
}

func (s *MemStore) UpdateKnowledgeArtifactProgressWithPhase(id string, progress float64, phase, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a := s.artifacts[id]
	if a == nil {
		return fmt.Errorf("artifact %s not found", id)
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

func (s *MemStore) MarkKnowledgeArtifactStale(id string, stale bool) error {
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

func (s *MemStore) MarkKnowledgeArtifactStaleWithReason(id string, reasonJSON string, reportID string) error {
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

func (s *MemStore) GetArtifactsForSources(repoID string, sources []SourceRef) []*Artifact {
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

func (s *MemStore) GetArtifactsForFiles(repoID string, filePaths []string) []*Artifact {
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

func (s *MemStore) MarkRepositoryUnderstandingNeedsRefresh(repoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, u := range s.understandings {
		if u.RepositoryID != repoID {
			continue
		}
		if u.Stage == UnderstandingReady || u.Stage == UnderstandingFirstPassReady {
			u.Stage = UnderstandingNeedsRefresh
			u.UpdatedAt = now
		}
	}
	return nil
}

func (s *MemStore) UpdateRepositoryUnderstandingProgress(id string, progress float64, phase, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	u := s.understandings[id]
	if u == nil {
		return fmt.Errorf("understanding %s not found", id)
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

func (s *MemStore) AttachArtifactUnderstanding(artifactID, understandingID, revisionFP string) error {
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

func (s *MemStore) DeleteKnowledgeArtifact(id string) error {
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

func (s *MemStore) SupersedeArtifact(id string, sections []Section) error {
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

func (s *MemStore) StoreKnowledgeSections(artifactID string, sections []Section) error {
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

func (s *MemStore) GetKnowledgeSections(artifactID string) []Section {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadSectionsLocked(artifactID)
}

func (s *MemStore) StoreRefinementUnits(artifactID string, units []RefinementUnit) error {
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

func (s *MemStore) GetRefinementUnits(artifactID string) []RefinementUnit {
	s.mu.RLock()
	defer s.mu.RUnlock()
	units := s.refinements[artifactID]
	out := make([]RefinementUnit, len(units))
	copy(out, units)
	return out
}

func (s *MemStore) StoreKnowledgeEvidence(sectionID string, evidence []Evidence) error {
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

func (s *MemStore) GetKnowledgeEvidence(sectionID string) []Evidence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.evidence[sectionID]
}

func (s *MemStore) StoreArtifactDependencies(artifactID string, dependencies []ArtifactDependency) error {
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

func (s *MemStore) GetArtifactDependencies(artifactID string) []ArtifactDependency {
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
