package graphql

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
)

func TestEnqueueKnowledgeJobCreatesQueuedKnowledgeJob(t *testing.T) {
	knowledgeStore := knowledge.NewMemStore()
	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 1})
	defer func() {
		_ = orch.Shutdown(2 * time.Second)
	}()

	key := knowledge.ArtifactKey{
		RepositoryID: "repo-1",
		Type:         knowledge.ArtifactCliffNotes,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Scope:        knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository},
	}
	artifact, created, err := knowledgeStore.ClaimArtifact(key, knowledge.SourceRevision{})
	if err != nil {
		t.Fatalf("ClaimArtifact: %v", err)
	}
	if !created {
		t.Fatal("expected fresh artifact claim")
	}

	r := &Resolver{
		KnowledgeStore: knowledgeStore,
		Orchestrator:   orch,
	}

	block := make(chan struct{})
	err = r.enqueueKnowledgeJob(artifact, "seed:cliff_notes", 1234, func(_ context.Context, rt llm.Runtime) error {
		rt.ReportProgress(0.2, "snapshot", "queued")
		<-block
		return nil
	})
	if err != nil {
		t.Fatalf("enqueueKnowledgeJob: %v", err)
	}

	var job *llm.Job
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		active := orch.ListActive(llm.ListFilter{
			Subsystem: llm.SubsystemKnowledge,
			JobType:   "seed:cliff_notes",
			RepoID:    "repo-1",
		})
		if len(active) > 0 {
			job = active[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job == nil {
		t.Fatal("expected active queued job")
	}
	if job.ArtifactID != artifact.ID {
		t.Fatalf("expected artifact id %q, got %q", artifact.ID, job.ArtifactID)
	}
	if job.TargetKey != knowledgeJobTargetKey(key) {
		t.Fatalf("expected target key %q, got %q", knowledgeJobTargetKey(key), job.TargetKey)
	}
	if job.Subsystem != llm.SubsystemKnowledge {
		t.Fatalf("expected subsystem %q, got %q", llm.SubsystemKnowledge, job.Subsystem)
	}
	close(block)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job = jobStore.GetByID(job.ID)
		if job != nil && job.Status == llm.StatusReady && job.SnapshotBytes == 1234 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job == nil {
		t.Fatal("expected completed job")
	}
	if job.Status != llm.StatusReady {
		t.Fatalf("expected ready status, got %q", job.Status)
	}
	if job.SnapshotBytes != 1234 {
		t.Fatalf("expected snapshot bytes 1234, got %d", job.SnapshotBytes)
	}
}

func TestKnowledgeJobsShareGlobalConcurrencyGate(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_KNOWLEDGE_MAX_CONCURRENCY", "1")
	knowledgeArtifactGates = sync.Map{}
	knowledgeGlobalSlots = nil
	knowledgeGlobalGate = sync.Once{}

	knowledgeStore := knowledge.NewMemStore()
	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 2})
	defer func() {
		_ = orch.Shutdown(2 * time.Second)
	}()

	makeArtifact := func(repoID string, artifactType knowledge.ArtifactType) *knowledge.Artifact {
		key := knowledge.ArtifactKey{
			RepositoryID: repoID,
			Type:         artifactType,
			Audience:     knowledge.AudienceDeveloper,
			Depth:        knowledge.DepthMedium,
			Scope:        knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository},
		}
		artifact, created, err := knowledgeStore.ClaimArtifact(key, knowledge.SourceRevision{})
		if err != nil {
			t.Fatalf("ClaimArtifact(%s): %v", artifactType, err)
		}
		if !created {
			t.Fatalf("expected fresh artifact claim for %s", artifactType)
		}
		return artifact
	}

	r := &Resolver{
		KnowledgeStore: knowledgeStore,
		Orchestrator:   orch,
	}

	entered := make(chan string, 2)
	releaseRunning := make(chan struct{})

	first := makeArtifact("repo-1", knowledge.ArtifactCliffNotes)
	if err := r.enqueueKnowledgeJob(first, "cliff_notes", 100, func(_ context.Context, rt llm.Runtime) error {
		rt.ReportProgress(0.25, "generating", "first")
		entered <- "cliff_notes"
		<-releaseRunning
		return nil
	}); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}

	second := makeArtifact("repo-1", knowledge.ArtifactCodeTour)
	if err := r.enqueueKnowledgeJob(second, "code_tour", 100, func(_ context.Context, rt llm.Runtime) error {
		rt.ReportProgress(0.25, "generating", "second")
		entered <- "code_tour"
		<-releaseRunning
		return nil
	}); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no job entered the shared knowledge slot")
	}

	select {
	case name := <-entered:
		t.Fatalf("expected only one job in the shared knowledge slot before release, but %s entered too", name)
	case <-time.After(250 * time.Millisecond):
	}

	close(releaseRunning)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("second job never entered after shared slot released")
	}
}

