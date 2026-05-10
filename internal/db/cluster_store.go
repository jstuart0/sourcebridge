// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/models"

	"github.com/sourcebridge/sourcebridge/internal/clustering"
)

// ---------------------------------------------------------------------------
// ClusterStore methods (satisfies clustering.ClusterStore implicitly)
// ---------------------------------------------------------------------------

// surrealCluster is the SurrealDB representation of a cluster record.
type surrealCluster struct {
	ID        *models.RecordID `json:"id,omitempty"`
	RepoID    string           `json:"repo_id"`
	Label     string           `json:"label"`
	LLMLabel  *string          `json:"llm_label,omitempty"`
	Size      int              `json:"size"`
	EdgeHash  string           `json:"edge_hash"`
	Partial   bool             `json:"partial"`
	CreatedAt surrealTime      `json:"created_at"`
	UpdatedAt surrealTime      `json:"updated_at"`
}

func (c *surrealCluster) toCluster() clustering.Cluster {
	return clustering.Cluster{
		ID:        recordIDString(c.ID),
		RepoID:    c.RepoID,
		Label:     c.Label,
		LLMLabel:  c.LLMLabel,
		Size:      c.Size,
		EdgeHash:  c.EdgeHash,
		Partial:   c.Partial,
		CreatedAt: c.CreatedAt.Time,
		UpdatedAt: c.UpdatedAt.Time,
	}
}

// (surrealClusterMember used to live here as the row shape for inline
// cluster_member ops; the current ReplaceClusters flow batches inserts
// via raw SurrealDB statements and doesn't materialise the row type in
// Go. Removed to satisfy lint.)

// GetRepoEdgeHash returns the cluster_graph_edge_hash stored on the
// ca_repository record, or an empty string when the field is absent.
func (s *SurrealStore) GetRepoEdgeHash(ctx context.Context, repoID string) (string, error) {
	db := s.client.DB()
	if db == nil {
		return "", nil
	}
	type hashRow struct {
		Hash string `json:"cluster_graph_edge_hash"`
	}
	rows, err := queryOne[[]hashRow](ctx, db,
		`SELECT cluster_graph_edge_hash FROM type::thing('ca_repository', $id)`,
		map[string]any{"id": repoID})
	if err != nil || len(rows) == 0 {
		return "", nil
	}
	return rows[0].Hash, nil
}

// SetRepoEdgeHash writes the cluster_graph_edge_hash onto the ca_repository
// record so future clustering runs can skip unchanged graphs.
func (s *SurrealStore) SetRepoEdgeHash(ctx context.Context, repoID, hash string) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	_, err := surrealdb.Query[interface{}](ctx, db,
		`UPDATE type::thing('ca_repository', $id) SET cluster_graph_edge_hash = $hash`,
		map[string]any{"id": repoID, "hash": hash})
	return err
}

