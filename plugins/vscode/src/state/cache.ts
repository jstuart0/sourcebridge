// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

/**
 * DocCache — the replacement for the original session-scoped {@link
 * https://en.wikipedia.org/wiki/Cache_(computing) Map-as-cache} that
 * lived in `src/state/sessionCache.ts`. That class had no TTL, no
 * bound, and no version keying, which meant stale symbols kept
 * driving decoration and CodeLens providers long after a file had
 * been edited.
 *
 * This cache:
 *
 *  - is keyed by an external identifier (file path, repo id, symbol
 *    id) and a *version*. The providers pass
 *    `document.version` (VS Code's monotonic per-document edit
 *    counter) or `1` for keys that don't move, and a fetch is only
 *    served from cache when the stored version matches the requested
 *    version.
 *  - honours a per-entry TTL (default 2 minutes) so even keys that
 *    never change drop out eventually.
 *  - enforces an LRU cap so a long-running session with hundreds of
 *    files open can't grow the cache without bound.
 *
 * All operations are synchronous. Fetches are orchestrated by
 * callers with `getOrFetch` — that method dedupes concurrent fetches
 * so simultaneous lens + hover + decoration runs on the same file
 * issue a single network call.
 */

export interface CacheOptions {
  /** Maximum live entries. Oldest by last access is evicted. */
  max: number;
  /** Time-to-live in milliseconds. Zero = no TTL. */
  ttlMs: number;
}

interface Entry<T> {
  value: T;
  version: number;
  expiresAt: number; // ms since epoch, or 0 for no TTL
  /**
   * Monotonic access rank — strictly increases on each read or write.
   * Using a counter rather than Date.now() so millisecond-resolution
   * ties between rapid operations don't defeat LRU ordering.
   */
  rank: number;
}

export class DocCache<T> {
  private readonly map = new Map<string, Entry<T>>();
  private readonly inflight = new Map<string, Promise<T>>();
  private readonly max: number;
  private readonly ttlMs: number;
  private rankCounter = 0;

  constructor(opts: CacheOptions) {
    this.max = Math.max(1, opts.max);
    this.ttlMs = Math.max(0, opts.ttlMs);
  }

  private nextRank(): number {
    this.rankCounter += 1;
    return this.rankCounter;
  }

  /**
   * Read from cache. Returns undefined when the key is missing,
   * expired, or present at a different version.
   */
  get(key: string, version: number): T | undefined {
    const entry = this.map.get(key);
    if (!entry) return undefined;
    if (entry.version !== version) return undefined;
    if (entry.expiresAt !== 0 && Date.now() > entry.expiresAt) {
      this.map.delete(key);
      return undefined;
    }
    entry.rank = this.nextRank();
    return entry.value;
  }

  /** Unconditionally store. Evicts the oldest entry if over capacity. */
  set(key: string, version: number, value: T): void {
    const expiresAt = this.ttlMs === 0 ? 0 : Date.now() + this.ttlMs;
    this.map.set(key, { value, version, expiresAt, rank: this.nextRank() });
    if (this.map.size > this.max) this.evictOldest();
  }

  /**
   * Deduped fetch-or-cache. If the key at the given version is in
   * the cache and unexpired, returns it. Otherwise invokes `loader`,
   * caches the result, and returns it. Concurrent calls for the same
   * key+version share one `loader` invocation.
   */
  async getOrFetch(
    key: string,
    version: number,
    loader: () => Promise<T>,
  ): Promise<T> {
    const hit = this.get(key, version);
    if (hit !== undefined) return hit;
    const dedupeKey = `${key}:${version}`;
    const existing = this.inflight.get(dedupeKey);
    if (existing) return existing;
    const promise = loader()
      .then((value) => {
        this.set(key, version, value);
        return value;
      })
      .finally(() => {
        this.inflight.delete(dedupeKey);
      });
    this.inflight.set(dedupeKey, promise);
    return promise;
  }

  /** Drop any entry at the given key regardless of version. */
  invalidate(key: string): void {
    this.map.delete(key);
  }

  /** Wipe everything. Useful on config change / reconnect. */
  clear(): void {
    this.map.clear();
    this.inflight.clear();
  }

  /** Approximate size, for logs and debugging only. */
  size(): number {
    return this.map.size;
  }

  private evictOldest(): void {
    let oldestKey: string | undefined;
    let oldestRank = Number.POSITIVE_INFINITY;
    for (const [k, v] of this.map) {
      if (v.rank < oldestRank) {
        oldestRank = v.rank;
        oldestKey = k;
      }
    }
    if (oldestKey !== undefined) this.map.delete(oldestKey);
  }
}