func TestRepositoryCliffNotesJobsDoNotAutoRetry(t *testing.T) {
	knowledgeStore := knowledge.NewMemStore()
	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 1})
	defer func() {
		_ = orch.Shutdown(2 * time.Second)
	}()

	key := knowledge.ArtifactKey{
		RepositoryID: "repo-1",
		Type:         knowledge.ArtifactCliffNotes,
		Audience:     knowledge.AudienceDeveloper,
		Depth:        knowledge.DepthMedium,
		Scope:        knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository},
	}
	artifact, created, err := knowledgeStore.ClaimArtifact(key, knowledge.SourceRevision{})
	if err != nil {
		t.Fatalf("ClaimArtifact: %v", err)
	}
	if !created {
		t.Fatal("expected fresh artifact claim")
	}

	r := &Resolver{
		KnowledgeStore: knowledgeStore,
		Orchestrator:   orch,
	}
	if err := r.enqueueKnowledgeJob(artifact, "cliff_notes", 256, func(_ context.Context, _ llm.Runtime) error {
		return nil
	}); err != nil {
		t.Fatalf("enqueueKnowledgeJob: %v", err)
	}

	var job *llm.Job
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		active := orch.ListActive(llm.ListFilter{
			Subsystem: llm.SubsystemKnowledge,
			JobType:   "cliff_notes",
			RepoID:    "repo-1",
		})
		if len(active) > 0 {
			job = active[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job == nil {
		t.Fatal("expected active queued job")
	}
	if job.MaxAttempts != 1 {
		t.Fatalf("expected max attempts 1, got %d", job.MaxAttempts)
	}
}

func TestQueuedKnowledgeJobsHeartbeatWhileWaitingForGate(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_KNOWLEDGE_MAX_CONCURRENCY", "1")
	knowledgeArtifactGates = sync.Map{}
	knowledgeGlobalSlots = nil
	knowledgeGlobalGate = sync.Once{}
	prevInterval := knowledgeQueueHeartbeatInterval
	knowledgeQueueHeartbeatInterval = 10 * time.Millisecond
	defer func() {
		knowledgeQueueHeartbeatInterval = prevInterval
	}()

	knowledgeStore := knowledge.NewMemStore()
	jobStore := llm.NewMemStore()
	orch := orchestrator.New(jobStore, orchestrator.Config{MaxConcurrency: 2})
	defer func() {
		_ = orch.Shutdown(2 * time.Second)
	}()

	makeArtifact := func(repoID string) *knowledge.Artifact {
		key := knowledge.ArtifactKey{
			RepositoryID: repoID,
			Type:         knowledge.ArtifactCliffNotes,
			Audience:     knowledge.AudienceDeveloper,
			Depth:        knowledge.DepthDeep,
			Scope:        knowledge.ArtifactScope{ScopeType: knowledge.ScopeRepository},
		}
		artifact, created, err := knowledgeStore.ClaimArtifact(key, knowledge.SourceRevision{})
		if err != nil {
			t.Fatalf("ClaimArtifact(%s): %v", repoID, err)
		}
		if !created {
			t.Fatalf("expected fresh artifact claim for %s", repoID)
		}
		return artifact
	}

	r := &Resolver{
		KnowledgeStore: knowledgeStore,
		Orchestrator:   orch,
	}

	releaseFirst := make(chan struct{})
	firstEntered := make(chan struct{}, 1)
	first := makeArtifact("repo-1")
	if err := r.enqueueKnowledgeJob(first, "cliff_notes", 100, func(_ context.Context, rt llm.Runtime) error {
		rt.ReportProgress(0.25, "generating", "first")
		firstEntered <- struct{}{}
		<-releaseFirst
		return nil
	}); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}

	second := makeArtifact("repo-2")
	if err := r.enqueueKnowledgeJob(second, "cliff_notes", 100, func(_ context.Context, rt llm.Runtime) error {
		return nil
	}); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	select {
	case <-firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first job never entered the knowledge slot")
	}

	var waitingJob *llm.Job
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		active := orch.ListActive(llm.ListFilter{
			Subsystem: llm.SubsystemKnowledge,
			JobType:   "cliff_notes",
			RepoID:    "repo-2",
		})
		if len(active) > 0 {
			waitingJob = active[0]
			if waitingJob.ProgressPhase == "queued" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waitingJob == nil {
		t.Fatal("expected second job to be active and waiting")
	}

	initialUpdatedAt := waitingJob.UpdatedAt
	time.Sleep(50 * time.Millisecond)
	waitingJob = jobStore.GetByID(waitingJob.ID)
	if waitingJob == nil {
		t.Fatal("expected waiting job to still exist")
	}
	if !waitingJob.UpdatedAt.After(initialUpdatedAt) {
		t.Fatalf("expected queued waiting job heartbeat to advance updated_at, initial=%s current=%s", initialUpdatedAt, waitingJob.UpdatedAt)
	}
	if waitingJob.ProgressPhase != "queued" {
		t.Fatalf("expected queued progress phase while waiting, got %q", waitingJob.ProgressPhase)
	}

	close(releaseFirst)
}
