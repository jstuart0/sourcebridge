// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

// Package tlsreload provides a Watcher that hot-reloads mTLS material
// (client cert, key, CA bundle) from disk and exposes the current
// material to a gRPC client through atomic accessors. R3 slice 4.
//
// Why this package exists separately from internal/worker/client.go:
//
//  1. The cert load + validate + watch logic is meaningful surface
//     (CA chain verification, EKU, key match, SAN, K8s symlink-swap
//     awareness, periodic-poll fallback). Putting it inline in
//     client.go makes that file impossible to reason about.
//  2. The Watcher has its own tests (this package) without dragging
//     in the gRPC-server fixtures the worker.Client tests use.
//  3. The Watcher is reusable. If we ever add another mTLS-bearing
//     gRPC client (e.g. a future API↔scheduler channel), it can
//     instantiate the same type.
//
// Validation contract (what counts as a "valid" cert+key+CA bundle):
//
//   - The cert + key parse via tls.X509KeyPair. This implicitly
//     verifies the public key in the cert matches the private key.
//   - The cert is currently valid (NotBefore <= now <= NotAfter).
//   - The cert chains to the configured CA pool when ChainVerification
//     is true (for production deployments with a real cert-manager CA).
//   - The cert has the ClientAuth extended key usage (EKU).
//   - The cert's SAN matches the configured ServiceIdentity (when set).
//   - The CA bundle has at least one PEM-encoded certificate.
//
// Any failure leaves the previously-active cert in use and increments
// the validation-failure counter. The Watcher logs at WARN level on
// failed reloads so operators can investigate without a flood of noise.
//
// Kubernetes secret-volume awareness:
//
// Projected Secret volumes (the standard for cert-manager + Reloader)
// update via symlink swaps on the *directory*, not on individual files.
// fsnotify on a single file misses these events. The Watcher therefore
// watches the parent directory of each path AND each file directly,
// AND polls mtime every PollInterval (default 5 minutes) as a
// belt-and-suspenders safety net for environments where fsnotify's
// inotify backend is unavailable (rare).
package tlsreload

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Sentinel errors. Callers can check for these with errors.Is to
// distinguish failure modes (e.g. test assertions, structured metrics).
var (
	ErrEmptyCABundle      = errors.New("ca bundle has no PEM certificates")
	ErrCertNotYetValid    = errors.New("cert NotBefore is in the future")
	ErrCertExpired        = errors.New("cert is expired")
	ErrChainVerifyFailed  = errors.New("cert does not chain to the configured CA")
	ErrMissingClientAuth  = errors.New("cert is missing the ClientAuth EKU")
	ErrServiceIdentityNoMatch = errors.New("cert SAN does not match the configured service identity")
)

// Config configures a Watcher. All paths are required; ServiceIdentity
// is optional (when empty, SAN matching is skipped).
type Config struct {
	// CertPath is the path to the PEM-encoded client cert.
	CertPath string
	// KeyPath is the path to the PEM-encoded private key.
	KeyPath string
	// CAPath is the path to the PEM-encoded CA bundle the worker's
	// server cert must chain to.
	CAPath string
	// ServiceIdentity is the expected DNS SAN on the cert (e.g.
	// "api.sourcebridge.svc.cluster.local"). Empty disables SAN matching.
	ServiceIdentity string
	// ChainVerification, when true, verifies the cert chains to the
	// configured CA pool. Set to false only for self-signed test certs
	// where the cert IS its own root.
	ChainVerification bool
	// PollInterval is how often the watcher polls cert file mtimes as
	// a belt-and-suspenders backup to fsnotify. Zero = 5 minutes.
	PollInterval time.Duration
	// Logger is the structured logger; nil = slog.Default().
	Logger *slog.Logger
}

// Candidate is a Stage-validated, not-yet-Committed mTLS material set.
// It carries a monotonically-increasing Generation so a stale
// candidate cannot clobber a newer one if two reloads race.
//
// R3 followups B2 (codex r1+r1b on followups delivery): Candidates exist
// so the worker.Client can probe a new gRPC conn against the candidate
// material BEFORE the watcher's atomic pointers advance. A failed probe
// drops the candidate without ever exposing the rejected cert via
// Cert()/GetClientCertificate.
type Candidate struct {
	Generation uint64
	Cert       *tls.Certificate
	RootCAs    *x509.CertPool
}

