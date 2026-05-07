// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package clustering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/orchestrator"
)

// inputIDPattern validates repo_id, cluster_id, and symbol_id parameters
// accepted by MCP tools. Values must be 1–128 characters of [a-zA-Z0-9:_-].
var inputIDPattern = regexp.MustCompile(`^[a-zA-Z0-9:_\-]{1,128}$`)

// ValidateID returns an error if id does not match the expected shape.
func ValidateID(id string) error {
	if !inputIDPattern.MatchString(id) {
		return fmt.Errorf("invalid id %q: must match [a-zA-Z0-9:_-]{1,128}", id)
	}
	return nil
}

// Dispatcher is a thin wrapper around the orchestrator Enqueue method so
// the clustering package doesn't import the concrete orchestrator type.
type Dispatcher interface {
	Enqueue(req *llm.EnqueueRequest) (*llm.Job, error)
}

// Runner orchestrates a single clustering run for a repository.
type Runner struct {
	store      ClusterStore
	dispatcher Dispatcher
}

// NewRunner creates a Runner with the given dependencies.
func NewRunner(store ClusterStore, dispatcher Dispatcher) *Runner {
	return &Runner{store: store, dispatcher: dispatcher}
}

// EnqueueForRepo schedules an async clustering job for the given repository.
// commitSHA is used to seed the LPA RNG — the same commit always produces
// the same clusters. This method returns immediately; the job runs in the
// orchestrator's bounded worker pool.
func (r *Runner) EnqueueForRepo(repoID, commitSHA string) {
	if r == nil || r.dispatcher == nil {
		return
	}
	targetKey := fmt.Sprintf("clustering:%s", repoID)
	req := &llm.EnqueueRequest{
		Subsystem: llm.SubsystemClustering,
		JobType:   "cluster_graph",
		TargetKey: targetKey,
		Priority:  llm.PriorityMaintenance,
		RepoID:    repoID,
		RunWithContext: func(ctx context.Context, rt llm.Runtime) error {
			return r.run(ctx, rt, repoID, commitSHA)
		},
	}
	if _, err := r.dispatcher.Enqueue(req); err != nil {
		slog.Warn("clustering: failed to enqueue job",
			"repo_id", repoID, "error", err)
	}
}

// NewEnqueueHook returns a function suitable for use as a post-index hook.
// When called with (repoID, commitSHA), it enqueues an async clustering job
// and returns immediately so the indexing pipeline is not blocked.
func NewEnqueueHook(store ClusterStore, dispatcher Dispatcher) func(repoID, commitSHA string) {
	if store == nil || dispatcher == nil {
		return func(_, _ string) {}
	}
	r := NewRunner(store, dispatcher)
	return func(repoID, commitSHA string) {
		r.EnqueueForRepo(repoID, commitSHA)
	}
}

// NewOrchestratorDispatcher wraps the concrete orchestrator so the clustering
// package only imports the llm/orchestrator package for the concrete type.
// Call sites that have a *orchestrator.Orchestrator use this.
func NewOrchestratorDispatcher(o *orchestrator.Orchestrator) Dispatcher {
	if o == nil {
		return nil
	}
	return o
}