// ReplaceClusters atomically deletes all existing clusters for the repository
// and inserts the new set in a single BEGIN/COMMIT transaction batch. Readers
// therefore never observe an empty window between the delete and the insert.
//
// Implementation note: SurrealDB's Go SDK does not expose a native transaction
// API over the WebSocket protocol; however, multi-statement queries issued as a
// single Query call are wrapped in BEGIN/COMMIT by the server and execute
// atomically. We exploit this by composing the full delete + insert as one
// batch query, passing cluster and member rows as JSON arrays via $clusters and
// $members parameters iterated with FOR loops.
func (s *SurrealStore) ReplaceClusters(ctx context.Context, repoID string, clusters []clustering.Cluster) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	now := time.Now().UTC()

	// Build flat arrays for the BEGIN/COMMIT batch.
	//
	// clusterContent omits the "cid" key that we use in type::thing() to set
	// the record ID. In a SCHEMAFULL table, CONTENT must not include fields
	// that are not defined in the schema; the record ID is supplied separately
	// via the type::thing() expression in the CREATE statement. llm_label uses
	// omitempty so that nil *string serialises as absent-key rather than JSON
	// null — SurrealDB v2.2+ rejects explicit null for option<string> fields.
	type clusterContent struct {
		RepoID    string    `json:"repo_id"`
		Label     string    `json:"label"`
		LLMLabel  *string   `json:"llm_label,omitempty"`
		Size      int       `json:"size"`
		EdgeHash  string    `json:"edge_hash"`
		Partial   bool      `json:"partial"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	// clusterID pairs each content object with the raw ID used in type::thing.
	type clusterID struct {
		Cid     string         `json:"cid"`
		Content clusterContent `json:"content"`
	}
	type memberRow struct {
		ClusterID string `json:"cid"`
		SymbolID  string `json:"symbol_id"`
		RepoID    string `json:"repo_id"`
	}

	clusterRows := make([]clusterID, 0, len(clusters))
	memberRows := make([]memberRow, 0)
	for _, c := range clusters {
		rawID := strings.TrimPrefix(c.ID, "cluster:")
		if rawID == "" {
			rawID = uuid.New().String()
		}
		clusterRows = append(clusterRows, clusterID{
			Cid: rawID,
			Content: clusterContent{
				RepoID:    repoID,
				Label:     c.Label,
				LLMLabel:  c.LLMLabel,
				Size:      c.Size,
				EdgeHash:  c.EdgeHash,
				Partial:   c.Partial,
				CreatedAt: now,
				UpdatedAt: now,
			},
		})
		for _, m := range c.Members {
			memberRows = append(memberRows, memberRow{
				ClusterID: rawID,
				SymbolID:  m.SymbolID,
				RepoID:    repoID,
			})
		}
	}

	// Use CONTENT $c.content instead of SET ... so that absent keys (e.g.
	// llm_label when LLMLabel is nil and omitempty drops it) are absent from
	// the created record by construction. A SET llm_label = $c.llm_label
	// statement assigns NONE to the field when the key is missing, which
	// SurrealDB v2.2+ rejects for option<string> inside a transactional
	// FOR-loop.
	_, err := surrealdb.Query[interface{}](ctx, db,
		`BEGIN;
		 DELETE cluster_member WHERE repo_id = $repo_id;
		 DELETE cluster        WHERE repo_id = $repo_id;
		 FOR $c IN $clusters {
		   CREATE type::thing('cluster', $c.cid) CONTENT $c.content;
		 };
		 FOR $m IN $members {
		   CREATE cluster_member SET
		     cluster_id = type::thing('cluster', $m.cid),
		     symbol_id  = $m.symbol_id,
		     repo_id    = $m.repo_id;
		 };
		 COMMIT;`,
		map[string]any{
			"repo_id":  repoID,
			"clusters": clusterRows,
			"members":  memberRows,
		})
	if err != nil {
		return fmt.Errorf("clustering: transactional replace failed: %w", err)
	}
	return nil
}

// SaveClusters persists a full set of clusters and their members for a
// repository. The caller is responsible for calling DeleteClusters first when
// replacing an existing set. Prefer ReplaceClusters for full replacements.
func (s *SurrealStore) SaveClusters(ctx context.Context, repoID string, clusters []clustering.Cluster) error {
	db := s.client.DB()
	if db == nil {
		return fmt.Errorf("database not connected")
	}
	now := time.Now().UTC()
	for _, c := range clusters {
		clusterUUID := uuid.New().String()
		// Strip the "cluster:" prefix that the job assigns so we can use
		// SurrealDB's type::thing syntax cleanly.
		rawID := strings.TrimPrefix(c.ID, "cluster:")
		if rawID == "" {
			rawID = clusterUUID
		}
		// Build the vars map conditionally: include llm_label only when
		// non-nil, and dereference the pointer when included. Passing a
		// nil *string as $llm_label would produce SET llm_label = null,
		// which SurrealDB v2.2+ rejects for option<string> columns.
		// When nil, omit the field from both vars and the SET clause so
		// SurrealDB leaves it absent (permitted for option<string>).
		vars := map[string]any{
			"cid":        rawID,
			"repo_id":    repoID,
			"label":      c.Label,
			"size":       c.Size,
			"edge_hash":  c.EdgeHash,
			"partial":    c.Partial,
			"created_at": now,
			"updated_at": now,
		}
		sql := `CREATE type::thing('cluster', $cid) SET
				repo_id    = $repo_id,
				label      = $label,
				size       = $size,
				edge_hash  = $edge_hash,
				partial    = $partial,
				created_at = $created_at,
				updated_at = $updated_at`
		if c.LLMLabel != nil {
			vars["llm_label"] = *c.LLMLabel
			sql += `, llm_label = $llm_label`
		}
		_, err := surrealdb.Query[interface{}](ctx, db, sql, vars)
		if err != nil {
			slog.Warn("clustering: failed to save cluster", "repo_id", repoID, "error", err)
			continue
		}
		// Persist members.
		for _, m := range c.Members {
			_, _ = surrealdb.Query[interface{}](ctx, db,
				`CREATE cluster_member SET
					cluster_id = type::thing('cluster', $cid),
					symbol_id  = $symbol_id,
					repo_id    = $repo_id`,
				map[string]any{
					"cid":       rawID,
					"symbol_id": m.SymbolID,
					"repo_id":   repoID,
				})
		}
	}
	return nil
}

// GetClusters returns all clusters for a repository without member lists.
func (s *SurrealStore) GetClusters(ctx context.Context, repoID string) ([]clustering.Cluster, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}
	rows, err := queryOne[[]surrealCluster](ctx, db,
		`SELECT * FROM cluster WHERE repo_id = $repo_id ORDER BY size DESC`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		return nil, nil
	}
	out := make([]clustering.Cluster, len(rows))
	for i, r := range rows {
		out[i] = r.toCluster()
	}
	return out, nil
}

// GetClusterByID returns a single cluster including its full member list.
func (s *SurrealStore) GetClusterByID(ctx context.Context, clusterID string) (*clustering.Cluster, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}
	// Strip leading "cluster:" prefix if present.
	rawID := strings.TrimPrefix(clusterID, "cluster:")

	rows, err := queryOne[[]surrealCluster](ctx, db,
		`SELECT * FROM type::thing('cluster', $id)`,
		map[string]any{"id": rawID})
	if err != nil || len(rows) == 0 {
		return nil, nil
	}
	c := rows[0].toCluster()

	// Load members.
	type memberRow struct {
		SymbolID string `json:"symbol_id"`
		RepoID   string `json:"repo_id"`
	}
	members, _ := queryOne[[]memberRow](ctx, db,
		`SELECT symbol_id, repo_id FROM cluster_member WHERE cluster_id = type::thing('cluster', $id)`,
		map[string]any{"id": rawID})
	for _, m := range members {
		c.Members = append(c.Members, clustering.ClusterMember{
			ClusterID: clusterID,
			SymbolID:  m.SymbolID,
			RepoID:    m.RepoID,
		})
	}
	return &c, nil
}

// GetClusterForSymbol returns the cluster containing the given symbol.
func (s *SurrealStore) GetClusterForSymbol(ctx context.Context, repoID, symbolID string) (*clustering.Cluster, error) {
	db := s.client.DB()
	if db == nil {
		return nil, nil
	}
	type memberRow struct {
		ClusterID *models.RecordID `json:"cluster_id"`
	}
	rows, err := queryOne[[]memberRow](ctx, db,
		`SELECT cluster_id FROM cluster_member WHERE symbol_id = $sid AND repo_id = $repo_id LIMIT 1`,
		map[string]any{"sid": symbolID, "repo_id": repoID})
	if err != nil || len(rows) == 0 || rows[0].ClusterID == nil {
		return nil, nil
	}
	cid := recordIDString(rows[0].ClusterID)
	return s.GetClusterByID(ctx, cid)
}

// DeleteClusters removes all cluster and cluster_member records for a
// repository. Called on repo deletion and at the start of each re-cluster run.
func (s *SurrealStore) DeleteClusters(ctx context.Context, repoID string) error {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	_, err := surrealdb.Query[interface{}](ctx, db,
		`DELETE cluster_member WHERE repo_id = $repo_id;
		 DELETE cluster WHERE repo_id = $repo_id`,
		map[string]any{"repo_id": repoID})
	if err != nil {
		slog.Warn("clustering: failed to delete clusters", "repo_id", repoID, "error", err)
	}
	return err
}

// SetClusterLLMLabel writes an LLM-generated label onto an existing cluster record.
// Returns clustering.ErrClusterNotFound when the cluster no longer exists (e.g.
// deleted by a concurrent ReplaceClusters during re-index). The caller should
// log a warning and continue; the cluster keeps its heuristic label.
func (s *SurrealStore) SetClusterLLMLabel(ctx context.Context, clusterID string, label string) error {
	db := s.client.DB()
	if db == nil {
		return nil
	}
	// Strip the "cluster:" prefix to construct the SurrealDB record ID.
	rawID := strings.TrimPrefix(clusterID, "cluster:")
	updated, err := queryOne[[]map[string]any](ctx, db,
		`UPDATE type::thing('cluster', $cid) SET
			llm_label  = $llm_label,
			updated_at = time::now()`,
		map[string]any{
			"cid":       rawID,
			"llm_label": label,
		})
	if err != nil {
		slog.Warn("clustering: failed to set llm_label", "cluster_id", clusterID, "error", err)
		return err
	}
	if len(updated) == 0 {
		return clustering.ErrClusterNotFound
	}
	return nil
}