// Watcher loads, validates, and serves mTLS material to a gRPC client.
// All accessors are safe for concurrent use; readers never block on
// reload activity.
type Watcher struct {
	cfg Config
	log *slog.Logger

	// Atomic pointers to the currently-active (committed) material.
	// Writers hold commitMu; readers Load() lock-free.
	cert atomic.Pointer[tls.Certificate]
	caP  atomic.Pointer[x509.CertPool]

	// Generation counters. stagedGen advances on every successful
	// Stage; committedGen advances on every successful Commit. The
	// constructor's initial Stage+Commit raises both to 1 atomically.
	stagedGen    atomic.Uint64
	committedGen atomic.Uint64

	// Last-known mtimes. Used by the poll loop to decide whether to
	// re-read. Writers hold reloadMu.
	lastCertMtime atomic.Pointer[time.Time]
	lastKeyMtime  atomic.Pointer[time.Time]
	lastCAMtime   atomic.Pointer[time.Time]

	// reloadMu serializes reload attempts (Stage). The atomic accessors
	// stay non-blocking. The Commit path uses commitMu, NOT reloadMu —
	// callbacks fired post-Stage call Commit asynchronously and a
	// reloadMu hold here would deadlock if a callback ran on the same
	// goroutine as Stage (it doesn't today, but the mutex split is the
	// codex-recommended safety guarantee).
	reloadMu sync.Mutex

	// commitMu serializes Commit attempts. Held only for the
	// generation check + atomic swap; never held while invoking
	// callbacks. Reads of cert/caP do not require this lock — they
	// are atomic.
	commitMu sync.Mutex

	// candidateCallbacks: subscribers that receive each newly-Staged
	// Candidate. The R3-followups Client wires this and Commits the
	// candidate after a successful probe of a new conn built from it.
	candidateCallbacksMu sync.RWMutex
	candidateCallbacks   []func(c Candidate)

	// reloadCallbacks: legacy success/error notifications fired after
	// every reload-driven Stage attempt (regardless of whether any
	// subscriber Commits). Kept for backward compatibility with
	// existing tests/observability tooling. New code should subscribe
	// via OnCandidate.
	reloadCallbacksMu sync.RWMutex
	reloadCallbacks   []func(success bool, err error)

	// Counters for observability. Atomic so accessors don't lock.
	loadSuccessCount  atomic.Int64
	loadFailureCount  atomic.Int64
	commitCount       atomic.Int64
	commitStaleCount  atomic.Int64

	// Lifecycle.
	fsw    *fsnotify.Watcher
	stopCh chan struct{}
	wg     sync.WaitGroup
	closed atomic.Bool
}

