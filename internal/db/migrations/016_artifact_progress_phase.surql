-- Migration 016: Add progress_phase and progress_message to knowledge artifacts
-- Enables the frontend to display a human-readable phase label under the
-- progress bar instead of just a number (Phase 5 streaming progress).

DEFINE FIELD IF NOT EXISTS progress_phase ON ca_knowledge_artifact TYPE string DEFAULT '';
DEFINE FIELD IF NOT EXISTS progress_message ON ca_knowledge_artifact TYPE string DEFAULT '';

UPDATE ca_knowledge_artifact SET
  progress_phase = '',
  progress_message = ''
WHERE progress_phase = NONE OR progress_phase = NULL;

UPSERT schema_version:v16 SET version = 16;
