---
sourcebridge:
    page_id: sourcebridge.glossary
    template: glossary
    audience: for-engineers
    dependencies:
        dependency_scope: direct
---
<!-- sourcebridge:block id="bb2bd907b303f" kind="heading" owner="generated" -->
## internal/auth
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b00cf17876f67" kind="paragraph" owner="generated" -->
**Init** `func Init(cfg Config) error` (internal/auth/auth.go:22-41)\
Init initialises the authentication subsystem. Must be called once before any handler is registered. Returns an error if the configuration is invalid or the token store is unreachable.
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b56565d9af518" kind="paragraph" owner="generated" -->
**Middleware** `func Middleware(next http.Handler) http.Handler` (internal/auth/middleware.go:10-35)\
Middleware wraps next and enforces authentication on every request. Unauthenticated requests receive a 401 response. Panics if Init has not been called.
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b73e710da3e39" kind="paragraph" owner="generated" -->
**RequireRole** `func RequireRole(role string) func(http.Handler) http.Handler` (internal/auth/role.go:18-44)\
RequireRole returns a middleware that enforces that the authenticated user holds role. Returns 403 when the user lacks the role. Panics if Init has not been called.
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b9c7af0c5b332" kind="heading" owner="generated" -->
## internal/jobs
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b4112f56263a2" kind="paragraph" owner="generated" -->
**Dispatcher** `type Dispatcher struct` (internal/jobs/dispatcher.go:12-28)\
Dispatcher manages the bounded worker pool. MaxConcurrency (default 8) limits simultaneous job execution. Callers enqueue via Submit; the call blocks when the pool is full.
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b27d844a8464b" kind="paragraph" owner="generated" -->
**Submit** `func (d *Dispatcher) Submit(ctx context.Context, job Job) error` (internal/jobs/dispatcher.go:44-62)\
Submit enqueues job for execution. Blocks until a worker slot is available or ctx is cancelled. Returns ctx.Err() on cancellation; never drops a job silently.
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="ba3656ef08d70" kind="heading" owner="generated" -->
## internal/qa
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bb98e8fb9c664" kind="paragraph" owner="generated" -->
**Run** `func Run(ctx context.Context, req Request) (Result, error)` (internal/qa/qa.go:31-78)\
Run executes an agentic QA session against the indexed codebase. The session is bounded by req.Budget; results include citations in the (path:start-end) format.
<!-- /sourcebridge:block -->
