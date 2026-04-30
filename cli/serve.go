// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sourcebridge/sourcebridge/internal/api/rest"
	"github.com/sourcebridge/sourcebridge/internal/auth"
	"github.com/sourcebridge/sourcebridge/internal/config"
	"github.com/sourcebridge/sourcebridge/internal/db"
	gitres "github.com/sourcebridge/sourcebridge/internal/git/resolution"
	"github.com/sourcebridge/sourcebridge/internal/graph"
	"github.com/sourcebridge/sourcebridge/internal/health"
	"github.com/sourcebridge/sourcebridge/internal/knowledge"
	"github.com/sourcebridge/sourcebridge/internal/llm"
	"github.com/sourcebridge/sourcebridge/internal/llm/resolution"
	"github.com/sourcebridge/sourcebridge/internal/secretcipher"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/assembly"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/credentials"
	lwmetrics "github.com/sourcebridge/sourcebridge/internal/livingwiki/metrics"
	lworch "github.com/sourcebridge/sourcebridge/internal/livingwiki/orchestrator"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/scheduler"
	"github.com/sourcebridge/sourcebridge/internal/livingwiki/webhook"
	"github.com/sourcebridge/sourcebridge/internal/settings/comprehension"
	"github.com/sourcebridge/sourcebridge/internal/settings/livingwiki"
	"github.com/sourcebridge/sourcebridge/internal/telemetry"
	"github.com/sourcebridge/sourcebridge/internal/qa"
	"github.com/sourcebridge/sourcebridge/internal/trash"
	"github.com/sourcebridge/sourcebridge/internal/version"
	"github.com/sourcebridge/sourcebridge/internal/worker"
	"github.com/sourcebridge/sourcebridge/internal/worker/tlsreload"
	"github.com/sourcebridge/sourcebridge/internal/worker/llmcall"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the SourceBridge.ai API server and web UI",
	RunE:  runServe,
}

var servePort int

func init() {
	serveCmd.Flags().IntVar(&servePort, "port", 0, "HTTP port (overrides config)")
}

