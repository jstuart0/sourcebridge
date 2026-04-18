package graphql

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/indexer"
	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
)

type stubComprehensionStore struct {
	workspace *comprehension.Settings
}

func (s stubComprehensionStore) GetSettings(scope comprehension.Scope) (*comprehension.Settings, error) {
	if scope == comprehension.WorkspaceScope && s.workspace != nil {
		return s.workspace, nil
	}
	return nil, nil
}

func (s stubComprehensionStore) SetSettings(settings *comprehension.Settings) error {
	return nil
}

func (s stubComprehensionStore) DeleteSettings(scope comprehension.Scope) error {
	return nil
}

func (s stubComprehensionStore) ListSettings() ([]comprehension.Settings, error) {
	return nil, nil
}

func (s stubComprehensionStore) GetModelCapabilities(modelID string) (*comprehension.ModelCapabilities, error) {
	return nil, nil
}

func (s stubComprehensionStore) SetModelCapabilities(m *comprehension.ModelCapabilities) error {
	return nil
}

func (s stubComprehensionStore) DeleteModelCapabilities(modelID string) error {
	return nil
}

func (s stubComprehensionStore) ListModelCapabilities() ([]comprehension.ModelCapabilities, error) {
	return nil, nil
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func TestTopLevelModuleScopesFallsBackToFilesWhenModulesMissing(t *testing.T) {
	store := graph.NewStore()
	result := &indexer.IndexResult{
		RepoName: "fallback-repo",
		RepoPath: "/tmp/fallback-repo",
		Files: []indexer.FileResult{
			{Path: "main.go", Language: "go", LineCount: 20},
			{Path: "internal/api/auth.go", Language: "go", LineCount: 40},
			{Path: "web/app/page.tsx", Language: "typescript", LineCount: 80},
		},
	}
	repo, err := store.StoreIndexResult(result)
	if err != nil {
		t.Fatalf("StoreIndexResult: %v", err)
	}

	children := buildScopeChildren(store, repo.ID, knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository})
	if len(children) == 0 {
		t.Fatal("expected repository children from file fallback")
	}

	foundRootFile := false
	foundInternalModule := false
	for _, child := range children {
		if child.ScopeType == knowledgepkg.ScopeFile && child.ScopePath == "main.go" {
			foundRootFile = true
		}
		if child.ScopeType == knowledgepkg.ScopeModule && child.ScopePath == "internal" {
			foundInternalModule = true
		}
	}
	if !foundRootFile {
		t.Fatal("expected top-level file child")
	}
	if !foundInternalModule {
		t.Fatal("expected top-level module child from file paths")
	}
}

func TestResolvedKnowledgeGenerationModePrecedence(t *testing.T) {
	repo := &graph.Repository{GenerationModeDefault: "classic"}
	store := stubComprehensionStore{
		workspace: &comprehension.Settings{
			ScopeType:                      comprehension.ScopeWorkspace,
			ScopeKey:                       comprehension.WorkspaceScope.Key,
			KnowledgeGenerationModeDefault: "understanding_first",
		},
	}

	mode := resolvedKnowledgeGenerationMode(store, repo, nil)
	if mode != knowledgepkg.GenerationModeClassic {
		t.Fatalf("expected repo default to win, got %q", mode)
	}

	requested := KnowledgeGenerationModeUnderstandingFirst
	mode = resolvedKnowledgeGenerationMode(store, repo, &requested)
	if mode != knowledgepkg.GenerationModeUnderstandingFirst {
		t.Fatalf("expected request override to win, got %q", mode)
	}
}

