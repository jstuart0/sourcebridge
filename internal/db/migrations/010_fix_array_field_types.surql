-- Fix generic option<array> fields that silently drop data.
-- SurrealDB requires typed arrays (array<string>, array<float>, etc.) for SCHEMAFULL tables.

-- Requirement tags and acceptance criteria must be string arrays.
REMOVE FIELD IF EXISTS tags ON ca_requirement;
DEFINE FIELD tags ON ca_requirement TYPE option<array<string>> PERMISSIONS FULL;

REMOVE FIELD IF EXISTS acceptance_criteria ON ca_requirement;
DEFINE FIELD acceptance_criteria ON ca_requirement TYPE option<array<string>> PERMISSIONS FULL;

-- Embedding vectors must be float arrays.
REMOVE FIELD IF EXISTS vector ON ca_embedding;
DEFINE FIELD vector ON ca_embedding TYPE option<array<float>> PERMISSIONS FULL;