func runServe(cmd *cobra.Command, args []string) error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if servePort > 0 {
		cfg.Server.HTTPPort = servePort
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Initialize logger
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Connect to database
	surrealDB := db.NewSurrealDB(cfg.Storage)
	if err := surrealDB.Connect(context.Background()); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	surrealDB.StartKeepalive()
	defer surrealDB.Close()

	// Choose the store implementation based on surreal mode.
	var store graph.GraphStore
	var knowledgeStore knowledge.KnowledgeStore
	var jobStore llm.JobStore
	var comprehensionStore comprehension.Store
	var summaryNodeStore comprehension.SummaryNodeStore
	var lwStore livingwiki.Store
	var lwResolver *livingwiki.Resolver
	var lwRepoStore livingwiki.RepoSettingsStore
	if cfg.Storage.SurrealMode == "external" {
		// Run migrations against the external SurrealDB instance.
		migrationsDir := migrationsPath()
		slog.Info("running database migrations", "dir", migrationsDir)
		if err := surrealDB.Migrate(context.Background(), migrationsDir); err != nil {
			return fmt.Errorf("failed to run migrations: %w", err)
		}

		surrealStore := db.NewSurrealStore(surrealDB)
		store = surrealStore
		knowledgeStore = surrealStore
		jobStore = surrealStore
		comprehensionStore = surrealStore
		summaryNodeStore = surrealStore
		slog.Info("using SurrealDB-backed store (external mode)")
	} else {
		memCS := comprehension.NewMemStore()
		store = graph.NewStore()
		knowledgeStore = knowledge.NewMemStore()
		jobStore = llm.NewMemStore()
		comprehensionStore = memCS
		summaryNodeStore = memCS
		slog.Info("using in-memory store (embedded mode)")
	}

	// Initialize shared cache (memory by default, Redis when configured).
	// Consumed by the MCP session store for HA across replicas, and by
	// the trash retention worker for cross-replica leader election.
	cache := db.NewCache(cfg.Storage)

	// Trash (soft-delete recycle bin). Phase 1 requires external
	// SurrealDB — embedded mode falls back to nil (feature disabled).
	var trashStore trash.Store
	if cfg.Trash.Enabled && cfg.Storage.SurrealMode == "external" {
		trashStore = trash.NewSurrealStore(surrealDB)
		slog.Info("trash (recycle bin) enabled",
			"retention_days", cfg.Trash.RetentionDays,
			"sweep_interval_sec", cfg.Trash.SweepIntervalSec)

		// Retention worker runs in the background for the lifetime of
		// the server process. Leader election via Redis ensures only
		// one replica sweeps per tick when the cache is Redis-backed.
		worker := trash.NewWorker(trashStore, cache, trash.WorkerConfig{
			RetentionDays: cfg.Trash.RetentionDays,
			SweepInterval: time.Duration(cfg.Trash.SweepIntervalSec) * time.Second,
			MaxBatchSize:  cfg.Trash.MaxBatchSize,
		})
		go func() {
			if err := worker.Run(context.Background()); err != nil {
				slog.Error("trash retention worker exited", "error", err)
			}
		}()
	} else if cfg.Trash.Enabled {
		slog.Warn("trash is enabled in config but requires external SurrealDB; feature disabled",
			"storage.surreal_mode", cfg.Storage.SurrealMode)
	}

	knowledgeTimeoutProvider := func() time.Duration {
		// TimeoutSecs is for individual LLM completions (default 30s).
		// Knowledge generation is a multi-step pipeline that takes much
		// longer — use the dedicated constant (default 30 minutes).
		return worker.TimeoutKnowledgeRepository
	}

	// Initialize worker client.
	//
	// Default-disabled TLS path: errors are non-fatal (AI features get
	// disabled and the API still serves the rest of the surface). This
	// matches the pre-R2 behavior so OSS deployments with no worker
	// available still come up.
	//
	// TLS-enabled path: errors are FATAL. R2 slice 4 of plan
	// 2026-04-29-workspace-llm-source-of-truth-r2.md requires fail-closed
	// semantics: an operator who flipped SOURCEBRIDGE_WORKER_TLS_ENABLED=true
	// is asserting that mTLS must be working; a TLS load failure, handshake
	// mismatch, or worker still serving insecure must NOT degrade the API
	// to plaintext or hide behind a healthy readiness probe.
	//
	// Why we run a boot-time CheckHealth probe under TLS: grpc.NewClient
	// is lazy — it does not actually open a connection. Without an explicit
	// RPC, the API would start cleanly even when the worker is on insecure
	// gRPC, the worker's cert SAN doesn't match, or the CA chain is wrong;
	// the misconfig would only surface on the first real LLM call. Running
	// CheckHealth synchronously at boot makes the handshake the canary.
	var workerClient *worker.Client
	var workerTLSReloadWatcher *tlsreload.Watcher
	if cfg.Worker.Address != "" {
		opts := []worker.Option{
			worker.WithKnowledgeTimeoutProvider(knowledgeTimeoutProvider),
		}
		// R3 slice 4: when mTLS is enabled, construct a hot-reload
		// watcher BEFORE worker.New so we can pass it via Option. The
		// watcher loads + validates the initial cert (a second
		// validation on top of dialCredentials' load — cheap, and it
		// gives us mtime baselines for the poll loop). On every
		// validated cert reload the worker.Client redials and
		// atomic-swaps its bundle so future RPCs use the new cert
		// without any kubectl-side restart.
		if cfg.Worker.TLS.Enabled {
			// Codex r2 critical fix: the watcher's ServiceIdentity
			// validates the API client cert's SAN. cfg.Worker.TLS.ServerName
			// is the WORKER server's expected SAN, not the API's client SAN
			// — they're issued by the same CA but for different roles.
			// Leaving ServiceIdentity empty skips SAN matching on the
			// client cert; chain verification + ClientAuth EKU + key
			// match are sufficient identity assertions for the API's
			// own cert. The server's SAN is asserted on every handshake
			// via tls.Config.ServerName + the custom VerifyPeerCertificate
			// in worker.buildDialCredentials.
			w, werr := tlsreload.New(tlsreload.Config{
				CertPath:          cfg.Worker.TLS.CertPath,
				KeyPath:           cfg.Worker.TLS.KeyPath,
				CAPath:            cfg.Worker.TLS.CAPath,
				ServiceIdentity:   "", // see comment above
				ChainVerification: true,
				Logger:            slog.Default(),
			})
			if werr != nil {
				return fmt.Errorf("worker tls hot-reload watcher init: %w (refusing to start; check TLS material at %s, %s, %s)",
					werr, cfg.Worker.TLS.CertPath, cfg.Worker.TLS.KeyPath, cfg.Worker.TLS.CAPath)
			}
			if serr := w.Start(); serr != nil {
				return fmt.Errorf("worker tls hot-reload watcher start: %w", serr)
			}
			workerTLSReloadWatcher = w
			opts = append(opts, worker.WithTLSReloadWatcher(w))
		}
		wc, err := worker.New(
			cfg.Worker.Address,
			worker.TLSConfig{
				Enabled:    cfg.Worker.TLS.Enabled,
				CertPath:   cfg.Worker.TLS.CertPath,
				KeyPath:    cfg.Worker.TLS.KeyPath,
				CAPath:     cfg.Worker.TLS.CAPath,
				ServerName: cfg.Worker.TLS.ServerName,
			},
			opts...,
		)
		if err != nil {
			if cfg.Worker.TLS.Enabled {
				if workerTLSReloadWatcher != nil {
					_ = workerTLSReloadWatcher.Close()
				}
				return fmt.Errorf("worker client init with mTLS enabled: %w (refusing to start; check TLS material at %s, %s, %s)",
					err, cfg.Worker.TLS.CertPath, cfg.Worker.TLS.KeyPath, cfg.Worker.TLS.CAPath)
			}
			slog.Warn("failed to create worker client, AI features disabled", "error", err)
		} else {
			// TLS-enabled: probe the handshake before declaring success.
			// CheckHealth has a 3s timeout (TimeoutHealth) and runs a
			// real unary RPC. If TLS is misconfigured, this fails with a
			// transport error and we refuse to start.
			if cfg.Worker.TLS.Enabled {
				healthCtx, healthCancel := context.WithTimeout(context.Background(), 5*time.Second)
				ok, healthErr := wc.CheckHealth(healthCtx)
				healthCancel()
				if healthErr != nil {
					_ = wc.Close()
					if workerTLSReloadWatcher != nil {
						_ = workerTLSReloadWatcher.Close()
					}
					return fmt.Errorf("worker mTLS handshake probe failed: %w (refusing to start; check that the worker is running with SOURCEBRIDGE_WORKER_TLS_ENABLED=true and the certs are signed by the same CA)", healthErr)
				}
				if !ok {
					_ = wc.Close()
					if workerTLSReloadWatcher != nil {
						_ = workerTLSReloadWatcher.Close()
					}
					return fmt.Errorf("worker mTLS handshake probe returned non-serving (refusing to start; the worker process is up but its gRPC health check is reporting NOT_SERVING)")
				}
				slog.Info("worker mTLS handshake probe ok", "address", cfg.Worker.Address)
			}
			workerClient = wc
			defer workerClient.Close()
			if workerTLSReloadWatcher != nil {
				defer func() { _ = workerTLSReloadWatcher.Close() }()
				slog.Info("worker tls hot-reload watcher running",
					"address", cfg.Worker.Address,
					"cert", cfg.Worker.TLS.CertPath,
					"poll_minutes", 5)
			}
			slog.Info("worker client initialized", "address", cfg.Worker.Address, "tls_enabled", cfg.Worker.TLS.Enabled)
		}
	} else {
		if cfg.Worker.TLS.Enabled {
			return fmt.Errorf("worker.tls.enabled is true but worker.address is empty (refusing to start; either set worker.address or disable TLS)")
		}
		slog.Info("worker address not configured, AI features disabled")
	}

	// Initialize auth
	jwtMgr := auth.NewJWTManager(cfg.Security.JWTSecret, cfg.Security.JWTTTLMinutes, cfg.Edition)
	var authPersister auth.AuthPersister
	var gitConfigStore rest.GitConfigStore
	var gitConfigStoreConcrete *db.SurrealGitConfigStore // typed handle for the resolver
	var llmConfigStore rest.LLMConfigStore
	var llmConfigStoreConcrete *db.SurrealLLMConfigStore     // typed handle for profile-aware adapter wiring
	var llmProfileStore *db.SurrealLLMProfileStore           // typed handle for profile-aware adapter wiring
	var llmStoreAdapterFromProfiles resolution.LLMConfigStore // profile-aware adapter assembled in the external-storage branch
	var queueControlStore rest.QueueControlStore
	var tokenStore auth.APITokenStore
	var oidcStateStore auth.OIDCStateStore
	var desktopAuthStore rest.DesktopAuthSessionStore
	if cfg.Storage.SurrealMode == "external" {
		authPersister = auth.NewSurrealPersister(surrealDB)
		tokenStore = auth.NewSurrealAPITokenStore(surrealDB)
		oidcStateStore = auth.NewSurrealOIDCStateStore(surrealDB)
		desktopAuthStore = rest.NewSurrealDesktopAuthStore(surrealDB)
		slog.Info("auth persistence enabled via SurrealDB")

		// R3 slice 2: git creds source-of-truth. The legacy boot-time
		// merge below (which let cfg.Git.DefaultToken win whenever the
		// env var was set) is gone — same shape of bug as the LLM
		// workstream fixed in R1/R2. cfg.Git is now ONLY the env-bootstrap
		// layer of the git resolver; the resolver reads ca_git_config on
		// every Resolve via a version-keyed cache so an admin save on
		// replica A is visible to replica B on the very next clone.
		//
		// default_token is encrypted at rest under the sbenc:v1 envelope
		// (same cipher as ca_llm_config). The encryption key comes from
		// cfg.Security.EncryptionKey; the OSS escape hatch
		// SOURCEBRIDGE_ALLOW_UNENCRYPTED_GIT_TOKEN keeps the LLM naming
		// convention. cfg.Git is captured by VALUE into the resolver and
		// MUST NOT be mutated post-boot.
		gitCipher := secretcipher.NewAESGCMCipher(
			cfg.Security.EncryptionKey,
			strings.EqualFold(os.Getenv("SOURCEBRIDGE_ALLOW_UNENCRYPTED_GIT_TOKEN"), "true"),
		)
		if !gitCipher.HasKey() {
			if gitCipher.AllowsUnencrypted() {
				slog.Warn("git config: SOURCEBRIDGE_ALLOW_UNENCRYPTED_GIT_TOKEN=true — admin saves of default_token may land in plaintext on disk (OSS dev only)")
			} else {
				slog.Warn("git config: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY is unset — admin saves of default_token will return 422 unless ALLOW_UNENCRYPTED is on (set the encryption key in production)")
			}
		}
		// Codex r2 high fix: pass the SSH path validator so SaveGitConfig
		// rejects relative paths, traversal, shell metacharacters, and
		// out-of-allow-root paths at the store layer (the plan's
		// authoritative save-time gate). The validator is constructed
		// here because internal/git/resolution depends on internal/db
		// — the resolution package can't be imported by the db package
		// without a cycle. cli/serve.go owns the cross-package wiring.
		sshValidator := gitres.NewSSHKeyPathValidator(cfg.Git.SSHKeyPathRoot)
		gcs := db.NewSurrealGitConfigStore(
			surrealDB,
			db.WithGitConfigCipher(gitCipher),
			db.WithGitConfigSSHValidator(sshValidator.Validate),
		)
		gitConfigStore = gcs
		gitConfigStoreConcrete = gcs

		// Load persisted LLM config store. The legacy boot-time merge
		// (where DB values were applied onto cfg.LLM and env vars
		// silently won) is gone — see plan
		// thoughts/shared/plans/2026-04-29-workspace-llm-source-of-truth.md.
		// cfg.LLM is now the env-bootstrap layer of the resolver, never
		// mutated post-boot. The resolver layer reads workspace settings
		// directly from the DB on every Resolve via a version-keyed
		// cache, so an admin save on replica A is visible to replica B
		// on the very next worker LLM call.
		//
		// Slice 3 (R3): api_key column is encrypted at rest with the
		// versioned sbenc:v1 envelope. The encryption key comes from
		// cfg.Security.EncryptionKey; the OSS escape hatch
		// (SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY) is consulted from env
		// at boot and forwarded as a store option.
		//
		// LLM provider profiles slice 1 (librarian-M1): build the cipher
		// ONCE and pass it to BOTH the legacy ca_llm_config store AND
		// the new ca_llm_profile store via With…Cipher. This ensures
		// both rows are encrypted under identical key material with
		// identical legacy-warn rate-limiting state.
		allowUnencLLM := strings.EqualFold(os.Getenv("SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY"), "true")
		llmCipher := secretcipher.NewAESGCMCipher(cfg.Security.EncryptionKey, allowUnencLLM)
		if !llmCipher.HasKey() {
			if llmCipher.AllowsUnencrypted() {
				slog.Warn("llm config: SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY=true — admin saves of api_key may land in plaintext on disk (OSS dev only)")
			} else {
				slog.Warn("llm config: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY is unset — admin saves of api_key will return 422 unless ALLOW_UNENCRYPTED is on (set the encryption key in production)")
			}
		}
		lcs := db.NewSurrealLLMConfigStore(surrealDB, db.WithLLMConfigCipher(llmCipher))
		lps := db.NewSurrealLLMProfileStore(surrealDB, db.WithLLMProfileCipher(llmCipher))
		llmConfigStore = &llmConfigAdapter{store: lcs, profileStore: lps, surrealDB: surrealDB}
		queueControlStore = &queueControlAdapter{store: db.NewSurrealQueueControlStore(surrealDB)}

		// Living-wiki settings store + resolver (UI > env > default precedence).
		if cfg.Security.EncryptionKey == "" {
			slog.Warn("living-wiki: SOURCEBRIDGE_SECURITY_ENCRYPTION_KEY not set; secrets stored in plaintext")
		}
		lwStore = db.NewLivingWikiSettingsStore(surrealDB, cfg.Security.EncryptionKey)
		// Slice 5: per-repo LLM override stores api_key encrypted under
		// the same sbenc:v1 envelope as ca_llm_config. Forward the same
		// encryption opts.
		lwRepoOpts := []db.LivingWikiRepoSettingsStoreOption{
			db.WithLivingWikiRepoEncryptionKey(cfg.Security.EncryptionKey),
		}
		if strings.EqualFold(os.Getenv("SOURCEBRIDGE_ALLOW_UNENCRYPTED_LLM_KEY"), "true") {
			lwRepoOpts = append(lwRepoOpts, db.WithLivingWikiRepoAllowUnencrypted(true))
		}
		lwRepoStore = db.NewLivingWikiRepoSettingsStore(surrealDB, lwRepoOpts...)
		lwEnv := livingwiki.EnvConfig{
			Enabled:                 cfg.LivingWiki.Enabled,
			WorkerCount:             cfg.LivingWiki.WorkerCount,
			EventTimeout:            cfg.LivingWiki.EventTimeout,
			ConfluenceWebhookSecret: cfg.LivingWiki.ConfluenceWebhookSecret,
			NotionWebhookSecret:     cfg.LivingWiki.NotionWebhookSecret,
		}
		lwResolver = livingwiki.NewResolver(lwStore, lwEnv, 0)

		// LLM provider profiles slice 1 (codex-H4 + bob/codex H1):
		// schema-ensure runs in dependency order BEFORE MigrateToProfiles
		// and BEFORE the resolver / REST handlers mount.
		//
		//   1. lps.EnsureSchema                                 — ca_llm_profile table + UNIQUE INDEX
		//   2. lcs.EnsureProfilesSchemaExtensions               — active_profile_id + updated_at on ca_llm_config
		//   3. lwRepoStore (concrete).EnsureProfilesSchemaExtensions — living_wiki_llm_override.profile_id
		//   4. db.MigrateToProfiles                             — seed Default profile from legacy or env
		//
		// All three schema-ensure steps are idempotent. Migration is
		// idempotent on the deterministic record id ca_llm_profile:default-migrated;
		// concurrent boots converge via SurrealDB's BEGIN/COMMIT serialization.
		bootCtx, bootCancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := lps.EnsureSchema(bootCtx); err != nil {
			bootCancel()
			return fmt.Errorf("ensure ca_llm_profile schema: %w", err)
		}
		if err := lcs.EnsureProfilesSchemaExtensions(bootCtx); err != nil {
			bootCancel()
			return fmt.Errorf("ensure ca_llm_config profile-extensions schema: %w", err)
		}
		if lwRepoStoreConcrete, ok := lwRepoStore.(*db.LivingWikiRepoSettingsStore); ok && lwRepoStoreConcrete != nil {
			if err := lwRepoStoreConcrete.EnsureProfilesSchemaExtensions(bootCtx); err != nil {
				bootCancel()
				return fmt.Errorf("ensure lw_repo_settings profile-extensions schema: %w", err)
			}
		}
		if err := db.MigrateToProfiles(bootCtx, surrealDB, lcs, lps, llmCipher, allowUnencLLM, cfg.LLM); err != nil {
			bootCancel()
			return fmt.Errorf("llm profile migration failed: %w", err)
		}
		bootCancel()

		// Build the profile-aware resolver adapter (codex-H2 + H3 +
		// codex-M5). The adapter implements both LLMConfigStore (so the
		// existing DefaultResolver can reach it) AND ProfileLookupStore
		// (so slice 3's per-repo override can fetch a specific profile
		// by id). Reconciliation is handled via a small concrete-type
		// shim that adapts db.reconcileLegacyToActive into the
		// resolution.ProfileAwareReconciler interface.
		llmProfileStore = lps
		llmConfigStoreConcrete = lcs
		profileAdapter := resolution.NewProfileAwareLLMResolverAdapter(
			&profileAwareConfigStoreShim{store: lcs},
			&profileAwareProfileStoreShim{store: lps},
			&profileAwareReconcilerShim{surrealDB: surrealDB},
			slog.Default(),
		)
		llmStoreAdapterFromProfiles = profileAdapter
	}
	if tokenStore == nil {
		tokenStore = auth.NewAPITokenStore()
	}
	if oidcStateStore == nil {
		oidcStateStore = auth.NewMemoryOIDCStateStore()
	}
	if desktopAuthStore == nil {
		desktopAuthStore = rest.NewMemoryDesktopAuthStore()
	}
	localAuth := auth.NewLocalAuth(jwtMgr, authPersister)

	// Build the runtime LLM-config resolver. cfg.LLM is the env-bootstrap
	// layer; the workspace store is layer 2. Per-repo override (slice 5)
	// adds layer 1 by passing a non-nil RepoOverrideStore. Resolver runs
	// in every replica — version-keyed cache makes admin saves visible
	// cross-replica without polling or restart.
	//
	// LLM provider profiles slice 1: the resolver's view of the
	// workspace layer is the new ProfileAwareLLMResolverAdapter
	// (assembled above in the external-storage branch). It owns:
	//   - dual-read fallback for the truly-pre-migration window (D8 / codex-H3),
	//   - rolling-deploy reconciliation via the version watermark (codex-H2 / r1c),
	//   - active-profile-missing banner state (codex-H3),
	//   - per-repo profile lookup for slice 3.
	//
	// Two stores against the same row would each track their own
	// loadedLegacyOnce, producing duplicate WARNs on legacy reads — not
	// a correctness bug but messier than necessary.
	var llmStoreAdapter resolution.LLMConfigStore
	if cfg.Storage.SurrealMode == "external" && surrealDB != nil {
		llmStoreAdapter = llmStoreAdapterFromProfiles
	}
	// Defensive: keep the typed concrete stores reachable even when the
	// adapter is the new profile-aware one. Tests that exercise the
	// pre-profile shape can still reach lcs via llmConfigStoreConcrete.
	_ = llmConfigStoreConcrete
	_ = llmProfileStore

	// LLM provider profiles slice 1: build the rest-facing adapter
	// that the new /admin/llm-profiles handlers will reach. nil when
	// running embedded (no SurrealDB) — handlers return 503 in that
	// case via their own nil-store check.
	var llmProfileRestAdapter rest.LLMProfileStoreAdapter
	if llmProfileStore != nil && llmConfigStoreConcrete != nil && surrealDB != nil {
		var resolverAdapter *resolution.ProfileAwareLLMResolverAdapter
		if pa, ok := llmStoreAdapterFromProfiles.(*resolution.ProfileAwareLLMResolverAdapter); ok {
			resolverAdapter = pa
		}
		llmProfileRestAdapter = &llmProfileStoreAdapter{
			lcs:             llmConfigStoreConcrete,
			lps:             llmProfileStore,
			surrealDB:       surrealDB,
			resolverAdapter: resolverAdapter,
		}
	}

	// Slice 5: per-repo override store. Defaults to "default" tenant
	// for the multi-tenant cutover; tenant scoping is wired through
	// the GraphQL resolver context in enterprise mode.
	var repoOverrideAdapter resolution.RepoOverrideStore
	if lwRepoStore != nil {
		// Need the *db.LivingWikiRepoSettingsStore concrete type to
		// access decrypted fields. Type-assert; if the store is
		// something else (in-memory test mode) skip the override.
		if concrete, ok := lwRepoStore.(*db.LivingWikiRepoSettingsStore); ok {
			repoOverrideAdapter = &lwRepoOverrideAdapter{
				store:    concrete,
				tenantID: "default",
			}
		}
	}
	llmResolver := resolution.New(llmStoreAdapter, repoOverrideAdapter, cfg.LLM, slog.Default())

	// R3 slice 2: build the runtime git-credential resolver. Mirrors the
	// LLM resolver above. cfg.Git is the env-bootstrap layer (captured by
	// VALUE inside the resolver and never mutated post-boot). The
	// workspace store is layer 1; on every Resolve the resolver consults
	// the version cell so admin saves on a peer replica are visible
	// without polling. nil store ⇒ embedded mode without persistence;
	// resolver still serves env-bootstrap cleanly.
	var gitResolver gitres.Resolver
	if gitConfigStoreConcrete != nil {
		gitResolver = gitres.New(gitConfigStoreConcrete, cfg.Git, slog.Default())
	} else {
		gitResolver = gitres.New(nil, cfg.Git, slog.Default())
	}

	// Build the LLM-aware adapter around the worker client. Every gRPC
	// LLM RPC must flow through this Caller so resolved metadata is
	// attached to the outgoing context. The AST lint test in
	// internal/llm/resolution/lint_test.go enforces that.
	var llmCaller *llmcall.Caller
	if workerClient != nil {
		llmCaller = llmcall.New(workerClient, llmResolver, slog.Default())
	}

	// Living-wiki dispatcher construction.
	//
	// Kill-switch: check env var before any DB state so operators can disable
	// the feature instantly in an incident without a config change or restart.
	var lwDispatcher *webhook.Dispatcher
	killSwitch := strings.EqualFold(os.Getenv("SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH"), "true")
	if killSwitch {
		slog.Warn("living-wiki kill-switch active (SOURCEBRIDGE_LIVING_WIKI_KILL_SWITCH=true); dispatcher not started")
	} else if cfg.Storage.SurrealMode == "external" && lwResolver != nil {
		resolved, resolveErr := lwResolver.Get()
		if resolveErr != nil {
			slog.Warn("living-wiki resolver error; dispatcher not started", "error", resolveErr)
		} else if resolved != nil && resolved.Enabled {
			broker := credentials.NewResolverBroker(lwResolver)

			// Parse EventTimeout from the config string (e.g. "5m").
			var eventTimeout time.Duration
			if cfg.LivingWiki.EventTimeout != "" {
				if d, err := time.ParseDuration(cfg.LivingWiki.EventTimeout); err == nil {
					eventTimeout = d
				}
			}

			d, assembleErr := assembly.AssembleDispatcher(assembly.AssemblerDeps{
				SurrealDB:    surrealDB,
				GraphStore:   store,
				WorkerClient: workerClient,
				LLMCaller:    llmCaller,
				Broker:       broker,
				Logger:       &slogLivingWikiLogger{},
				WorkerCount:  cfg.LivingWiki.WorkerCount,
				EventTimeout: eventTimeout,
			})
			if assembleErr != nil {
				slog.Error("living-wiki assembly failed; dispatcher not started", "error", assembleErr)
			} else {
				if startErr := d.Start(context.Background()); startErr != nil {
					slog.Error("living-wiki dispatcher failed to start", "error", startErr)
				} else {
					lwDispatcher = d
					slog.Info("living-wiki dispatcher started",
						"worker_count", cfg.LivingWiki.WorkerCount,
						"event_timeout", cfg.LivingWiki.EventTimeout,
					)
				}
			}
		} else {
			slog.Info("living-wiki globally disabled; dispatcher not started")
		}
	}

	// Living-wiki job result store (R8).
	// Constructed regardless of kill-switch status so the GraphQL resolver can
	// query past results even when the dispatcher is not running.
	var lwJobResultStore livingwiki.JobResultStore
	if cfg.Storage.SurrealMode == "external" {
		lwJobResultStore = db.NewLivingWikiJobResultStore(surrealDB)
	}

	// Living-wiki metrics collector (R8).
	// Registered once at boot; the /metrics handler calls WritePrometheusText.
	_ = lwmetrics.Default // ensures the package-level collector is initialized

	// Living-wiki scheduler (R6).
	// Started after the dispatcher so the dispatcher is ready to accept events
	// on the first tick. schedCancel is always non-nil; it is a no-op when the
	// scheduler was not started, so the defer below is unconditionally safe.
	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel() // ensure goroutine is cancelled on any exit path
	if lwDispatcher != nil && lwRepoStore != nil {
		var schedInterval time.Duration
		if cfg.LivingWiki.SchedulerInterval != "" {
			if d, err := time.ParseDuration(cfg.LivingWiki.SchedulerInterval); err == nil && d > 0 {
				schedInterval = d
			}
		}
		sched := scheduler.New(scheduler.SchedulerDeps{
			RepoStore:   lwRepoStore,
			Dispatcher:  lwDispatcher,
			Cache:       cache,
			Interval:    schedInterval,
			MaxParallel: cfg.LivingWiki.MaxConcurrentJobsPerTenant,
			TenantID:    "default",
		})
		go func() {
			if err := sched.Run(schedCtx); err != nil && err != context.Canceled {
				slog.Error("living-wiki scheduler exited unexpectedly", "error", err)
			}
		}()
		slog.Info("living-wiki scheduler started",
			"interval", cfg.LivingWiki.SchedulerInterval,
			"max_parallel", cfg.LivingWiki.MaxConcurrentJobsPerTenant,
		)
	}

	// Build the shared health checker. In external mode, pass the SurrealDB
	// instance as the pinger so /readyz and serviceHealth do a real round-trip.
	// In embedded/in-memory mode, pass nil so the checker treats DB as healthy
	// without attempting a WebSocket connection.
	var healthChecker *health.Checker
	if cfg.Storage.SurrealMode == "external" {
		healthChecker = health.New(surrealDB, workerClient)
	} else {
		healthChecker = health.New(nil, workerClient)
	}

	// Create HTTP server
	server := rest.NewServer(cfg, localAuth, jwtMgr, store, workerClient,
		rest.WithEnterpriseDB(surrealDB.DB()),
		rest.WithHealthChecker(healthChecker),
		rest.WithKnowledgeStore(knowledgeStore),
		rest.WithJobStore(jobStore),
		rest.WithGitConfigStore(gitConfigStore),
		rest.WithGitResolver(gitResolver),
		rest.WithLLMConfigStore(llmConfigStore),
		rest.WithLLMProfileStore(llmProfileRestAdapter),
		rest.WithLLMResolver(llmResolver),
		rest.WithLLMCaller(llmCaller),
		rest.WithQueueControlStore(queueControlStore),
		rest.WithTokenStore(tokenStore),
		rest.WithDesktopAuthStore(desktopAuthStore),
		rest.WithComprehensionStore(comprehensionStore),
		rest.WithSummaryNodeStore(summaryNodeStore),
		rest.WithCache(cache),
		rest.WithTrashStore(trashStore),
		rest.WithLivingWikiStore(lwStore),
		rest.WithLivingWikiResolver(lwResolver),
		rest.WithLivingWikiRepoStore(lwRepoStore),
		rest.WithLivingWikiDispatcher(lwDispatcher),
		rest.WithLivingWikiJobResultStore(lwJobResultStore),
		rest.WithLivingWikiLiveOrchestrator(func() *lworch.Orchestrator {
			if lwDispatcher == nil {
				return nil
			}
			return lwDispatcher.LiveOrchestrator()
		}()),
	)

	// Initialize OIDC if configured
	if cfg.Security.OIDC.ClientID != "" && cfg.Security.OIDC.IssuerURL != "" {
		oidcProvider, err := auth.NewOIDCProvider(context.Background(), cfg.Security.OIDC, jwtMgr, oidcStateStore)
		if err != nil {
			slog.Warn("OIDC initialization failed, SSO disabled", "error", err)
		} else {
			server.SetOIDCProvider(oidcProvider)
			slog.Info("OIDC SSO enabled", "issuer", cfg.Security.OIDC.IssuerURL)
		}
	}

	// Start anonymous telemetry (opt-out via SOURCEBRIDGE_TELEMETRY=off)
	dataDir := cfg.Storage.RepoCachePath
	if dataDir == "" {
		dataDir = "/data"
	}
	tracker := telemetry.New(
		version.Version,
		cfg.Edition,
		dataDir,
		telemetry.WithLLMProviderKind(classifyTelemetryLLMProviderKind(cfg.LLM.Provider)),
		telemetry.WithCountProvider(&telemetryCountProvider{store: store}),
	)
	tracker.Start()
	defer tracker.Stop()

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.HTTPPort),
		Handler:      server.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 360 * time.Second, // Safety backstop for long AI operations; real timeouts are per-operation
		IdleTimeout:  120 * time.Second,
	}

	// Start server
	errCh := make(chan error, 1)
	go func() {
		slog.Info("starting server", "port", cfg.Server.HTTPPort, "url", cfg.Server.PublicBaseURL)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	if cleaner, ok := tokenStore.(auth.CleanupCapable); ok {
		go startAuthCleanupLoop("api_tokens", cleaner)
	}
	if cleaner, ok := oidcStateStore.(auth.CleanupCapable); ok {
		go startAuthCleanupLoop("oidc_states", cleaner)
	}
	if cleaner, ok := desktopAuthStore.(interface {
		Cleanup(context.Context) (int, error)
	}); ok {
		go startDesktopAuthCleanupLoop("desktop_auth_sessions", cleaner)
	}

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		slog.Info("received shutdown signal", "signal", sig)
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slog.Info("shutting down server")
	if err := httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown error: %w", err)
	}
	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("server component shutdown error: %w", err)
	}

	// schedCancel() is called by defer above, ensuring the scheduler goroutine
	// exits after the dispatcher has drained.
	slog.Info("server stopped")
	return nil
}