// New constructs a Watcher and performs the initial cert/key/CA load.
// Returns an error on any initial-load failure: the caller should
// refuse to start the gRPC client when the initial cert is invalid
// rather than silently degrade. Callers MUST call Start to begin file
// watching and Close when done.
func New(cfg Config) (*Watcher, error) {
	if cfg.CertPath == "" || cfg.KeyPath == "" || cfg.CAPath == "" {
		return nil, fmt.Errorf("tlsreload: cert_path, key_path, and ca_path are required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Minute
	}

	w := &Watcher{
		cfg:    cfg,
		log:    log,
		stopCh: make(chan struct{}),
	}

	// Initial load — Stage + immediate Commit. Must succeed.
	cand, err := w.Stage()
	if err != nil {
		return nil, fmt.Errorf("tlsreload: initial stage: %w", err)
	}
	ok, err := w.Commit(cand)
	if err != nil {
		return nil, fmt.Errorf("tlsreload: initial commit: %w", err)
	}
	if !ok {
		// Cannot happen on first Commit — committedGen is 0 and the
		// candidate's Generation is 1. Defensive belt-and-suspenders.
		return nil, fmt.Errorf("tlsreload: initial commit rejected")
	}
	return w, nil
}

// Start begins file watching and the periodic poll. Idempotent: a
// second call is a no-op. Errors when fsnotify cannot be initialized
// (rare; usually only on platforms without inotify).
func (w *Watcher) Start() error {
	if w == nil || w.closed.Load() {
		return errors.New("tlsreload: watcher closed")
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("tlsreload: fsnotify watcher: %w", err)
	}
	w.fsw = fsw

	// Watch each parent directory (catches K8s symlink swaps) AND
	// each file (catches direct overwrites).
	dirs := uniqueParentDirs(w.cfg.CertPath, w.cfg.KeyPath, w.cfg.CAPath)
	for _, d := range dirs {
		if err := fsw.Add(d); err != nil {
			w.log.Warn("tlsreload: failed to watch directory", "path", d, "error", err)
		}
	}
	for _, f := range []string{w.cfg.CertPath, w.cfg.KeyPath, w.cfg.CAPath} {
		// Best-effort — direct file watch fails when the file is
		// behind a projected-volume symlink that doesn't exist as a
		// real inode. The directory watch will catch the change.
		_ = fsw.Add(f)
	}

	w.wg.Add(2)
	go w.watchLoop()
	go w.pollLoop()
	return nil
}

// Close stops watching and releases resources. Safe to call multiple
// times. Pending callbacks are not unregistered — their next firing
// is the watcher's last action.
func (w *Watcher) Close() error {
	if w == nil {
		return nil
	}
	if !w.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(w.stopCh)
	if w.fsw != nil {
		_ = w.fsw.Close()
	}
	w.wg.Wait()
	return nil
}

// Cert returns the current active cert. Lock-free. Returns nil if
// the watcher has been closed and somehow the pointer was cleared
// (defensive — the constructor always populates it).
func (w *Watcher) Cert() *tls.Certificate { return w.cert.Load() }

// RootCAs returns the current active CA pool. Lock-free.
func (w *Watcher) RootCAs() *x509.CertPool { return w.caP.Load() }

// GetClientCertificate is the tls.Config.GetClientCertificate hook.
// On every TLS handshake the runtime calls this; we serve the latest
// cert without locking. R3 slice 4: this is the function that makes
// future RPCs use the new cert after a reload — the gRPC connection
// can stay open across rotations as long as TLS handshakes pick up
// the new material via this accessor.
func (w *Watcher) GetClientCertificate(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	c := w.cert.Load()
	if c == nil {
		return nil, errors.New("tlsreload: no active cert")
	}
	return c, nil
}

// OnReload registers a callback invoked after every Reload attempt
// (both successful Stage and failed Stage). Subscribers should be
// light: the watcher invokes them serially under no specific deadline.
//
// R3 slice 4 (legacy contract): early subscribers used this to cycle
// their gRPC connection on every successful Stage. R3 followups B2
// shifted that responsibility to OnCandidate so callbacks can refuse
// to advance the watcher's committed material on a probe failure.
// OnReload remains for purely-observability subscribers.
//
// Callbacks fire OUTSIDE reloadMu and commitMu — a callback is free
// to call Commit directly without deadlocking.
func (w *Watcher) OnReload(cb func(success bool, err error)) {
	if cb == nil {
		return
	}
	w.reloadCallbacksMu.Lock()
	w.reloadCallbacks = append(w.reloadCallbacks, cb)
	w.reloadCallbacksMu.Unlock()
}

// OnCandidate registers a callback that receives every newly-Staged
// Candidate. Subscribers (typically the worker.Client) probe a new
// gRPC connection built from the candidate; on success they call
// Commit on this watcher, then swap their internal bundle. On failure
// they drop the candidate without calling Commit, leaving the
// watcher's active material unchanged.
//
// Callbacks fire OUTSIDE reloadMu and commitMu — a callback is free
// to call Commit directly without deadlocking. Multiple subscribers
// race; the first to Commit wins, and later subscribers will see
// ok=false on a stale Commit.
//
// R3 followups B2.
func (w *Watcher) OnCandidate(cb func(c Candidate)) {
	if cb == nil {
		return
	}
	w.candidateCallbacksMu.Lock()
	w.candidateCallbacks = append(w.candidateCallbacks, cb)
	w.candidateCallbacksMu.Unlock()
}

// Reload triggers a reload-driven Stage and fires candidate-callbacks.
// Subscribers (typically the worker.Client) decide whether to Commit
// the resulting Candidate after their own validation (e.g. a post-
// rotation health probe). On Stage failure the previously-active
// material remains in use, the validation-failure counter increments,
// and legacy reload-callbacks fire with success=false. Returns the
// validation error so callers can log or retry.
//
// R3 followups B2: this is the public auto-trigger entry point used
// by the fsnotify and poll loops. It does NOT auto-Commit — that
// behavior moved to the OnCandidate callback chain so a rejected
// post-rotation probe can drop the candidate.
func (w *Watcher) Reload() error {
	cand, err := w.Stage()
	if err != nil {
		w.fireReloadCallbacks(false, err)
		return err
	}
	w.fireCandidateCallbacks(cand)
	w.fireReloadCallbacks(true, nil)
	return nil
}

// Stage reads cert+key+CA from disk, validates, and returns a
// Candidate with a freshly-incremented Generation. Does NOT mutate
// the watcher's active atomic pointers. The caller commits via
// Commit() after their own validation passes.
//
// Stage is serialized via reloadMu so concurrent callers never race
// on the disk reads, but Commit takes its OWN mutex and does not
// block on Stage — callbacks dispatched after Stage can call Commit
// without deadlocking.
func (w *Watcher) Stage() (Candidate, error) {
	w.reloadMu.Lock()
	defer w.reloadMu.Unlock()

	cert, caPool, err := w.loadAndValidate()
	if err != nil {
		w.loadFailureCount.Add(1)
		w.log.Warn("tlsreload: stage validation failed; keeping previously-committed cert",
			"error", err,
			"cert_path", w.cfg.CertPath,
			"ca_path", w.cfg.CAPath)
		return Candidate{}, err
	}

	gen := w.stagedGen.Add(1)
	w.recordMtimes()

	leaf := certLeaf(cert)
	w.log.Info("tlsreload: cert staged",
		"cert_path", w.cfg.CertPath,
		"subject", leafSubject(leaf),
		"not_after", leafNotAfter(leaf),
		"generation", gen)
	return Candidate{Generation: gen, Cert: cert, RootCAs: caPool}, nil
}

// Commit makes a candidate the active material iff it is strictly
// newer than the currently-committed generation. Idempotent against
// the active candidate (same generation, same pointers): returns
// ok=true with no state change. Returns ok=false (no error) when
// the candidate is stale (older or already-superseded). Returns
// (false, error) when the watcher is closed.
//
// R3 followups B2 (codex r1b on followups delivery): the predicate
// is "first probe-passing candidate after the current commit wins;
// later candidates win only when STRICTLY newer." This intentionally
// allows an older-but-validated candidate to commit ahead of a newer
// candidate whose probe is still in flight — we want the freshest
// cert that has actually passed an mTLS handshake to be active, not
// the freshest cert that has only been Stage-validated.
func (w *Watcher) Commit(c Candidate) (ok bool, err error) {
	if w == nil || w.closed.Load() {
		return false, errors.New("tlsreload: watcher closed")
	}
	if c.Cert == nil || c.RootCAs == nil {
		return false, errors.New("tlsreload: cannot commit empty candidate")
	}

	w.commitMu.Lock()
	defer w.commitMu.Unlock()

	committed := w.committedGen.Load()
	if c.Generation == committed {
		// Idempotent: only treat as a "no-op accept" when the candidate
		// is exactly the active material. A different cert with the
		// same generation is a programming error.
		if w.cert.Load() == c.Cert {
			return true, nil
		}
		return false, errors.New("tlsreload: commit at active generation with different cert pointer")
	}
	if c.Generation < committed {
		w.commitStaleCount.Add(1)
		w.log.Info("tlsreload: stale candidate dropped at commit",
			"candidate_generation", c.Generation,
			"committed_generation", committed)
		return false, nil
	}

	// Strictly newer: install.
	w.cert.Store(c.Cert)
	w.caP.Store(c.RootCAs)
	w.committedGen.Store(c.Generation)
	w.commitCount.Add(1)
	w.loadSuccessCount.Add(1)

	leaf := certLeaf(c.Cert)
	w.log.Info("tlsreload: candidate committed",
		"generation", c.Generation,
		"subject", leafSubject(leaf),
		"not_after", leafNotAfter(leaf),
		"commit_count", w.commitCount.Load())
	return true, nil
}

// LoadSuccessCount returns the number of committed candidates (including
// the initial load). Useful for tests.
func (w *Watcher) LoadSuccessCount() int64 { return w.loadSuccessCount.Load() }

// LoadFailureCount returns the number of Stage-validation failures.
// Useful for tests / metrics.
func (w *Watcher) LoadFailureCount() int64 { return w.loadFailureCount.Load() }

// CommitCount returns the number of successful Commits.
func (w *Watcher) CommitCount() int64 { return w.commitCount.Load() }

// CommitStaleCount returns the number of Commit attempts dropped as
// stale (older or equal-with-different-cert generation).
func (w *Watcher) CommitStaleCount() int64 { return w.commitStaleCount.Load() }

// StagedGeneration returns the most recently-Staged generation.
// committedGen <= stagedGen always.
func (w *Watcher) StagedGeneration() uint64 { return w.stagedGen.Load() }

// CommittedGeneration returns the most recently-committed generation.
func (w *Watcher) CommittedGeneration() uint64 { return w.committedGen.Load() }

// loadAndValidate reads cert + key + CA from disk and runs the full
// validation contract. On any failure returns the error WITHOUT
// touching the active material.
func (w *Watcher) loadAndValidate() (*tls.Certificate, *x509.CertPool, error) {
	clientCert, err := tls.LoadX509KeyPair(w.cfg.CertPath, w.cfg.KeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load cert/key: %w", err)
	}
	leaf, err := x509.ParseCertificate(clientCert.Certificate[0])
	if err != nil {
		return nil, nil, fmt.Errorf("parse leaf cert: %w", err)
	}
	clientCert.Leaf = leaf

	now := time.Now()
	if now.Before(leaf.NotBefore) {
		return nil, nil, fmt.Errorf("%w (NotBefore=%s, now=%s)", ErrCertNotYetValid, leaf.NotBefore, now)
	}
	if now.After(leaf.NotAfter) {
		return nil, nil, fmt.Errorf("%w (NotAfter=%s, now=%s)", ErrCertExpired, leaf.NotAfter, now)
	}

	// EKU: must include client auth.
	hasClientAuth := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
			break
		}
	}
	if !hasClientAuth {
		return nil, nil, ErrMissingClientAuth
	}

	// Service identity (SAN).
	if w.cfg.ServiceIdentity != "" {
		matched := false
		for _, san := range leaf.DNSNames {
			if san == w.cfg.ServiceIdentity {
				matched = true
				break
			}
		}
		if !matched {
			return nil, nil, fmt.Errorf("%w: cert dns_names=%v want=%s",
				ErrServiceIdentityNoMatch, leaf.DNSNames, w.cfg.ServiceIdentity)
		}
	}

	// CA bundle.
	caPEM, err := os.ReadFile(w.cfg.CAPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read ca bundle: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, nil, ErrEmptyCABundle
	}

	// Chain verify (skipped for self-signed test fixtures where the
	// cert is its own CA).
	if w.cfg.ChainVerification {
		opts := x509.VerifyOptions{
			Roots:     caPool,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		if _, verr := leaf.Verify(opts); verr != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrChainVerifyFailed, verr)
		}
	}

	return &clientCert, caPool, nil
}

