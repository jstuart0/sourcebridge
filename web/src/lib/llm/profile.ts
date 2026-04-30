// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * Shared LLM profile types — slice 3 of the LLM provider profiles plan.
 *
 * Slice 2 introduced ProfileResponse on the admin /admin/llm page (see
 * web/src/app/(app)/admin/llm/profile-editor.tsx). Slice 3's per-repo
 * override picker also needs this shape (to render the dropdown + the
 * "API key configured" preview), so the type moves here as the single
 * source of truth and profile-editor re-exports it for back-compat
 * with existing imports.
 *
 * The shape mirrors `internal/api/rest/llm_profiles.go::ProfileResponse`
 * exactly. JSON keys are snake_case (matching the REST wire shape);
 * the GraphQL surface uses camelCase, but the per-repo override only
 * needs the REST shape — it fetches /api/v1/admin/llm-profiles directly
 * for the dropdown.
 */
export interface ProfileResponse {
  id: string;
  name: string;
  provider: string;
  base_url: string;
  api_key_set: boolean;
  api_key_hint?: string;
  summary_model: string;
  review_model: string;
  ask_model: string;
  knowledge_model: string;
  architecture_diagram_model: string;
  report_model?: string;
  draft_model: string;
  timeout_secs: number;
  advanced_mode: boolean;
  is_active: boolean;
  created_at?: string;
  updated_at?: string;
}

/**
 * Wire shape returned by GET /api/v1/admin/llm-profiles. The
 * `active_profile_missing` flag drives the admin /admin/llm repair
 * banner (slice 2) AND the per-repo override's "active profile is
 * missing" hint (slice 3 — UI uses it to disable the picker if no
 * active profile exists, since switching to a profile-mode override
 * would also be broken).
 */
export interface ListProfilesResponse {
  profiles: ProfileResponse[];
  active_profile_missing: boolean;
}