func startAuthCleanupLoop(name string, cleaner auth.CleanupCapable) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		count, err := cleaner.Cleanup(ctx)
		cancel()
		if err != nil {
			slog.Warn("auth cleanup failed", "target", name, "error", err)
			continue
		}
		if count > 0 {
			slog.Info("auth cleanup completed", "target", name, "deleted", count)
		}
	}
}

func startDesktopAuthCleanupLoop(name string, cleaner interface {
	Cleanup(context.Context) (int, error)
}) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		count, err := cleaner.Cleanup(ctx)
		cancel()
		if err != nil {
			slog.Warn("auth cleanup failed", "target", name, "error", err)
			continue
		}
		if count > 0 {
			slog.Info("auth cleanup completed", "target", name, "deleted", count)
		}
	}
}

// migrationsPath returns the path to the database migrations directory.
// It first checks for a SOURCEBRIDGE_MIGRATIONS_DIR env var, then falls back
// to locating the directory relative to the binary.
func migrationsPath() string {
	if dir := os.Getenv("SOURCEBRIDGE_MIGRATIONS_DIR"); dir != "" {
		return dir
	}

	// Try /migrations (Docker container layout)
	if info, err := os.Stat("/migrations"); err == nil && info.IsDir() {
		return "/migrations"
	}

	// Try relative to the executable
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Join(filepath.Dir(exe), "migrations")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	// Try relative to the source (works during development)
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		dir := filepath.Join(filepath.Dir(filename), "..", "internal", "db", "migrations")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	// Final fallback
	return "internal/db/migrations"
}

