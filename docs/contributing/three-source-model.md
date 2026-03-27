# Three-Source Model for Code Analysis

SourceBridge uses three distinct sources of code content for AI analysis.
Every feature must declare which source it uses, and the UI must make this
clear to the user.

## Sources

### 1. Indexed Repository Snapshot

- **What:** The last-indexed state of the repository stored in the graph
  database. Includes symbols, call edges, file metadata, and requirements.
- **When used:** Cliff Notes, Learning Paths, Code Tours, Workflow Stories,
  Symbol Impact Analysis, Execution Paths.
- **Freshness:** Updated on each indexing run. May lag behind the latest
  commit. The `stale` flag on artifacts indicates when the source revision
  has changed since generation.
- **UI label:** `Indexed repository view`

### 2. Local Editor Buffer

- **What:** The current unsaved contents of the user's active editor tab,
  sent from VS Code or JetBrains via the `code` field on `DiscussCodeInput`
  or `ReviewCodeInput`.
- **When used:** DiscussCode (when `code` is provided), ReviewCode (when
  `code` is provided).
- **Freshness:** Real-time — reflects the exact text the user is editing.
- **UI label:** `Using current editor contents`

### 3. Server File Read

- **What:** The on-disk file content read from the repository working
  directory on the server. Used as a fallback when no editor buffer is
  provided.
- **When used:** ReviewCode (when `code` is not provided), DiscussCode
  (when `code` is not provided but `filePath` is).
- **Freshness:** Reflects the server's filesystem state, which may differ
  from both the index and the user's local editor.
- **UI label:** `Using server file` or no special label (default).

## Resolution Priority

For features that accept a `code` field:

1. Use `code` (editor buffer) if provided and non-empty
2. Fall back to server file read via `filePath`
3. If neither is available, return an error

For knowledge features (Cliff Notes, etc.):

1. Always use the indexed snapshot — no editor buffer support
2. The snapshot is assembled from the graph store, not read from disk

## Implementation Rules

- The `DiscussCodeInput.code` field sends the editor buffer to the worker
  as `context_code` in the `AnswerQuestionRequest` proto
- The `ReviewCodeInput.code` field is used as the review content directly
- Knowledge features never receive editor buffer content
- Every result panel should indicate which source was used
- IDE extensions should always send `code` when the user has an active
  editor tab open