func TestCliffNotesSectionMetadataJSON(t *testing.T) {
	understanding := &knowledgepkg.RepositoryUnderstanding{
		ID:         "u-123",
		RevisionFP: "rev-456",
	}
	raw := cliffNotesSectionMetadataJSON(
		knowledgepkg.ArtifactCliffNotes,
		understanding,
		"deep",
		"Core System Flows",
		true,
	)
	if raw == "" {
		t.Fatal("expected metadata JSON")
	}
	var meta cliffNotesSectionMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta.SectionKey != "core_system_flows" {
		t.Fatalf("expected section key core_system_flows, got %q", meta.SectionKey)
	}
	if meta.RefinementTier != "deep" {
		t.Fatalf("expected deep refinement tier, got %q", meta.RefinementTier)
	}
	if !meta.RefinedWithEvidence {
		t.Fatal("expected refined_with_evidence=true")
	}
	if meta.EvidenceRevisionFP != "rev-456" || meta.UnderstandingID != "u-123" {
		t.Fatalf("unexpected understanding linkage %#v", meta)
	}
	if meta.RendererVersion != knowledgepkg.RendererVersionForArtifact(knowledgepkg.ArtifactCliffNotes) {
		t.Fatalf("unexpected renderer version %q", meta.RendererVersion)
	}
}

func TestCliffNotesDeepeningTargetsSkipsQueuedRunningAndCompletedUnits(t *testing.T) {
	store := knowledgepkg.NewMemStore()
	artifact, err := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		RepositoryID: "repo-1",
		Type:         knowledgepkg.ArtifactCliffNotes,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthDeep,
		Scope:        &knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
		Status:       knowledgepkg.StatusReady,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	sections := []knowledgepkg.Section{
		{Title: "Architecture Overview", SectionKey: "architecture_overview", RefinementStatus: "light"},
		{Title: "Domain Model", SectionKey: "domain_model", RefinementStatus: "light"},
		{Title: "External Dependencies", SectionKey: "external_dependencies", RefinementStatus: "light"},
		{Title: "Key Abstractions", SectionKey: "key_abstractions", RefinementStatus: "light"},
	}
	if err := store.StoreKnowledgeSections(artifact.ID, sections); err != nil {
		t.Fatalf("StoreKnowledgeSections: %v", err)
	}
	if err := store.StoreRefinementUnits(artifact.ID, []knowledgepkg.RefinementUnit{
		{SectionKey: "architecture_overview", SectionTitle: "Architecture Overview", RefinementType: cliffNotesDeepRefinementType, Status: knowledgepkg.RefinementQueued},
		{SectionKey: "external_dependencies", SectionTitle: "External Dependencies", RefinementType: cliffNotesDeepRefinementType, Status: knowledgepkg.RefinementRunning},
		{SectionKey: "domain_model", SectionTitle: "Domain Model", RefinementType: cliffNotesDeepRefinementType, Status: knowledgepkg.RefinementCompleted},
	}); err != nil {
		t.Fatalf("StoreRefinementUnits: %v", err)
	}

	targets := cliffNotesDeepeningTargets(store, artifact)
	if len(targets) != 1 || targets[0] != "Key Abstractions" {
		t.Fatalf("unexpected deepening targets: %#v", targets)
	}
}

func TestCliffNotesDeepeningTargetsIncludeLowConfidenceOrInferredSections(t *testing.T) {
	store := knowledgepkg.NewMemStore()
	artifact, err := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		RepositoryID: "repo-1",
		Type:         knowledgepkg.ArtifactCliffNotes,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthDeep,
		Scope:        &knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
		Status:       knowledgepkg.StatusReady,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	sections := []knowledgepkg.Section{
		{Title: "Architecture Overview", SectionKey: "architecture_overview", RefinementStatus: "light", Confidence: knowledgepkg.ConfidenceHigh},
		{Title: "Domain Model", SectionKey: "domain_model", RefinementStatus: "light", Confidence: knowledgepkg.ConfidenceHigh},
		{Title: "External Dependencies", SectionKey: "external_dependencies", RefinementStatus: "light", Confidence: knowledgepkg.ConfidenceHigh},
		{Title: "Key Abstractions", SectionKey: "key_abstractions", RefinementStatus: "light", Confidence: knowledgepkg.ConfidenceHigh},
		{Title: "Testing Strategy", SectionKey: "testing_strategy", RefinementStatus: "needs_evidence", Confidence: knowledgepkg.ConfidenceLow},
		{Title: "Configuration & Feature Flags", SectionKey: "configuration_feature_flags", RefinementStatus: "light", Confidence: knowledgepkg.ConfidenceHigh, Inferred: true},
		{Title: "Concurrency & Background Work", SectionKey: "concurrency_background_work", RefinementStatus: "unsupported_claims", Confidence: knowledgepkg.ConfidenceHigh},
	}
	if err := store.StoreKnowledgeSections(artifact.ID, sections); err != nil {
		t.Fatalf("StoreKnowledgeSections: %v", err)
	}

	targets := cliffNotesDeepeningTargets(store, artifact)
	if len(targets) != 7 {
		t.Fatalf("expected 7 targets, got %#v", targets)
	}
	if targets[0] != "Testing Strategy" || targets[1] != "Concurrency & Background Work" || targets[2] != "Configuration & Feature Flags" {
		t.Fatalf("expected explicit weak sections first, got %#v", targets)
	}
	if !containsString(targets, "Architecture Overview") || !containsString(targets, "Domain Model") || !containsString(targets, "External Dependencies") || !containsString(targets, "Key Abstractions") {
		t.Fatalf("expected default deepening sections to remain, got %#v", targets)
	}
	if !containsString(targets, "Testing Strategy") || !containsString(targets, "Configuration & Feature Flags") || !containsString(targets, "Concurrency & Background Work") {
		t.Fatalf("expected dynamic weak-section targets, got %#v", targets)
	}
}