// fireReloadCallbacks invokes every registered OnReload callback
// serially. Panics in callbacks are recovered so a buggy subscriber
// can't kill the watcher loop. Callbacks run OUTSIDE the watcher's
// internal mutexes.
func (w *Watcher) fireReloadCallbacks(ok bool, err error) {
	w.reloadCallbacksMu.RLock()
	cbs := make([]func(bool, error), len(w.reloadCallbacks))
	copy(cbs, w.reloadCallbacks)
	w.reloadCallbacksMu.RUnlock()
	for _, cb := range cbs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					w.log.Error("tlsreload: OnReload callback panicked", "panic", r)
				}
			}()
			cb(ok, err)
		}()
	}
}

// fireCandidateCallbacks invokes every registered OnCandidate callback
// serially with the staged Candidate. Panics in callbacks are
// recovered so a buggy subscriber can't kill the watcher loop.
// Callbacks run OUTSIDE the watcher's internal mutexes so they can
// call Commit without deadlocking.
func (w *Watcher) fireCandidateCallbacks(c Candidate) {
	w.candidateCallbacksMu.RLock()
	cbs := make([]func(Candidate), len(w.candidateCallbacks))
	copy(cbs, w.candidateCallbacks)
	w.candidateCallbacksMu.RUnlock()
	for _, cb := range cbs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					w.log.Error("tlsreload: OnCandidate callback panicked", "panic", r)
				}
			}()
			cb(c)
		}()
	}
}

