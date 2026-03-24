-- Add progress field to knowledge artifacts for generation tracking
DEFINE FIELD IF NOT EXISTS progress ON ca_knowledge_artifact TYPE float DEFAULT 0.0;
UPSERT schema_version:v5 SET version = 5;