func TestMarkCliffNotesDeepRefinementStatusTracksAttempts(t *testing.T) {
	store := knowledgepkg.NewMemStore()
	artifact, err := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		ID:                      "artifact-1",
		RepositoryID:            "repo-1",
		Type:                    knowledgepkg.ArtifactCliffNotes,
		Audience:                knowledgepkg.AudienceDeveloper,
		Depth:                   knowledgepkg.DepthDeep,
		Status:                  knowledgepkg.StatusReady,
		UnderstandingID:         "u-1",
		UnderstandingRevisionFP: "rev-1",
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	sections := []knowledgepkg.Section{
		{Title: "Core System Flows", SectionKey: "core_system_flows", RefinementStatus: "light"},
	}
	if err := store.StoreKnowledgeSections(artifact.ID, sections); err != nil {
		t.Fatalf("StoreKnowledgeSections: %v", err)
	}

	markCliffNotesDeepRefinementStatus(store, artifact, sections, []string{"Core System Flows"}, knowledgepkg.RefinementQueued, "")
	markCliffNotesDeepRefinementStatus(store, artifact, sections, []string{"Core System Flows"}, knowledgepkg.RefinementRunning, "")
	markCliffNotesDeepRefinementStatus(store, artifact, sections, []string{"Core System Flows"}, knowledgepkg.RefinementFailed, "boom")

	units := store.GetRefinementUnits(artifact.ID)
	if len(units) != 1 {
		t.Fatalf("expected 1 refinement unit, got %d", len(units))
	}
	unit := units[0]
	if unit.Status != knowledgepkg.RefinementFailed {
		t.Fatalf("expected failed status, got %q", unit.Status)
	}
	if unit.AttemptCount != 1 {
		t.Fatalf("expected attempt count 1, got %d", unit.AttemptCount)
	}
	if unit.LastError != "boom" {
		t.Fatalf("expected last error boom, got %q", unit.LastError)
	}
}

func TestSyncCliffNotesRefinementUnitsStoresAllSections(t *testing.T) {
	store := knowledgepkg.NewMemStore()
	artifact, err := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		ID:                      "artifact-sync-1",
		RepositoryID:            "repo-1",
		Type:                    knowledgepkg.ArtifactCliffNotes,
		Audience:                knowledgepkg.AudienceDeveloper,
		Depth:                   knowledgepkg.DepthDeep,
		Status:                  knowledgepkg.StatusReady,
		UnderstandingID:         "u-1",
		UnderstandingRevisionFP: "rev-1",
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	sections := []knowledgepkg.Section{
		{Title: "System Purpose", SectionKey: "system_purpose"},
		{Title: "Architecture Overview", SectionKey: "architecture_overview"},
		{Title: "Domain Model", SectionKey: "domain_model"},
	}

	syncCliffNotesRefinementUnits(store, artifact, sections, &knowledgepkg.RepositoryUnderstanding{
		ID:         "u-1",
		RevisionFP: "rev-1",
	})

	units := store.GetRefinementUnits(artifact.ID)
	if len(units) != 3 {
		t.Fatalf("expected 3 refinement units, got %#v", units)
	}
	for _, unit := range units {
		if unit.RefinementType != cliffNotesLightRefinementType {
			t.Fatalf("expected light refinement units, got %#v", units)
		}
		if unit.Status != knowledgepkg.RefinementCompleted {
			t.Fatalf("expected completed status, got %#v", units)
		}
	}
}

