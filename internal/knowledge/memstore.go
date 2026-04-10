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
	mu        sync.RWMutex
	artifacts map[string]*Artifact  // artifactID -> Artifact
	sections  map[string][]Section  // artifactID -> []Section
	evidence  map[string][]Evidence // sectionID -> []Evidence
}

// NewMemStore creates a new in-memory knowledge store.
func NewMemStore() *MemStore {
	return &MemStore{
		artifacts: make(map[string]*Artifact),
		sections:  make(map[string][]Section),
		evidence:  make(map[string][]Evidence),
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

func (s *MemStore) ClaimArtifact(key ArtifactKey, sourceRevision SourceRevision) (*Artifact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key = key.Normalized()
	for _, existing := range s.artifacts {
		if existing.RepositoryID != key.RepositoryID || existing.Type != key.Type || existing.Audience != key.Audience || existing.Depth != key.Depth {
			continue
		}
		if artifactScopeKey(existing.Scope) == key.ScopeKey() {
			out := *existing
			out.Sections = s.loadSectionsLocked(existing.ID)
			return &out, false, nil
		}
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
	s.mu.RLock()
	defer s.mu.RUnlock()

	key = key.Normalized()
	for _, existing := range s.artifacts {
		if existing.RepositoryID != key.RepositoryID || existing.Type != key.Type || existing.Audience != key.Audience || existing.Depth != key.Depth {
			continue
		}
		if artifactScopeKey(existing.Scope) == key.ScopeKey() {
			out := *existing
			out.Sections = s.loadSectionsLocked(existing.ID)
			return &out
		}
	}
	return nil
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
			if ev.ID == "" {
				ev.ID = uuid.New().String()
			}
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

func (s *MemStore) StoreKnowledgeEvidence(sectionID string, evidence []Evidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := make([]Evidence, len(evidence))
	for i, ev := range evidence {
		if ev.ID == "" {
			ev.ID = uuid.New().String()
		}
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

func artifactScopeKey(scope *ArtifactScope) string {
	if scope == nil {
		return ArtifactScope{ScopeType: ScopeRepository}.ScopeKey()
	}
	return scope.ScopeKey()
}
