-- Migration 043: Add progress fields to ca_repository_understanding
-- Enables the frontend to surface live build motion (percentage, phase label,
-- message) for the repository-understanding build path, mirroring the
-- progress fields already present on ca_knowledge_artifact.

DEFINE FIELD IF NOT EXISTS progress         ON ca_repository_understanding TYPE float   DEFAULT 0.0;
DEFINE FIELD IF NOT EXISTS progress_phase   ON ca_repository_understanding TYPE string  DEFAULT '';
DEFINE FIELD IF NOT EXISTS progress_message ON ca_repository_understanding TYPE string  DEFAULT '';

UPDATE ca_repository_understanding SET
  progress         = 0.0,
  progress_phase   = '',
  progress_message = ''
WHERE progress IS NONE OR progress IS NULL;

UPSERT schema_version:v43 SET version = 43;
