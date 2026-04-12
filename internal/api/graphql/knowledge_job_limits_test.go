package graphql

import (
	"os"
	"testing"
)

func TestKnowledgeJobBaseTypeStripsPrefixes(t *testing.T) {
	tests := map[string]string{
		"cliff_notes":           "cliff_notes",
		"seed:cliff_notes":      "cliff_notes",
		"refresh:workflow_story": "workflow_story",
	}
	for input, want := range tests {
		if got := knowledgeJobBaseType(input); got != want {
			t.Fatalf("knowledgeJobBaseType(%q)=%q want %q", input, got, want)
		}
	}
}

func TestKnowledgeJobConcurrencyLimitUsesEnvOverride(t *testing.T) {
	t.Setenv("SOURCEBRIDGE_KNOWLEDGE_CLIFF_NOTES_MAX_CONCURRENCY", "3")
	if got := knowledgeJobConcurrencyLimit("seed:cliff_notes"); got != 3 {
		t.Fatalf("expected env override 3, got %d", got)
	}
}

func TestKnowledgeJobConcurrencyLimitDefaults(t *testing.T) {
	for _, key := range []string{
		"SOURCEBRIDGE_KNOWLEDGE_CLIFF_NOTES_MAX_CONCURRENCY",
		"SOURCEBRIDGE_KNOWLEDGE_LEARNING_PATH_MAX_CONCURRENCY",
		"SOURCEBRIDGE_KNOWLEDGE_CODE_TOUR_MAX_CONCURRENCY",
		"SOURCEBRIDGE_KNOWLEDGE_WORKFLOW_STORY_MAX_CONCURRENCY",
		"SOURCEBRIDGE_KNOWLEDGE_DEFAULT_MAX_CONCURRENCY",
	} {
		_ = os.Unsetenv(key)
	}
	if got := knowledgeJobConcurrencyLimit("workflow_story"); got != 2 {
		t.Fatalf("expected workflow_story default 2, got %d", got)
	}
	if got := knowledgeJobConcurrencyLimit("learning_path"); got != 1 {
		t.Fatalf("expected learning_path default 1, got %d", got)
	}
}