// llmConfigAdapter bridges db.SurrealLLMConfigStore and rest.LLMConfigStore
// to avoid a circular import between the db and rest packages.
//
// LLM provider profiles slice 1: SaveLLMConfig now routes through the
// active profile when one exists (D7) via WriteActiveProfilePatchWithRetry,
// which goes through writeActiveProfileWithLegacyMirror to dual-write
// the active profile and the legacy mirror row in one BEGIN/COMMIT
// (codex-H2). When no active profile exists yet (extreme boot-race),
// the adapter falls back to the legacy SaveLLMConfig path so the
// migration can pick up the legacy bytes (bob-L1).
type llmConfigAdapter struct {
	store        *db.SurrealLLMConfigStore
	profileStore *db.SurrealLLMProfileStore // nil in pre-profile or test wirings
	surrealDB    *db.SurrealDB              // nil in pre-profile or test wirings
}

type queueControlAdapter struct {
	store *db.SurrealQueueControlStore
}

func (a *llmConfigAdapter) LoadLLMConfig() (*rest.LLMConfigRecord, error) {
	rec, err := a.store.LoadLLMConfig()
	if err != nil || rec == nil {
		return nil, err
	}
	return &rest.LLMConfigRecord{
		Provider:                 rec.Provider,
		BaseURL:                  rec.BaseURL,
		APIKey:                   rec.APIKey,
		SummaryModel:             rec.SummaryModel,
		ReviewModel:              rec.ReviewModel,
		AskModel:                 rec.AskModel,
		KnowledgeModel:           rec.KnowledgeModel,
		ArchitectureDiagramModel: rec.ArchitectureDiagramModel,
		ReportModel:              rec.ReportModel,
		DraftModel:               rec.DraftModel,
		TimeoutSecs:              rec.TimeoutSecs,
		AdvancedMode:             rec.AdvancedMode,
	}, nil
}

