-- Migration 034: symbol embedding column + HNSW index.
--
-- Part of the hybrid retrieval Phase 2 rollout. Adds the vector
-- storage and ANN index the search service uses to compute semantic
-- similarity against query embeddings.
--
-- Dimension rationale:
--
--   - The default embedder models (nomic-embed-text for Ollama and
--     text-embedding-3-small for OpenAI-compatible backends) both
--     produce 768-dim vectors. Operators running a different model
--     can REMOVE INDEX + DEFINE INDEX at the new dimension as part
--     of a generation swap; the embedding_dim column on the row
--     lets the write path refuse mismatched writes so the index and
--     stored vectors stay in sync.
--
-- Distance rationale:
--
--   - COSINE similarity is appropriate for embedding-model outputs
--     (direction matters, magnitude is noise). It also matches the
--     vector::similarity::cosine function used in search queries.
--
-- Idempotency:
--
--   - DEFINE FIELD IF NOT EXISTS + DEFINE INDEX IF NOT EXISTS keep
--     replays safe. Fresh installs get the index from the start;
--     upgrading installs get it on first startup after this file
--     ships.
--
-- Rollback story:
--
--   - REMOVE INDEX idx_ca_symbol_vec ON ca_symbol. Embeddings
--     remain on the row but are no longer queryable. The search
--     service detects the empty / erroring query and falls back to
--     FTS-only cleanly.
--
-- See thoughts/shared/plans/2026-04-22-hybrid-retrieval-search.md
-- §Phase 2.

DEFINE FIELD IF NOT EXISTS embedding       ON ca_symbol TYPE option<array<float>>;
DEFINE FIELD IF NOT EXISTS embedding_model ON ca_symbol TYPE option<string>;
DEFINE FIELD IF NOT EXISTS embedding_dim   ON ca_symbol TYPE option<int>;
DEFINE FIELD IF NOT EXISTS embedding_hash  ON ca_symbol TYPE option<string>;

DEFINE INDEX IF NOT EXISTS idx_ca_symbol_vec
    ON ca_symbol
    FIELDS embedding
    HNSW DIMENSION 768 DIST COSINE;

UPSERT schema_version:v34 SET version = 34;