func (w *Watcher) recordMtimes() {
	if t, err := mtimeOf(w.cfg.CertPath); err == nil {
		w.lastCertMtime.Store(&t)
	}
	if t, err := mtimeOf(w.cfg.KeyPath); err == nil {
		w.lastKeyMtime.Store(&t)
	}
	if t, err := mtimeOf(w.cfg.CAPath); err == nil {
		w.lastCAMtime.Store(&t)
	}
}

// watchLoop drains fsnotify events and triggers a reload whenever any
// of the watched paths show activity. We deliberately don't try to
// distinguish "real" cert change from spurious chmod/atime events —
// loadAndSwap is idempotent and cheap, and false positives are
// safer than missing a real rotation.
func (w *Watcher) watchLoop() {
	defer w.wg.Done()
	if w.fsw == nil {
		return
	}
	// Coalesce bursts of events (K8s symlink swaps emit several events
	// in tight succession). We trigger one reload per quiet window.
	const debounce = 250 * time.Millisecond
	var pending bool
	timer := time.NewTimer(0)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	defer timer.Stop()

	flush := func() {
		if pending {
			pending = false
			_ = w.Reload()
		}
	}

	for {
		select {
		case <-w.stopCh:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if !w.eventTouchesWatchedPath(ev) {
				continue
			}
			pending = true
			// Reset the debounce timer.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(debounce)
		case <-timer.C:
			flush()
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.log.Warn("tlsreload: fsnotify error", "error", err)
		}
	}
}