func (a *llmConfigAdapter) SaveLLMConfig(rec *rest.LLMConfigRecord) error {
	// LLM provider profiles slice 1: legacy PUT /admin/llm-config now
	// translates to a PATCH against the ACTIVE profile (D7), going
	// through writeActiveProfileWithLegacyMirror so the legacy mirror
	// row stays in sync (codex-H2). The patch is "set every field"
	// because this endpoint's contract is total replacement of the
	// effective config.
	//
	// When no active profile exists yet (extreme narrow boot-race
	// window where the request arrives before MigrateToProfiles
	// completes), fall through to the legacy SaveLLMConfig — the
	// boot-time migration will pick up the legacy bytes and seed
	// Default from them.
	ctx := context.Background()
	activeID, _, err := a.store.LoadActiveProfileIDAndVersion(ctx)
	if err == nil && activeID != "" && a.profileStore != nil && a.surrealDB != nil {
		// Encrypt the api_key once, here. Empty plaintext sealed = ""
		// (cipher contract). The helpers expect the SEALED form.
		sealed, encErr := a.profileStore.EncryptedAPIKey(rec.APIKey)
		if encErr != nil {
			if errors.Is(encErr, db.ErrEncryptionKeyRequired) {
				return fmt.Errorf("%w: %v", rest.ErrLLMEncryptionKeyRequired, encErr)
			}
			return encErr
		}
		patch := db.ProfilePatch{
			Provider:                 rec.Provider,
			BaseURL:                  rec.BaseURL,
			APIKey:                   sealed,
			APIKeyMode:               db.APIKeyModeSet(),
			SummaryModel:             rec.SummaryModel,
			ReviewModel:              rec.ReviewModel,
			AskModel:                 rec.AskModel,
			KnowledgeModel:           rec.KnowledgeModel,
			ArchitectureDiagramModel: rec.ArchitectureDiagramModel,
			ReportModel:              rec.ReportModel,
			DraftModel:               rec.DraftModel,
			TimeoutSecs:              rec.TimeoutSecs,
			AdvancedMode:             rec.AdvancedMode,
			FieldsPresent: db.ProfilePatchFields{
				Provider:                 true,
				BaseURL:                  true,
				SummaryModel:             true,
				ReviewModel:              true,
				AskModel:                 true,
				KnowledgeModel:           true,
				ArchitectureDiagramModel: true,
				ReportModel:              true,
				DraftModel:               true,
				TimeoutSecs:              true,
				AdvancedMode:             true,
			},
		}
		// If api_key plaintext is empty, route through Clear so the
		// helper writes api_key='' rather than leaving a stale value.
		// Empty sealed bytes already encode "no key" semantically,
		// but going through Clear documents intent.
		if rec.APIKey == "" {
			patch.APIKey = ""
			patch.APIKeyMode = db.APIKeyModeClear()
		}
		if _, err := db.WriteActiveProfilePatchWithRetry(ctx, a.surrealDB, a.store, patch); err != nil {
			if errors.Is(err, db.ErrEncryptionKeyRequired) {
				return fmt.Errorf("%w: %v", rest.ErrLLMEncryptionKeyRequired, err)
			}
			return err
		}
		return nil
	}

	// Boot-race fallback (bob-L1): no active profile yet. Write through
	// the legacy path; the migration will pick up these bytes.
	err = a.store.SaveLLMConfig(&db.LLMConfigRecord{
		Provider:                 rec.Provider,
		BaseURL:                  rec.BaseURL,
		APIKey:                   rec.APIKey,
		SummaryModel:             rec.SummaryModel,
		ReviewModel:              rec.ReviewModel,
		AskModel:                 rec.AskModel,
		KnowledgeModel:           rec.KnowledgeModel,
		ArchitectureDiagramModel: rec.ArchitectureDiagramModel,
		ReportModel:              rec.ReportModel,
		DraftModel:               rec.DraftModel,
		TimeoutSecs:              rec.TimeoutSecs,
		AdvancedMode:             rec.AdvancedMode,
	})
	// Bridge the db-package sentinel into the rest-package sentinel so
	// handleUpdateLLMConfig can map it to a 422 without importing
	// internal/db. errors.Is matches across the wrap.
	if errors.Is(err, db.ErrEncryptionKeyRequired) {
		return fmt.Errorf("%w: %v", rest.ErrLLMEncryptionKeyRequired, err)
	}
	return err
}

