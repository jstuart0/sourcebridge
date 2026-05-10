// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package rest

// CA-TBD-knowledge-artifact-reconciler-coverage: unit tests for the extended
// OnJobFailed dispatch. Each test invokes the closure directly (same package)
// and verifies the correct store method fires for each job type.

import (
	"context"
	"testing"

	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
)

// buildOnJobFailedCallback assembles the same closure that NewServer wires into
// orchCfg.OnJobFailed, but using caller-supplied mocks. Keeping this in a helper
// makes each test case concise without a full Server construction.
//
// IMPORTANT: this function must stay in sync with the switch in router.go's
// OnJobFailed registration. If the dispatch changes there, update this helper
// and the tests below will confirm correct routing at the store-call level.
func buildOnJobFailedCallback(ks knowledge.KnowledgeStore, lw livingwiki.JobResultStore) func(*llm.Job) {
	s := &Server{
		knowledgeStore:           ks,
		livingWikiJobResultStore: lw,
	}
	return func(job *llm.Job) {
		if job == nil {
			return
		}
		switch job.JobType {
		case "build_repository_understanding":
			if s.knowledgeStore == nil || job.ArtifactID == "" {
				return
			}
			code := job.ErrorCode
			if code == "" {
				code = "JOB_FAILED"
			}
			msg := job.ErrorMessage
			if msg == "" {
				msg = "Repository understanding job failed"
			}
			_ = s.knowledgeStore.MarkRepositoryUnderstandingFailed(context.Background(), job.ArtifactID, code, msg)
		case "cliff_notes", "architecture_diagram", "learning_path", "code_tour", "workflow_story":
			if s.knowledgeStore == nil || job.ArtifactID == "" {
				return
			}
			code := job.ErrorCode
			if code == "" {
				code = "JOB_FAILED"
			}
			msg := job.ErrorMessage
			if msg == "" {
				msg = "Knowledge artifact generation failed"
			}
			_ = s.knowledgeStore.SetArtifactFailed(context.Background(), job.ArtifactID, code, msg)
		case "living_wiki_cold_start", "living_wiki_retry_excluded":
			if s.livingWikiJobResultStore == nil {
				return
			}
			persistStaleLivingWikiResult(s.livingWikiJobResultStore, job)
		}
	}
}

// TestOnJobFailed_UnderstandingJobType verifies that build_repository_understanding
// calls MarkRepositoryUnderstandingFailed (pre-existing CA-180 path).
func TestOnJobFailed_UnderstandingJobType(t *testing.T) {
	ks := knowledge.NewMemStore()
	cb := buildOnJobFailedCallback(ks, nil)

	u, err := ks.StoreRepositoryUnderstanding(t.Context(), &knowledge.RepositoryUnderstanding{
		ID:           "und-1",
		RepositoryID: "repo-1",
		Stage:        knowledge.UnderstandingBuildingTree,
	})
	if err != nil {
		t.Fatal(err)
	}

	cb(&llm.Job{
		ID:           "job-und",
		JobType:      "build_repository_understanding",
		ArtifactID:   u.ID,
		ErrorCode:    "LLM_TIMEOUT",
		ErrorMessage: "context deadline exceeded",
	})

	got := ks.GetRepositoryUnderstanding(t.Context(), "repo-1", knowledge.ArtifactScope{})
	if got == nil {
		t.Fatal("GetRepositoryUnderstanding returned nil")
	}
	if got.Stage != knowledge.UnderstandingFailed {
		t.Errorf("stage = %q, want %q", got.Stage, knowledge.UnderstandingFailed)
	}
}

