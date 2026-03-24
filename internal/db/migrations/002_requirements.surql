-- Migration 002: Requirements and traceability
-- Adds requirement tables, linking, and review results

-- Requirements
DEFINE TABLE IF NOT EXISTS requirement SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS org ON requirement TYPE record<organization>;
DEFINE FIELD IF NOT EXISTS external_id ON requirement TYPE option<string>;
DEFINE FIELD IF NOT EXISTS source ON requirement TYPE string;
DEFINE FIELD IF NOT EXISTS source_url ON requirement TYPE option<string>;
DEFINE FIELD IF NOT EXISTS title ON requirement TYPE string;
DEFINE FIELD IF NOT EXISTS description ON requirement TYPE string;
DEFINE FIELD IF NOT EXISTS acceptance_criteria ON requirement TYPE option<array<string>>;
DEFINE FIELD IF NOT EXISTS priority ON requirement TYPE option<string>;
DEFINE FIELD IF NOT EXISTS status ON requirement TYPE string DEFAULT 'active';
DEFINE FIELD IF NOT EXISTS metadata ON requirement TYPE object DEFAULT {};
DEFINE FIELD IF NOT EXISTS created_at ON requirement TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS updated_at ON requirement TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_req_org ON requirement FIELDS org;
DEFINE INDEX IF NOT EXISTS idx_req_external ON requirement FIELDS org, external_id;

-- Requirement → Code traceability
DEFINE TABLE IF NOT EXISTS implements TYPE RELATION FROM function | class | file TO requirement;
DEFINE FIELD IF NOT EXISTS confidence ON implements TYPE float;
DEFINE FIELD IF NOT EXISTS source ON implements TYPE string;
DEFINE FIELD IF NOT EXISTS verified ON implements TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS verified_by ON implements TYPE option<record<user>>;
DEFINE FIELD IF NOT EXISTS verified_at ON implements TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS snapshot ON implements TYPE record<snapshot>;
DEFINE FIELD IF NOT EXISTS model_version ON implements TYPE option<string>;
DEFINE FIELD IF NOT EXISTS superseded_at ON implements TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS created_at ON implements TYPE datetime DEFAULT time::now();

DEFINE TABLE IF NOT EXISTS partially_implements TYPE RELATION FROM function | class | file TO requirement;
DEFINE FIELD IF NOT EXISTS confidence ON partially_implements TYPE float;
DEFINE FIELD IF NOT EXISTS source ON partially_implements TYPE string;
DEFINE FIELD IF NOT EXISTS verified ON partially_implements TYPE bool DEFAULT false;
DEFINE FIELD IF NOT EXISTS verified_by ON partially_implements TYPE option<record<user>>;
DEFINE FIELD IF NOT EXISTS verified_at ON partially_implements TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS snapshot ON partially_implements TYPE record<snapshot>;
DEFINE FIELD IF NOT EXISTS model_version ON partially_implements TYPE option<string>;
DEFINE FIELD IF NOT EXISTS superseded_at ON partially_implements TYPE option<datetime>;
DEFINE FIELD IF NOT EXISTS created_at ON partially_implements TYPE datetime DEFAULT time::now();

-- Requirement → Test traceability
DEFINE TABLE IF NOT EXISTS tested_by TYPE RELATION FROM requirement TO test;
DEFINE FIELD IF NOT EXISTS confidence ON tested_by TYPE float;
DEFINE FIELD IF NOT EXISTS source ON tested_by TYPE string;

DEFINE TABLE IF NOT EXISTS validates TYPE RELATION FROM test TO requirement;
DEFINE FIELD IF NOT EXISTS confidence ON validates TYPE float;

-- PR impact
DEFINE TABLE IF NOT EXISTS pull_request SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS repo ON pull_request TYPE record<repository>;
DEFINE FIELD IF NOT EXISTS provider_id ON pull_request TYPE string;
DEFINE FIELD IF NOT EXISTS number ON pull_request TYPE int;
DEFINE FIELD IF NOT EXISTS title ON pull_request TYPE string;
DEFINE FIELD IF NOT EXISTS author ON pull_request TYPE string;
DEFINE FIELD IF NOT EXISTS status ON pull_request TYPE string;
DEFINE FIELD IF NOT EXISTS files_changed ON pull_request TYPE array<string>;
DEFINE FIELD IF NOT EXISTS analysis ON pull_request TYPE option<object>;
DEFINE FIELD IF NOT EXISTS created_at ON pull_request TYPE datetime DEFAULT time::now();

DEFINE TABLE IF NOT EXISTS impacted_by TYPE RELATION FROM requirement TO pull_request;
DEFINE FIELD IF NOT EXISTS impact_type ON impacted_by TYPE string;

-- Review Results
DEFINE TABLE IF NOT EXISTS review_result SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS target ON review_result TYPE record;
DEFINE FIELD IF NOT EXISTS repo ON review_result TYPE record<repository>;
DEFINE FIELD IF NOT EXISTS template ON review_result TYPE string;
DEFINE FIELD IF NOT EXISTS findings ON review_result TYPE array<object>;
DEFINE FIELD IF NOT EXISTS score ON review_result TYPE option<float>;
DEFINE FIELD IF NOT EXISTS created_at ON review_result TYPE datetime DEFAULT time::now();
DEFINE FIELD IF NOT EXISTS created_by ON review_result TYPE record<user>;

-- Embeddings
DEFINE TABLE IF NOT EXISTS embedding SCHEMAFULL;
DEFINE FIELD IF NOT EXISTS target ON embedding TYPE record;
DEFINE FIELD IF NOT EXISTS vector ON embedding TYPE array<float>;
DEFINE FIELD IF NOT EXISTS dimension ON embedding TYPE int;
DEFINE FIELD IF NOT EXISTS model ON embedding TYPE string;
DEFINE FIELD IF NOT EXISTS text_hash ON embedding TYPE string;
DEFINE FIELD IF NOT EXISTS created_at ON embedding TYPE datetime DEFAULT time::now();
DEFINE INDEX IF NOT EXISTS idx_embedding_target ON embedding FIELDS target;

-- Record migration (idempotent via fixed record ID)
UPSERT schema_version:v2 SET version = 2;
