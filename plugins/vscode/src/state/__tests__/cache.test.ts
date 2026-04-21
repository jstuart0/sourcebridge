import { DocCache } from "../cache";

function makeCache<T>(ttlMs = 60_000, max = 50): DocCache<T> {
  return new DocCache<T>({ ttlMs, max });
}

describe("DocCache", () => {
  test("hit-miss by key + version", () => {
    const c = makeCache<string>();
    c.set("f", 1, "alpha");
    expect(c.get("f", 1)).toBe("alpha");
    expect(c.get("f", 2)).toBeUndefined(); // version mismatch
    expect(c.get("g", 1)).toBeUndefined(); // missing key
  });

  test("TTL expiry drops the entry on read", async () => {
    const c = makeCache<string>(10);
    c.set("f", 1, "alpha");
    await new Promise((r) => setTimeout(r, 25));
    expect(c.get("f", 1)).toBeUndefined();
  });

  test("LRU eviction when over capacity", () => {
    const c = makeCache<number>(60_000, 3);
    c.set("a", 1, 1);
    c.set("b", 1, 2);
    c.set("c", 1, 3);
    // Touch a so b becomes the oldest by access.
    c.get("a", 1);
    c.set("d", 1, 4); // evicts b
    expect(c.get("a", 1)).toBe(1);
    expect(c.get("b", 1)).toBeUndefined();
    expect(c.get("c", 1)).toBe(3);
    expect(c.get("d", 1)).toBe(4);
  });

  test("getOrFetch dedupes concurrent loaders", async () => {
    const c = makeCache<string>();
    let calls = 0;
    const loader = async () => {
      calls++;
      await new Promise((r) => setTimeout(r, 10));
      return "value";
    };
    const [a, b, d] = await Promise.all([
      c.getOrFetch("k", 1, loader),
      c.getOrFetch("k", 1, loader),
      c.getOrFetch("k", 1, loader),
    ]);
    expect(a).toBe("value");
    expect(b).toBe("value");
    expect(d).toBe("value");
    expect(calls).toBe(1); // deduped
  });

  test("getOrFetch hits cache on subsequent call", async () => {
    const c = makeCache<number>();
    let calls = 0;
    const loader = async () => (++calls, 42);
    expect(await c.getOrFetch("k", 1, loader)).toBe(42);
    expect(await c.getOrFetch("k", 1, loader)).toBe(42);
    expect(calls).toBe(1);
  });

  test("invalidate + clear", () => {
    const c = makeCache<string>();
    c.set("a", 1, "x");
    c.set("b", 1, "y");
    c.invalidate("a");
    expect(c.get("a", 1)).toBeUndefined();
    expect(c.get("b", 1)).toBe("y");
    c.clear();
    expect(c.get("b", 1)).toBeUndefined();
  });

  test("version bump on same key purges the old value", () => {
    const c = makeCache<number>();
    c.set("f", 1, 100);
    expect(c.get("f", 1)).toBe(100);
    c.set("f", 2, 200);
    expect(c.get("f", 1)).toBeUndefined(); // old version gone
    expect(c.get("f", 2)).toBe(200);
  });

  test("inflight cleared on loader rejection so retries can proceed", async () => {
    const c = makeCache<string>();
    let attempt = 0;
    const flaky = async () => {
      attempt++;
      if (attempt === 1) throw new Error("boom");
      return "recovered";
    };
    await expect(c.getOrFetch("k", 1, flaky)).rejects.toThrow("boom");
    // Next attempt must not get stuck on the rejected promise.
    expect(await c.getOrFetch("k", 1, flaky)).toBe("recovered");
    expect(attempt).toBe(2);
  });
});
