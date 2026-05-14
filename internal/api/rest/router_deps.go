package rest

import "context"

// buildWorkerVersionFunc returns the WorkerVersion closure used by AppDeps and
// (via s.Deps) the GraphQL resolver. Extracted here so NewServer remains
// readable.
func buildWorkerVersionFunc(s *Server) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		if s.workerVersionLookup == nil {
			return ""
		}
		return s.workerVersionLookup.get(ctx)
	}
}