// run is the job body. It executes the full clustering pipeline for one repo.
func (r *Runner) run(ctx context.Context, rt llm.Runtime, repoID, commitSHA string) error {
	rt.ReportProgress(0.05, "loading", "Loading call graph", 0)

	// 1. Load call edges.
	rawEdges := r.store.GetCallEdges(repoID)

	// 2. Compute hash of sorted edges and check against stored hash.
	currentHash := edgeSetHash(rawEdges)
	storedHash, _ := r.store.GetRepoEdgeHash(ctx, repoID)
	if storedHash != "" && storedHash == currentHash {
		slog.Info("clustering: call graph unchanged, skipping",
			"repo_id", repoID, "edge_hash", currentHash[:8])
		rt.ReportProgress(1.0, "unchanged", "Call graph unchanged — skipping re-cluster", 0)
		return nil
	}

	rt.ReportProgress(0.15, "running_lpa", "Running label propagation", 0)

	// 3. Collect all node IDs.
	nodeSet := make(map[string]struct{}, len(rawEdges)*2)
	for _, e := range rawEdges {
		nodeSet[e.CallerID] = struct{}{}
		nodeSet[e.CalleeID] = struct{}{}
	}
	nodeIDs := make([]string, 0, len(nodeSet))
	for id := range nodeSet {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs) // deterministic order

	// 4. Run LPA.
	seed := BuildSeed(repoID, commitSHA)
	lpaResult := RunLPA(rawEdges, nodeIDs, seed)

	rt.ReportProgress(0.60, "building_clusters", "Building cluster records", 0)

	// 5. Group nodes by their final label.
	groups := make(map[string][]string, len(nodeIDs)/4+1)
	for _, id := range nodeIDs {
		lbl := lpaResult.Labels[id]
		groups[lbl] = append(groups[lbl], id)
	}

	// 6. Load symbol metadata for label derivation and in-degree ranking.
	syms := r.store.GetSymbolsByIDs(nodeIDs)
	if syms == nil {
		syms = make(map[string]*graph.StoredSymbol)
	}

	// 7. Build intra-cluster edge sets for per-cluster edge_hash and in-degree.
	type intraEdgeKey struct{ src, dst string }
	intraEdges := make(map[string][]intraEdgeKey, len(groups))
	inDegree := make(map[string]int, len(nodeIDs))
	for _, e := range rawEdges {
		srcLabel := lpaResult.Labels[e.CallerID]
		dstLabel := lpaResult.Labels[e.CalleeID]
		if srcLabel == dstLabel {
			intraEdges[srcLabel] = append(intraEdges[srcLabel], intraEdgeKey{e.CallerID, e.CalleeID})
			inDegree[e.CalleeID]++
		}
	}

	// 8. Materialize Cluster records.
	now := time.Now().UTC()
	clusters := make([]Cluster, 0, len(groups))
	for canonLabel, members := range groups {
		label := deriveLabel(members, syms)
		clusterEdges := intraEdges[canonLabel]
		eh := clusterEdgeHash(func() [][2]string {
			out := make([][2]string, len(clusterEdges))
			for i, e := range clusterEdges {
				out[i] = [2]string{e.src, e.dst}
			}
			return out
		}())

		// Rank members by in-degree for the member list (stored but not
		// exposed directly — ClusterSummary uses top-N).
		sort.Slice(members, func(i, j int) bool {
			return inDegree[members[i]] > inDegree[members[j]]
		})

		cls := Cluster{
			ID:        fmt.Sprintf("cluster:%s", uuid.New().String()),
			RepoID:    repoID,
			Label:     label,
			Size:      len(members),
			EdgeHash:  eh,
			Partial:   lpaResult.Partial,
			CreatedAt: now,
			UpdatedAt: now,
		}
		for _, mid := range members {
			cls.Members = append(cls.Members, ClusterMember{
				ClusterID: cls.ID,
				SymbolID:  mid,
				RepoID:    repoID,
			})
		}
		clusters = append(clusters, cls)
	}

	rt.ReportProgress(0.80, "persisting", "Persisting clusters", 0)

	// 9. Atomic replace: delete old clusters and insert new ones in a single
	// transaction so GetClusters never returns empty mid-swap.
	if err := r.store.ReplaceClusters(ctx, repoID, clusters); err != nil {
		return fmt.Errorf("replace clusters: %w", err)
	}
	if err := r.store.SetRepoEdgeHash(ctx, repoID, currentHash); err != nil {
		slog.Warn("clustering: failed to update edge hash",
			"repo_id", repoID, "error", err)
	}

	// 10. Compute and log quality metrics.
	q := ComputeModularity(rawEdges, lpaResult.Labels)
	smin, smax, sp50, sp95 := SizeDistribution(clusters)
	slog.Info("clustering: run complete",
		"repo_id", repoID,
		"cluster_count", len(clusters),
		"iterations", lpaResult.Iterations,
		"partial", lpaResult.Partial,
		"modularity_q", q,
		"size_min", smin,
		"size_max", smax,
		"size_p50", sp50,
		"size_p95", sp95,
	)

	rt.ReportProgress(1.0, "ready", fmt.Sprintf("Clustered %d symbols into %d subsystems (Q=%.2f)", len(nodeIDs), len(clusters), q), 0)
	return nil
}

// edgeSetHash computes SHA-256 of the sorted (callerID, calleeID) pairs.
func edgeSetHash(edges []graph.CallEdge) string {
	sorted := make([]graph.CallEdge, len(edges))
	copy(sorted, edges)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CallerID != sorted[j].CallerID {
			return sorted[i].CallerID < sorted[j].CallerID
		}
		return sorted[i].CalleeID < sorted[j].CalleeID
	})
	h := sha256.New()
	for _, e := range sorted {
		h.Write([]byte(e.CallerID))
		h.Write([]byte("|"))
		h.Write([]byte(e.CalleeID))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// clusterEdgeHash computes SHA-256 of the sorted intra-cluster edges.
// Each element is [2]string{src, dst}.
func clusterEdgeHash(edges [][2]string) string {
	sorted := make([][2]string, len(edges))
	copy(sorted, edges)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i][0] != sorted[j][0] {
			return sorted[i][0] < sorted[j][0]
		}
		return sorted[i][1] < sorted[j][1]
	})
	h := sha256.New()
	for _, e := range sorted {
		h.Write([]byte(e[0]))
		h.Write([]byte("|"))
		h.Write([]byte(e[1]))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// deriveLabel returns a heuristic cluster name from the dominant package path
// prefix among its member symbols. If no package path can be inferred, it
// returns "cluster".
func deriveLabel(memberIDs []string, syms map[string]*graph.StoredSymbol) string {
	pkgFreq := make(map[string]int, len(memberIDs))
	for _, id := range memberIDs {
		sym, ok := syms[id]
		if !ok || sym == nil {
			continue
		}
		pkg := packageFromFilePath(sym.FilePath)
		if pkg != "" {
			pkgFreq[pkg]++
		}
	}
	if len(pkgFreq) == 0 {
		return "cluster"
	}
	best := ""
	bestCount := 0
	for pkg, count := range pkgFreq {
		if count > bestCount || (count == bestCount && pkg < best) {
			best = pkg
			bestCount = count
		}
	}
	return best
}

// packageFromFilePath extracts the package/directory name from a file path.
// "internal/auth/session.go" → "auth"
// "auth.py" → "auth"
func packageFromFilePath(filePath string) string {
	// Strip extension.
	if i := strings.LastIndexByte(filePath, '.'); i > 0 {
		filePath = filePath[:i]
	}
	// Take the last path component of the directory.
	dir := filePath
	if i := strings.LastIndexByte(filePath, '/'); i >= 0 {
		dir = filePath[:i]
		if j := strings.LastIndexByte(dir, '/'); j >= 0 {
			dir = dir[j+1:]
		}
	} else {
		// No slash — the file is at root; use the filename stem.
		return filePath
	}
	return dir
}
