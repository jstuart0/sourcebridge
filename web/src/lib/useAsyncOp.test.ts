import { describe, it, expect } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useAsyncOp } from "./useAsyncOp";

function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void } {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

describe("useAsyncOp", () => {
  it("isPending is true during run, false after", async () => {
    const { result } = renderHook(() => useAsyncOp());
    const d = deferred<void>();
    let runPromise!: Promise<void>;

    act(() => {
      runPromise = result.current.run("op", () => d.promise);
    });

    expect(result.current.isPending("op")).toBe(true);

    await act(async () => { d.resolve(); await runPromise; });

    expect(result.current.isPending("op")).toBe(false);
  });

  it("finally cleanup fires even on fn throw (key removed from pending)", async () => {
    const { result } = renderHook(() => useAsyncOp());
    const d = deferred<void>();
    let runPromise!: Promise<void>;

    act(() => {
      runPromise = result.current.run("op", () => d.promise);
    });

    expect(result.current.isPending("op")).toBe(true);

    await act(async () => {
      d.reject(new Error("boom"));
      await runPromise.catch(() => {});
    });

    expect(result.current.isPending("op")).toBe(false);
  });

  it("rethrows fn error to caller", async () => {
    const { result } = renderHook(() => useAsyncOp());
    let caught: unknown;
    await act(async () => {
      try {
        await result.current.run("op", () => Promise.reject(new Error("fail")));
      } catch (e) {
        caught = e;
      }
    });
    expect((caught as Error).message).toBe("fail");
  });

  it("run returns fn resolved value", async () => {
    const { result } = renderHook(() => useAsyncOp());
    let value: number | undefined;
    await act(async () => {
      value = await result.current.run("op", () => Promise.resolve(42));
    });
    expect(value).toBe(42);
  });

  it("multiple concurrent runs with different keys are independent", async () => {
    const { result } = renderHook(() => useAsyncOp());
    const dA = deferred<void>();
    const dB = deferred<void>();

    act(() => {
      void result.current.run("a", () => dA.promise);
      void result.current.run("b", () => dB.promise);
    });

    expect(result.current.isPending("a")).toBe(true);
    expect(result.current.isPending("b")).toBe(true);

    await act(async () => { dA.resolve(); });

    expect(result.current.isPending("a")).toBe(false);
    expect(result.current.isPending("b")).toBe(true);

    await act(async () => { dB.resolve(); });

    expect(result.current.isPending("b")).toBe(false);
  });

  it("concurrent same-key runs both appear in pending (no-dedup contract)", async () => {
    const { result } = renderHook(() => useAsyncOp());
    const d1 = deferred<string>();
    const d2 = deferred<string>();
    let r1: string | undefined;
    let r2: string | undefined;

    act(() => {
      void result.current.run("op", () => d1.promise).then((v) => { r1 = v; });
      void result.current.run("op", () => d2.promise).then((v) => { r2 = v; });
    });

    // Both in flight: key is in pending set
    expect(result.current.isPending("op")).toBe(true);

    await act(async () => { d1.resolve("first"); });
    // first resolved but second still running
    expect(result.current.isPending("op")).toBe(true);
    expect(r1).toBe("first");

    await act(async () => { d2.resolve("second"); });
    expect(result.current.isPending("op")).toBe(false);
    expect(r2).toBe("second");
  });
});