func TestCliffNotesDeepeningOutcomeFailsForWeakSections(t *testing.T) {
	sections := []knowledgepkg.Section{
		{Title: "Domain Model", SectionKey: "domain_model", RefinementStatus: "needs_evidence", Confidence: knowledgepkg.ConfidenceLow},
		{Title: "Key Abstractions", SectionKey: "key_abstractions", RefinementStatus: "deep", Confidence: knowledgepkg.ConfidenceHigh},
	}

	status, lastError := cliffNotesDeepeningOutcome(sections, []string{"Domain Model", "Key Abstractions"})
	if status != knowledgepkg.RefinementFailed {
		t.Fatalf("expected failed status, got %q", status)
	}
	if lastError == "" {
		t.Fatalf("expected non-empty error, got %q", lastError)
	}
	if !strings.Contains(lastError, "Domain Model") {
		t.Fatalf("expected Domain Model in error, got %q", lastError)
	}
}

func TestCliffNotesDeepeningOutcomeCompletesForDeepSections(t *testing.T) {
	sections := []knowledgepkg.Section{
		{Title: "Domain Model", SectionKey: "domain_model", RefinementStatus: "deep", Confidence: knowledgepkg.ConfidenceHigh},
		{Title: "Key Abstractions", SectionKey: "key_abstractions", RefinementStatus: "deep", Confidence: knowledgepkg.ConfidenceMedium},
	}

	status, lastError := cliffNotesDeepeningOutcome(sections, []string{"Domain Model", "Key Abstractions"})
	if status != knowledgepkg.RefinementCompleted {
		t.Fatalf("expected completed status, got %q", status)
	}
	if lastError != "" {
		t.Fatalf("expected empty error, got %q", lastError)
	}
}

func TestShouldAcceptDeepenedSectionRejectsWeakerReplacement(t *testing.T) {
	current := knowledgepkg.Section{
		Title:            "Domain Model",
		Content:          "Detailed grounded section",
		Confidence:       knowledgepkg.ConfidenceHigh,
		RefinementStatus: "deep",
		Evidence: []knowledgepkg.Evidence{
			{FilePath: "internal/api/auth.go"},
			{FilePath: "internal/store/repo.go"},
		},
	}
	incoming := knowledgepkg.Section{
		Title:            "Domain Model",
		Content:          "Thinner replacement",
		Confidence:       knowledgepkg.ConfidenceLow,
		RefinementStatus: "needs_evidence",
		Evidence:         nil,
	}

	if shouldAcceptDeepenedSection(current, incoming) {
		t.Fatal("expected weaker deepened section to be rejected")
	}
}

func TestSelectAcceptedDeepenedSectionsKeepsOnlyImprovements(t *testing.T) {
	existing := []knowledgepkg.Section{
		{
			Title:            "Domain Model",
			Content:          "Detailed grounded section",
			Confidence:       knowledgepkg.ConfidenceHigh,
			RefinementStatus: "deep",
			Evidence:         []knowledgepkg.Evidence{{FilePath: "internal/api/auth.go"}, {FilePath: "internal/store/repo.go"}},
		},
	}
	incoming := []knowledgepkg.Section{
		{
			Title:            "Domain Model",
			Content:          "Thinner replacement",
			Confidence:       knowledgepkg.ConfidenceLow,
			RefinementStatus: "needs_evidence",
			Evidence:         nil,
		},
		{
			Title:            "Key Abstractions",
			Content:          "Improved abstractions section",
			Confidence:       knowledgepkg.ConfidenceHigh,
			RefinementStatus: "deep",
			Evidence:         []knowledgepkg.Evidence{{FilePath: "workers/knowledge/servicer.py"}},
		},
	}

	accepted := selectAcceptedDeepenedSections(existing, incoming, []string{"Domain Model", "Key Abstractions"})
	if len(accepted) != 1 {
		t.Fatalf("expected only one accepted replacement, got %#v", accepted)
	}
	if accepted[0].Title != "Key Abstractions" {
		t.Fatalf("expected Key Abstractions to remain, got %#v", accepted)
	}
}

