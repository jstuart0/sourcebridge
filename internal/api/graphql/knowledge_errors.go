package graphql

import (
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	knowledgepkg "github.com/sourcebridge/sourcebridge/internal/knowledge"
)

func classifyError(err error) string {
	if err == nil {
		return ""
	}
	if st, ok := grpcstatus.FromError(err); ok {
		switch st.Code() {
		case codes.DeadlineExceeded:
			return "DEADLINE_EXCEEDED"
		case codes.Unavailable:
			return "WORKER_UNAVAILABLE"
		}
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "llm returned empty content"):
		return "LLM_EMPTY"
	case strings.Contains(msg, "snapshot too large"), strings.Contains(msg, "exceeds budget"):
		return "SNAPSHOT_TOO_LARGE"
	case strings.Contains(msg, "deadline exceeded"):
		return "DEADLINE_EXCEEDED"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "transport is closing"), strings.Contains(msg, "unavailable"):
		return "WORKER_UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}

func persistArtifactFailure(store knowledgepkg.KnowledgeStore, artifactID string, err error) {
	if store == nil || artifactID == "" || err == nil {
		return
	}
	code := classifyError(err)
	_ = store.SetArtifactFailed(artifactID, code, err.Error())
}

const staleGenerationThreshold = 60 * time.Second

func isInFlightGeneration(existing *knowledgepkg.Artifact) bool {
	if existing == nil {
		return false
	}
	if existing.Status != knowledgepkg.StatusGenerating && existing.Status != knowledgepkg.StatusPending {
		return false
	}
	return time.Since(existing.UpdatedAt) < staleGenerationThreshold
}
