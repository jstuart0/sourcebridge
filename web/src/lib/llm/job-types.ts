/**
 * Canonical LLM-job shape returned by /api/v1/admin/llm/activity (and the
 * per-repo filter variant used by RepoJobsPopover). Exists so the popover,
 * the admin monitor, and the repository page can share the same progress
 * UI without each redeclaring an almost-identical structural type.
 *
 * Optional fields reflect responses where the orchestrator hasn't yet
 * populated them (e.g. queue_position only exists while pending). Anything
 * required here is always present in the activity payload.
 */
export type JobStatus = "pending" | "generating" | "ready" | "failed" | "cancelled";

export interface LLMJobView {
  id: string;
  subsystem: string;
  job_type: string;
  status: JobStatus;
  progress: number;
  progress_phase?: string;
  progress_message?: string;
  error_code?: string;
  error_message?: string;
  error_title?: string;
  error_hint?: string;
  retry_count?: number;
  max_attempts?: number;
  attached_requests?: number;
  reused_summaries?: number;
  leaf_cache_hits?: number;
  file_cache_hits?: number;
  package_cache_hits?: number;
  root_cache_hits?: number;
  cached_nodes_loaded?: number;
  total_nodes?: number;
  resume_stage?: string;
  skipped_leaf_units?: number;
  skipped_file_units?: number;
  skipped_package_units?: number;
  skipped_root_units?: number;
  artifact_id?: string;
  repo_id?: string;
  queue_position?: number;
  queue_depth?: number;
  estimated_wait_ms?: number;
  generation_mode?: "classic" | "understanding_first";
  priority?: "interactive" | "maintenance" | "prewarm";
  elapsed_ms: number;
  updated_at: string;
  created_at?: string;
  started_at?: string;
  completed_at?: string;
}