func TestCliffNotesDeepeningTargetsRequeuesFailedWeakSections(t *testing.T) {
	store := knowledgepkg.NewMemStore()
	artifact, err := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		RepositoryID: "repo-1",
		Type:         knowledgepkg.ArtifactCliffNotes,
		Audience:     knowledgepkg.AudienceDeveloper,
		Depth:        knowledgepkg.DepthDeep,
		Scope:        &knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
		Status:       knowledgepkg.StatusReady,
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	sections := []knowledgepkg.Section{
		{Title: "Domain Model", SectionKey: "domain_model", RefinementStatus: "needs_evidence", Confidence: knowledgepkg.ConfidenceLow},
		{Title: "Architecture Overview", SectionKey: "architecture_overview", RefinementStatus: "deep", Confidence: knowledgepkg.ConfidenceHigh},
	}
	if err := store.StoreKnowledgeSections(artifact.ID, sections); err != nil {
		t.Fatalf("StoreKnowledgeSections: %v", err)
	}
	if err := store.StoreRefinementUnits(artifact.ID, []knowledgepkg.RefinementUnit{
		{
			SectionKey:     "domain_model",
			SectionTitle:   "Domain Model",
			RefinementType: cliffNotesDeepRefinementType,
			Status:         knowledgepkg.RefinementFailed,
			LastError:      "deepening did not materially improve sections: Domain Model",
		},
	}); err != nil {
		t.Fatalf("StoreRefinementUnits: %v", err)
	}

	targets := cliffNotesDeepeningTargets(store, artifact)
	if !containsString(targets, "Domain Model") {
		t.Fatalf("expected failed weak section to be requeued, got %#v", targets)
	}
}

func TestCliffNotesRenderPlanForArtifactUsesUnderstandingBackedDeepRender(t *testing.T) {
	store := knowledgepkg.NewMemStore()
	artifact, err := store.StoreKnowledgeArtifact(&knowledgepkg.Artifact{
		RepositoryID:            "repo-1",
		Type:                    knowledgepkg.ArtifactCliffNotes,
		Audience:                knowledgepkg.AudienceDeveloper,
		Depth:                   knowledgepkg.DepthDeep,
		Scope:                   &knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository},
		UnderstandingRevisionFP: "rev-1",
		RendererVersion:         knowledgepkg.RendererVersionForArtifact(knowledgepkg.ArtifactCliffNotes),
	})
	if err != nil {
		t.Fatalf("StoreKnowledgeArtifact: %v", err)
	}
	understanding, err := store.StoreRepositoryUnderstanding(&knowledgepkg.RepositoryUnderstanding{
		RepositoryID: "repo-1",
		Scope:        (&knowledgepkg.ArtifactScope{ScopeType: knowledgepkg.ScopeRepository}).NormalizePtr(),
		RevisionFP:   "rev-1",
		Stage:        knowledgepkg.UnderstandingReady,
		TreeStatus:   knowledgepkg.UnderstandingTreeComplete,
	})
	if err != nil {
		t.Fatalf("StoreRepositoryUnderstanding: %v", err)
	}
	_ = understanding

	plan := cliffNotesRenderPlanForArtifact(store, artifact, knowledgepkg.SourceRevision{ContentFingerprint: "rev-1"}, understanding)
	if !plan.RenderOnly {
		t.Fatal("expected render-only plan for fresh DEEP artifact backed by understanding")
	}
	if plan.UnderstandingDepth != string(knowledgepkg.DepthMedium) {
		t.Fatalf("expected medium understanding depth, got %q", plan.UnderstandingDepth)
	}
	if plan.RelevanceProfile != "product_core" {
		t.Fatalf("expected product_core relevance profile, got %q", plan.RelevanceProfile)
	}
	if len(plan.SelectedSectionTitles) != 16 {
		t.Fatalf("expected 16 deep section titles, got %d", len(plan.SelectedSectionTitles))
	}
}