// llmStoreResolverAdapter bridges *db.SurrealLLMConfigStore to the narrow
// resolution.LLMConfigStore interface. The resolver does not import
// internal/db (which would create a cycle), so the adapter lives here.
type llmStoreResolverAdapter struct {
	store *db.SurrealLLMConfigStore
}

func (a *llmStoreResolverAdapter) LoadLLMConfig() (*resolution.WorkspaceRecord, error) {
	rec, err := a.store.LoadLLMConfig()
	if err != nil || rec == nil {
		return nil, err
	}
	return &resolution.WorkspaceRecord{
		Provider:                 rec.Provider,
		BaseURL:                  rec.BaseURL,
		APIKey:                   rec.APIKey,
		SummaryModel:             rec.SummaryModel,
		ReviewModel:              rec.ReviewModel,
		AskModel:                 rec.AskModel,
		KnowledgeModel:           rec.KnowledgeModel,
		ArchitectureDiagramModel: rec.ArchitectureDiagramModel,
		ReportModel:              rec.ReportModel,
		DraftModel:               rec.DraftModel,
		TimeoutSecs:              rec.TimeoutSecs,
		AdvancedMode:             rec.AdvancedMode,
		Version:                  rec.Version,
	}, nil
}

func (a *llmStoreResolverAdapter) LoadLLMConfigVersion() (uint64, error) {
	return a.store.LoadLLMConfigVersion()
}

// lwRepoOverrideAdapter bridges *db.LivingWikiRepoSettingsStore to the
// narrow resolution.RepoOverrideStore interface.
//
// History: the parent delivery scoped this to living-wiki ops only. R2
// widens the override to apply to every repo-scoped LLM op. The "lw"
// prefix is preserved because the storage column is still
// `lw_repo_settings.living_wiki_llm_override` for backward compat — see
// CLAUDE.md legacy-name caveat.
type lwRepoOverrideAdapter struct {
	store    *db.LivingWikiRepoSettingsStore
	tenantID string
}

func (a *lwRepoOverrideAdapter) LoadLLMOverride(ctx context.Context, repoID string) (*resolution.RepoOverride, error) {
	if a == nil || a.store == nil || repoID == "" {
		return nil, nil
	}
	settings, err := a.store.GetRepoSettings(ctx, a.tenantID, repoID)
	if err != nil {
		return nil, err
	}
	if settings == nil || settings.LLMOverride == nil {
		return nil, nil
	}
	return &resolution.RepoOverride{
		Provider:                 settings.LLMOverride.Provider,
		BaseURL:                  settings.LLMOverride.BaseURL,
		APIKey:                   settings.LLMOverride.APIKey,
		AdvancedMode:             settings.LLMOverride.AdvancedMode,
		SummaryModel:             settings.LLMOverride.SummaryModel,
		ReviewModel:              settings.LLMOverride.ReviewModel,
		AskModel:                 settings.LLMOverride.AskModel,
		KnowledgeModel:           settings.LLMOverride.KnowledgeModel,
		ArchitectureDiagramModel: settings.LLMOverride.ArchitectureDiagramModel,
		ReportModel:              settings.LLMOverride.ReportModel,
		DraftModel:               settings.LLMOverride.DraftModel,
	}, nil
}

func (a *queueControlAdapter) LoadQueueControl() (*rest.QueueControlRecord, error) {
	rec, err := a.store.LoadQueueControl()
	if err != nil || rec == nil {
		return nil, err
	}
	return &rest.QueueControlRecord{
		IntakePaused: rec.IntakePaused,
	}, nil
}

func (a *queueControlAdapter) SaveQueueControl(rec *rest.QueueControlRecord) error {
	return a.store.SaveQueueControl(&db.QueueControlRecord{
		IntakePaused: rec.IntakePaused,
	})
}

// telemetryCountProvider provides aggregate counts from the graph store.
type telemetryCountProvider struct {
	store graph.GraphStore
}

func (p *telemetryCountProvider) TelemetryCounts() (repos, users int, features []string, counts map[string]int) {
	if p.store == nil {
		return 0, 0, nil, nil
	}
	allRepos := p.store.ListRepositories()
	repos = len(allRepos)

	var totalFiles, totalSymbols int
	for _, r := range allRepos {
		totalFiles += r.FileCount
		totalSymbols += r.FunctionCount + r.ClassCount
	}

	counts = map[string]int{
		"total_files":        totalFiles,
		"total_symbols":      totalSymbols,
		"qa_asks_total_14d":  qa.AsksTotal14d(),
	}

	// Merge in trash (recycle bin) counters. These are process-start
	// cumulative; safe to read even when the feature is disabled
	// (atomics default to zero).
	for k, v := range trash.Counters() {
		counts[k] = v
	}

	if qa.ServerSideEnabled() {
		features = append(features, "qa_server_side")
	}

	return repos, 0, features, counts
}

func classifyTelemetryLLMProviderKind(provider string) string {
	switch provider {
	case "ollama", "vllm", "llama-cpp", "sglang", "lmstudio":
		return "local"
	case "":
		return ""
	default:
		return "cloud"
	}
}

// ─────────────────────────────────────────────────────────────────────────
// LLM provider profiles slice 1 — REST adapter (rest.LLMProfileStoreAdapter)
// ─────────────────────────────────────────────────────────────────────────
//
// Bridges the rest package's narrow profile-store interface into the
// concrete db package. Owns:
//   - the BEGIN/COMMIT helper invocations + retry loops,
//   - the active-profile-id pre-check on DELETE (D5),
//   - the active-profile-missing accessor (codex-H3),
//   - the LogProfileSwitched slog emission on activate (bob-M2 / ruby-L1).

type llmProfileStoreAdapter struct {
	lcs           *db.SurrealLLMConfigStore
	lps           *db.SurrealLLMProfileStore
	surrealDB     *db.SurrealDB
	resolverAdapter *resolution.ProfileAwareLLMResolverAdapter // for the ActiveProfileMissing accessor
}

