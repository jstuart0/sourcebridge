---
sourcebridge:
    page_id: sourcebridge.activity_log
    template: activity_log
    audience: for-engineers
    dependencies:
        dependency_scope: direct
---
<!-- sourcebridge:block id="bd0672b671426" kind="heading" owner="generated" -->
## Week of 20 April 2026
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bd65897e92c4e" kind="table" owner="generated" -->
| Author | Commits | Files changed | Packages |
| --- | --- | --- | --- |
| alice | 2 | 14 | internal/auth |
| bob | 1 | 3 | internal/api, internal/auth |
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b78dbac6fee3a" kind="paragraph" owner="generated" -->
This week the team shipped the RequireRole middleware for route-level access control (internal/auth/role.go:18-44), patched a nil-pointer panic in RequireRole when the JWT claim was absent, and merged the JWT library migration to golang-jwt v5. 3 commits, 17 files changed, net -49 lines.

The JWT library migration was the most impactful change: legacy-jwt v2 is fully removed, token format changed from RS256 to ES256. Existing sessions invalidated on first deploy. The RequireRole middleware is covered by tests; the panic fix adds 2 regression tests to internal/auth/role_test.go.
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bbeedc9a02f90" kind="heading" owner="generated" -->
## Week of 13 April 2026
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b26c03fc6d1da" kind="table" owner="generated" -->
| Author | Commits | Files changed | Packages |
| --- | --- | --- | --- |
| bob | 1 | 8 | internal/jobs |
| carol | 1 | 1 |  |
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b3a26633cad4a" kind="callout" owner="generated" -->
> **WARNING:** **BREAKING CHANGE** in this week:
> `e5f6007` — feat: add job dispatcher to internal/jobs
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b6178cda260c6" kind="paragraph" owner="generated" -->
This week the team landed the initial internal/jobs.Dispatcher package — a bounded worker pool replacing the old internal/worker package. 2 commits, 9 files changed, net +215 lines.

BREAKING CHANGE: internal/worker is removed. Callers must migrate to internal/jobs.Dispatcher. The new package exposes Submit (blocking until a slot is available) and enforces MaxConcurrency at 8 by default. A go.sum update from carol reflects updated indirect dependencies.
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b58bda1f1a4b2" kind="heading" owner="generated" -->
## Week of 6 April 2026
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b356e1b021232" kind="table" owner="generated" -->
| Author | Commits | Files changed | Packages |
| --- | --- | --- | --- |
| carol | 1 | 0 |  |
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b78a9aab38a69" kind="paragraph" owner="generated" -->
This week carol committed the event-driven architecture decision record for order processing. 1 commit, 0 files changed (documentation-only).

No code changes landed this week. The ADR documents the shift to Kafka-backed order processing and is referenced by the order service implementation work tracked separately.
<!-- /sourcebridge:block -->
