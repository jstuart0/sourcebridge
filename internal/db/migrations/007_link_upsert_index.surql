-- Composite unique index to enforce one link per (repo, requirement, symbol) triple.
-- Prevents duplicates if auto-link is re-run without clearing existing links.
DEFINE INDEX IF NOT EXISTS idx_ca_link_unique
  ON ca_link FIELDS repo_id, requirement_id, symbol_id UNIQUE;