func (a *llmProfileStoreAdapter) ListProfiles(ctx context.Context) ([]rest.ProfileResponse, error) {
	profiles, err := a.lps.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	activeID, _, _ := a.lcs.LoadActiveProfileIDAndVersion(ctx)
	out := make([]rest.ProfileResponse, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, profileToResponse(p, activeID))
	}
	return out, nil
}

func (a *llmProfileStoreAdapter) GetProfile(ctx context.Context, id string) (*rest.ProfileResponse, error) {
	p, err := a.lps.LoadProfile(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrProfileNotFound) {
			return nil, rest.ErrProfileNotFound
		}
		return nil, err
	}
	activeID, _, _ := a.lcs.LoadActiveProfileIDAndVersion(ctx)
	resp := profileToResponse(*p, activeID)
	return &resp, nil
}

func (a *llmProfileStoreAdapter) CreateProfile(ctx context.Context, req rest.ProfileCreateRequest) (string, error) {
	id, err := a.lps.CreateProfile(ctx, db.ProfileCreate{
		Name:                     req.Name,
		Provider:                 req.Provider,
		BaseURL:                  req.BaseURL,
		APIKey:                   req.APIKey,
		SummaryModel:             req.SummaryModel,
		ReviewModel:              req.ReviewModel,
		AskModel:                 req.AskModel,
		KnowledgeModel:           req.KnowledgeModel,
		ArchitectureDiagramModel: req.ArchitectureDiagramModel,
		ReportModel:              req.ReportModel,
		DraftModel:               req.DraftModel,
		TimeoutSecs:              req.TimeoutSecs,
		AdvancedMode:             req.AdvancedMode,
	})
	if err != nil {
		return "", translateProfileStoreErr(err)
	}
	// Bump workspace.version + advance active watermark so the resolver
	// invalidates its cache on the next probe and so the watermark
	// stays in lockstep with the version on every new-code write.
	if _, bumpErr := db.BumpVersionAfterCreate(ctx, a.surrealDB, a.lcs); bumpErr != nil {
		// The profile is already created; surface the bump failure
		// but do not roll back. Subsequent writes will eventually bump.
		slog.Warn("llm profile create: workspace.version bump failed (profile is created; resolver may serve stale until next write)",
			"id", id, "error", bumpErr)
	}
	return id, nil
}

func (a *llmProfileStoreAdapter) UpdateProfile(ctx context.Context, id string, req rest.ProfileUpdateRequest) error {
	// Determine whether the target is the active profile. The choice
	// of helper depends on this; on a raced activation between this
	// pre-check and the BEGIN/COMMIT, the helper's CAS guard surfaces
	// ErrTargetNoLongerActive (codex-r1e Low / r1d M2).
	activeID, _, err := a.lcs.LoadActiveProfileIDAndVersion(ctx)
	if err != nil {
		return err
	}

	// Apply name + non-key string fields via the store's UpdateProfile,
	// which knows pointer-patch semantics + name uniqueness. Then,
	// separately, run the CAS-guarded version bump + dual-write so
	// downstream replicas observe the change. Two-phase write: the
	// row update is the source of truth; the helper makes the
	// version cell + watermark advance.
	patch := db.ProfileUpdate{
		Name:                     req.Name,
		Provider:                 req.Provider,
		BaseURL:                  req.BaseURL,
		APIKey:                   req.APIKey,
		ClearAPIKey:              req.ClearAPIKey,
		SummaryModel:             req.SummaryModel,
		ReviewModel:              req.ReviewModel,
		AskModel:                 req.AskModel,
		KnowledgeModel:           req.KnowledgeModel,
		ArchitectureDiagramModel: req.ArchitectureDiagramModel,
		ReportModel:              req.ReportModel,
		DraftModel:               req.DraftModel,
		TimeoutSecs:              req.TimeoutSecs,
		AdvancedMode:             req.AdvancedMode,
	}
	if err := a.lps.UpdateProfile(ctx, id, patch); err != nil {
		return translateProfileStoreErr(err)
	}

	// If the updated profile is the active one, run a dual-write batch
	// that mirrors the new contents to the legacy row + bumps version.
	// Otherwise just bump version + advance active watermark.
	if id == activeID {
		// Re-load the profile to capture the post-update sealed api_key
		// bytes and current scalar fields, then dual-write.
		p, loadErr := a.lps.LoadProfile(ctx, id)
		if loadErr != nil {
			return translateProfileStoreErr(loadErr)
		}
		sealed, encErr := a.lps.EncryptedAPIKey(p.APIKey)
		if encErr != nil {
			return translateProfileStoreErr(encErr)
		}
		patchHelper := db.ProfilePatch{
			Provider:                 p.Provider,
			BaseURL:                  p.BaseURL,
			APIKey:                   sealed,
			APIKeyMode:               db.APIKeyModeSet(),
			SummaryModel:             p.SummaryModel,
			ReviewModel:              p.ReviewModel,
			AskModel:                 p.AskModel,
			KnowledgeModel:           p.KnowledgeModel,
			ArchitectureDiagramModel: p.ArchitectureDiagramModel,
			ReportModel:              p.ReportModel,
			DraftModel:               p.DraftModel,
			TimeoutSecs:              p.TimeoutSecs,
			AdvancedMode:             p.AdvancedMode,
			FieldsPresent: db.ProfilePatchFields{
				Provider:                 true,
				BaseURL:                  true,
				SummaryModel:             true,
				ReviewModel:              true,
				AskModel:                 true,
				KnowledgeModel:           true,
				ArchitectureDiagramModel: true,
				ReportModel:              true,
				DraftModel:               true,
				TimeoutSecs:              true,
				AdvancedMode:             true,
			},
		}
		if p.APIKey == "" {
			patchHelper.APIKey = ""
			patchHelper.APIKeyMode = db.APIKeyModeClear()
		}
		_, bumpErr := db.WriteActiveProfilePatchWithRetry(ctx, a.surrealDB, a.lcs, patchHelper)
		if bumpErr != nil {
			return translateProfileStoreErr(bumpErr)
		}
	} else {
		// Non-active edit: bump workspace.version + advance active
		// watermark so the resolver picks up the change on the next
		// version probe (codex-M5 cache-invalidation invariant).
		if _, bumpErr := db.BumpVersionAfterCreate(ctx, a.surrealDB, a.lcs); bumpErr != nil {
			slog.Warn("llm profile update: workspace.version bump failed (profile updated; resolver may serve stale until next write)",
				"id", id, "error", bumpErr)
		}
	}
	return nil
}

func (a *llmProfileStoreAdapter) DeleteProfile(ctx context.Context, id string) error {
	activeID, _, err := a.lcs.LoadActiveProfileIDAndVersion(ctx)
	if err != nil {
		return err
	}
	if id == activeID {
		// D5: refuse delete-of-active at the API layer with 409.
		return rest.ErrProfileActiveDeleteForbidden
	}
	// First check existence — if the profile is gone, surface 404.
	if _, loadErr := a.lps.LoadProfile(ctx, id); loadErr != nil {
		return translateProfileStoreErr(loadErr)
	}
	// Run the CAS-guarded delete (which also zeros the api_key
	// ciphertext via xander-M1 inside the BEGIN/COMMIT batch).
	if _, helperErr := db.DeleteNonActiveWithRetry(ctx, a.surrealDB, a.lcs, id); helperErr != nil {
		return translateProfileStoreErr(helperErr)
	}
	return nil
}

func (a *llmProfileStoreAdapter) ActivateProfile(ctx context.Context, id, by string) error {
	// Pre-check the target exists; the helper will THROW
	// profile_not_found inside BEGIN/COMMIT, but a friendlier 404
	// avoids the round trip in the common case.
	if _, err := a.lps.LoadProfile(ctx, id); err != nil {
		return translateProfileStoreErr(err)
	}
	oldActiveID, _, _ := a.lcs.LoadActiveProfileIDAndVersion(ctx)
	newVersion, err := db.ActivateProfileWithRetry(ctx, a.surrealDB, a.lcs, id)
	if err != nil {
		return translateProfileStoreErr(err)
	}
	resolution.LogProfileSwitched(slog.Default(), oldActiveID, id, by, newVersion)
	return nil
}

func (a *llmProfileStoreAdapter) ActiveProfileMissing() bool {
	if a.resolverAdapter == nil {
		return false
	}
	return a.resolverAdapter.ActiveProfileMissing()
}