func (w *Watcher) eventTouchesWatchedPath(ev fsnotify.Event) bool {
	for _, p := range []string{w.cfg.CertPath, w.cfg.KeyPath, w.cfg.CAPath} {
		if ev.Name == p {
			return true
		}
		// Directory event without a specific filename — coarse but
		// safe: trigger a reload anyway. K8s symlink swaps surface
		// as Create/Remove on the directory's "..data" entry.
		if filepath.Dir(p) == filepath.Dir(ev.Name) {
			return true
		}
	}
	return false
}

// pollLoop periodically re-reads file mtimes. When any file's mtime
// has advanced since the last successful load, we trigger a reload.
// This catches platforms where fsnotify is unavailable AND any tail
// scenarios where a fast symlink swap is missed by the inotify backend.
func (w *Watcher) pollLoop() {
	defer w.wg.Done()
	t := time.NewTicker(w.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-t.C:
			if w.anyMtimeAdvanced() {
				_ = w.Reload()
			}
		}
	}
}

func (w *Watcher) anyMtimeAdvanced() bool {
	for path, last := range map[string]*atomic.Pointer[time.Time]{
		w.cfg.CertPath: &w.lastCertMtime,
		w.cfg.KeyPath:  &w.lastKeyMtime,
		w.cfg.CAPath:   &w.lastCAMtime,
	} {
		t, err := mtimeOf(path)
		if err != nil {
			continue
		}
		prev := last.Load()
		if prev == nil || t.After(*prev) {
			return true
		}
	}
	return false
}

// ─── helpers ─────────────────────────────────────────────────────────

func mtimeOf(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func uniqueParentDirs(paths ...string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		d := filepath.Dir(p)
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

func certLeaf(c *tls.Certificate) *x509.Certificate {
	if c == nil {
		return nil
	}
	if c.Leaf != nil {
		return c.Leaf
	}
	if len(c.Certificate) == 0 {
		return nil
	}
	leaf, _ := x509.ParseCertificate(c.Certificate[0])
	return leaf
}

func leafSubject(c *x509.Certificate) string {
	if c == nil {
		return ""
	}
	return c.Subject.String()
}

func leafNotAfter(c *x509.Certificate) string {
	if c == nil {
		return ""
	}
	return c.NotAfter.Format(time.RFC3339)
}
