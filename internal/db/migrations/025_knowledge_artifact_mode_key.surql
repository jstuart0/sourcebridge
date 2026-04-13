-- Migration 025: include generation mode in the artifact uniqueness key

REMOVE INDEX IF EXISTS idx_knowledge_artifact_key ON ca_knowledge_artifact;

DEFINE INDEX IF NOT EXISTS idx_knowledge_artifact_key
ON ca_knowledge_artifact FIELDS repo_id, type, audience, depth, scope_key, generation_mode UNIQUE;

UPSERT schema_version:v25 SET version = 25;