func (a *llmProfileStoreAdapter) ActiveProfileID(ctx context.Context) (string, error) {
	id, _, err := a.lcs.LoadActiveProfileIDAndVersion(ctx)
	return id, err
}

func profileToResponse(p db.Profile, activeID string) rest.ProfileResponse {
	return rest.ProfileResponse{
		ID:                       p.ID,
		Name:                     p.Name,
		Provider:                 p.Provider,
		BaseURL:                  p.BaseURL,
		APIKeySet:                p.APIKey != "",
		APIKeyHint:               rest.MaskAPIKeyHint(p.APIKey),
		SummaryModel:             p.SummaryModel,
		ReviewModel:              p.ReviewModel,
		AskModel:                 p.AskModel,
		KnowledgeModel:           p.KnowledgeModel,
		ArchitectureDiagramModel: p.ArchitectureDiagramModel,
		ReportModel:              p.ReportModel,
		DraftModel:               p.DraftModel,
		TimeoutSecs:              p.TimeoutSecs,
		AdvancedMode:             p.AdvancedMode,
		IsActive:                 p.ID == activeID,
		CreatedAt:                rest.FormatProfileTime(p.CreatedAt),
		UpdatedAt:                rest.FormatProfileTime(p.UpdatedAt),
	}
}

// translateProfileStoreErr maps db sentinels into rest sentinels so
// rest handlers can errors.Is without importing internal/db.
func translateProfileStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, db.ErrProfileNotFound):
		return rest.ErrProfileNotFound
	case errors.Is(err, db.ErrDuplicateProfileName):
		return rest.ErrDuplicateProfileName
	case errors.Is(err, db.ErrProfileNameRequired):
		return rest.ErrProfileNameRequired
	case errors.Is(err, db.ErrProfileNameTooLong):
		return rest.ErrProfileNameTooLong
	case errors.Is(err, db.ErrTargetNoLongerActive):
		return rest.ErrProfileTargetNoLongerActive
	case errors.Is(err, db.ErrVersionConflict):
		return rest.ErrProfileVersionConflict
	case errors.Is(err, db.ErrEncryptionKeyRequired):
		return fmt.Errorf("%w: %v", rest.ErrLLMEncryptionKeyRequired, err)
	default:
		return err
	}
}

// ─────────────────────────────────────────────────────────────────────────
// LLM provider profiles slice 1 — adapter shims (codex-H2 / H3 / M5)
// ─────────────────────────────────────────────────────────────────────────
//
// These three shims bridge concrete db package types into the narrow
// interfaces resolution.ProfileAwareLLMResolverAdapter expects. The
// adapter lives in internal/llm/resolution and cannot import
// internal/db (cycle); the shims sit at the cli/serve.go layer where
// the cross-package wiring is allowed.

// profileAwareConfigStoreShim bridges *db.SurrealLLMConfigStore →
// resolution.ProfileAwareConfigStore. The shim materializes a
// resolution.ConfigSnapshot from db.LLMConfigSnapshot, copying every
// field across (no business logic in the shim itself).
type profileAwareConfigStoreShim struct {
	store *db.SurrealLLMConfigStore
}

func (s *profileAwareConfigStoreShim) LoadConfigSnapshot(ctx context.Context) (*resolution.ConfigSnapshot, error) {
	dbSnap, err := s.store.LoadConfigSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if dbSnap == nil {
		return nil, nil
	}
	return &resolution.ConfigSnapshot{
		ActiveProfileID:                dbSnap.ActiveProfileID,
		Version:                        dbSnap.Version,
		UpdatedAt:                      dbSnap.UpdatedAt,
		LegacyProvider:                 dbSnap.LegacyProvider,
		LegacyBaseURL:                  dbSnap.LegacyBaseURL,
		LegacyAPIKey:                   dbSnap.LegacyAPIKey,
		LegacySummaryModel:             dbSnap.LegacySummaryModel,
		LegacyReviewModel:              dbSnap.LegacyReviewModel,
		LegacyAskModel:                 dbSnap.LegacyAskModel,
		LegacyKnowledgeModel:           dbSnap.LegacyKnowledgeModel,
		LegacyArchitectureDiagramModel: dbSnap.LegacyArchitectureDiagramModel,
		LegacyReportModel:              dbSnap.LegacyReportModel,
		LegacyDraftModel:               dbSnap.LegacyDraftModel,
		LegacyTimeoutSecs:              dbSnap.LegacyTimeoutSecs,
		LegacyAdvancedMode:             dbSnap.LegacyAdvancedMode,
	}, nil
}

func (s *profileAwareConfigStoreShim) LoadLLMConfigVersion() (uint64, error) {
	return s.store.LoadLLMConfigVersion()
}

// profileAwareProfileStoreShim bridges *db.SurrealLLMProfileStore →
// resolution.ProfileAwareProfileStore. Translates db.Profile (with a
// cleartext APIKey field) into resolution.WorkspaceRecord.
type profileAwareProfileStoreShim struct {
	store *db.SurrealLLMProfileStore
}

func (s *profileAwareProfileStoreShim) LoadProfileForResolution(ctx context.Context, profileID string) (*resolution.WorkspaceRecord, error) {
	p, err := s.store.LoadProfile(ctx, profileID)
	if err != nil {
		if errors.Is(err, db.ErrProfileNotFound) {
			return nil, resolution.ErrProfileNotFound
		}
		return nil, err
	}
	return &resolution.WorkspaceRecord{
		Provider:                  p.Provider,
		BaseURL:                   p.BaseURL,
		APIKey:                    p.APIKey,
		SummaryModel:              p.SummaryModel,
		ReviewModel:               p.ReviewModel,
		AskModel:                  p.AskModel,
		KnowledgeModel:            p.KnowledgeModel,
		ArchitectureDiagramModel:  p.ArchitectureDiagramModel,
		ReportModel:               p.ReportModel,
		DraftModel:                p.DraftModel,
		TimeoutSecs:               p.TimeoutSecs,
		AdvancedMode:              p.AdvancedMode,
		ProfileID:                 p.ID,
		UpdatedAt:                 p.UpdatedAt,
		LastLegacyVersionConsumed: p.LastLegacyVersionConsumed,
	}, nil
}

func (s *profileAwareProfileStoreShim) LoadAllProfileIDs(ctx context.Context) ([]string, error) {
	return s.store.LoadAllProfileIDs(ctx)
}

// profileAwareReconcilerShim adapts the package-private
// db.reconcileLegacyToActive helper into the
// resolution.ProfileAwareReconciler interface. The actual helper is
// invoked via the exported wrapper db.ReconcileLegacyToActive (added
// alongside this shim) so the shim doesn't need internal db access.
type profileAwareReconcilerShim struct {
	surrealDB *db.SurrealDB
}

func (s *profileAwareReconcilerShim) ReconcileLegacyToActive(
	ctx context.Context,
	observedVersion uint64,
	observedWatermark uint64,
	activeID string,
) (resolution.ReconcileResult, error) {
	dbResult, err := db.ReconcileLegacyToActiveExported(ctx, s.surrealDB, observedVersion, observedWatermark, activeID)
	if err != nil {
		// Translate db sentinels → resolution sentinels so the adapter
		// can errors.Is against its own.
		if errors.Is(err, db.ErrVersionConflict) {
			return resolution.ReconcileResult{}, resolution.ErrVersionConflict
		}
		if errors.Is(err, db.ErrWatermarkConflict) {
			return resolution.ReconcileResult{}, resolution.ErrWatermarkConflict
		}
		return resolution.ReconcileResult{}, err
	}
	return resolution.ReconcileResult{
		ActuallyWrote: dbResult.ActuallyWrote,
		NewWatermark:  dbResult.NewWatermark,
	}, nil
}

// slogLivingWikiLogger bridges webhook.Logger to the global slog logger.
type slogLivingWikiLogger struct{}

func (l *slogLivingWikiLogger) Info(msg string, kv ...any) {
	slog.Info(msg, kv...)
}

func (l *slogLivingWikiLogger) Error(msg string, err error, kv ...any) {
	args := append([]any{"error", err}, kv...)
	slog.Error(msg, args...)
}