// TestOnJobFailed_KnowledgeArtifactJobTypes verifies that every knowledge-artifact
// job type calls SetArtifactFailed and transitions the artifact to StatusFailed.
func TestOnJobFailed_KnowledgeArtifactJobTypes(t *testing.T) {
	artifactJobTypes := []struct {
		jobType      string
		artifactType knowledge.ArtifactType
	}{
		{"cliff_notes", knowledge.ArtifactCliffNotes},
		{"architecture_diagram", knowledge.ArtifactArchitectureDiagram},
		{"learning_path", knowledge.ArtifactLearningPath},
		{"code_tour", knowledge.ArtifactCodeTour},
		{"workflow_story", knowledge.ArtifactWorkflowStory},
	}

	for _, tc := range artifactJobTypes {
		tc := tc
		t.Run(tc.jobType, func(t *testing.T) {
			ks := knowledge.NewMemStore()
			a, err := ks.StoreKnowledgeArtifact(t.Context(), &knowledge.Artifact{
				ID:           "art-" + tc.jobType,
				RepositoryID: "repo-1",
				Type:         tc.artifactType,
				Status:       knowledge.StatusGenerating,
			})
			if err != nil {
				t.Fatal(err)
			}

			cb := buildOnJobFailedCallback(ks, nil)
			cb(&llm.Job{
				ID:           "job-" + tc.jobType,
				JobType:      tc.jobType,
				ArtifactID:   a.ID,
				ErrorCode:    "PROCESS_RESTART",
				ErrorMessage: "worker restarted",
			})

			got := ks.GetKnowledgeArtifact(t.Context(), a.ID)
			if got == nil {
				t.Fatal("artifact not found after callback")
			}
			if got.Status != knowledge.StatusFailed {
				t.Errorf("job_type=%s: status = %q, want %q", tc.jobType, got.Status, knowledge.StatusFailed)
			}
			if got.ErrorCode != "PROCESS_RESTART" {
				t.Errorf("job_type=%s: error_code = %q, want %q", tc.jobType, got.ErrorCode, "PROCESS_RESTART")
			}
		})
	}
}

// TestOnJobFailed_ArtifactIdempotency verifies that calling the callback twice
// on an already-failed artifact does not clobber the first error code.
func TestOnJobFailed_ArtifactIdempotency(t *testing.T) {
	ks := knowledge.NewMemStore()
	a, err := ks.StoreKnowledgeArtifact(t.Context(), &knowledge.Artifact{
		ID:           "art-idem",
		RepositoryID: "repo-1",
		Type:         knowledge.ArtifactCliffNotes,
		Status:       knowledge.StatusGenerating,
	})
	if err != nil {
		t.Fatal(err)
	}

	cb := buildOnJobFailedCallback(ks, nil)
	cb(&llm.Job{ID: "j1", JobType: "cliff_notes", ArtifactID: a.ID, ErrorCode: "FIRST"})
	cb(&llm.Job{ID: "j2", JobType: "cliff_notes", ArtifactID: a.ID, ErrorCode: "SECOND"})

	got := ks.GetKnowledgeArtifact(t.Context(), a.ID)
	if got.Status != knowledge.StatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.ErrorCode != "FIRST" {
		t.Errorf("error_code = %q, want %q (idempotency: second call must not overwrite)", got.ErrorCode, "FIRST")
	}
}

// TestOnJobFailed_LivingWikiJobTypes verifies that living-wiki cold-start and
// retry-excluded jobs persist a failed LivingWikiJobResult via
// persistStaleLivingWikiResult.
func TestOnJobFailed_LivingWikiJobTypes(t *testing.T) {
	lwJobTypes := []string{
		"living_wiki_cold_start",
		"living_wiki_retry_excluded",
	}
	for _, jt := range lwJobTypes {
		jt := jt
		t.Run(jt, func(t *testing.T) {
			lws := livingwiki.NewMemJobResultStore()
			cb := buildOnJobFailedCallback(nil, lws)

			repoID := "repo-lw-" + jt
			cb(&llm.Job{
				ID:              "lw-job-" + jt,
				JobType:         jt,
				Subsystem:       llm.SubsystemLivingWiki,
				TargetKey:       "lw:default:" + repoID,
				ProgressMessage: "12/50 pages complete",
			})

			result, err := lws.LastResultForRepo(t.Context(), "default", repoID)
			if err != nil {
				t.Fatal(err)
			}
			if result == nil {
				t.Fatalf("job_type=%s: expected LivingWikiJobResult to be persisted; got nil", jt)
			}
			if result.Status != "failed" {
				t.Errorf("job_type=%s: result.Status = %q, want %q", jt, result.Status, "failed")
			}
		})
	}
}

// TestOnJobFailed_NilJob verifies the callback is nil-safe.
func TestOnJobFailed_NilJob(t *testing.T) {
	cb := buildOnJobFailedCallback(knowledge.NewMemStore(), nil)
	cb(nil) // must not panic
}

// TestOnJobFailed_EmptyArtifactID verifies that artifact-type jobs with no
// ArtifactID are silently skipped.
func TestOnJobFailed_EmptyArtifactID(t *testing.T) {
	cb := buildOnJobFailedCallback(knowledge.NewMemStore(), nil)
	cb(&llm.Job{ID: "j1", JobType: "cliff_notes", ArtifactID: ""}) // must not panic
}
