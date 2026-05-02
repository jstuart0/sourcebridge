# Architecture: Block Count Fixture

The job scheduler (internal/scheduler/scheduler.go:1-70) manages background
task execution across the worker pool (internal/scheduler/pool.go:1-50).
Tasks are dispatched to idle workers through a thread-safe channel in
first-in-first-out order. The scheduler tracks in-flight task counts to
enforce the configured concurrency limit and apply backpressure when the
limit is reached. Completed tasks produce a result that the caller can
observe through a future-like handle returned at submission time.
