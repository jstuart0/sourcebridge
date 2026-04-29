-- 045_lw_repo_llm_override.surql
--
-- Adds the optional `living_wiki_llm_override` field to lw_repo_settings.
-- The field holds a sub-record with provider, base_url, api_key_cipher
-- (sbenc:v1-encrypted), model, updated_at, updated_by. Slice 5 of the
-- workspace-LLM-source-of-truth plan.
--
-- SurrealDB tolerates missing fields on read; existing rows decode with
-- the override absent (resolver falls through to workspace settings).
-- The field is option<object> so explicit clearing via SET = NONE works.

DEFINE FIELD IF NOT EXISTS living_wiki_llm_override ON lw_repo_settings TYPE option<object>;
