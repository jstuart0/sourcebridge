import { describe, it, expect, vi, afterEach } from "vitest";
import { renderHook, act, cleanup } from "@testing-library/react";

interface SSEEvent {
  data?: string;
}

class MockEventSource {
  url: string;
  onmessage: ((event: SSEEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  private listeners: Record<string, (event: SSEEvent) => void> = {};
  close = vi.fn();

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }

  addEventListener(event: string, handler: (event: SSEEvent) => void) {
    this.listeners[event] = handler;
  }

  simulateMessage(data: string) {
    this.onmessage?.({ data });
  }

  simulateError() {
    this.onerror?.();
  }

  simulateDone() {
    this.listeners["done"]?.({});
  }

  static instances: MockEventSource[] = [];
  static reset() {
    MockEventSource.instances = [];
  }
}

vi.stubGlobal("EventSource", MockEventSource);

afterEach(() => {
  cleanup();
  MockEventSource.reset();
});

// Import after mock is established
import { useSSE } from "./sse";

function getLatestSource(): MockEventSource {
  return MockEventSource.instances[MockEventSource.instances.length - 1];
}

describe("useSSE", () => {
  it("returns initial state with empty data, not streaming, no error", () => {
    const { result } = renderHook(() => useSSE("http://localhost/events"));
    expect(result.current.data).toBe("");
    expect(result.current.isStreaming).toBe(false);
    expect(result.current.error).toBeNull();
  });

  it("creates EventSource and sets isStreaming to true on start", () => {
    const { result } = renderHook(() => useSSE("http://localhost/events"));

    act(() => {
      result.current.start();
    });

    expect(result.current.isStreaming).toBe(true);
    const source = getLatestSource();
    expect(source).toBeDefined();
    expect(source.url).toBe("http://localhost/events");
  });

  it("accumulates data from message events", () => {
    const { result } = renderHook(() => useSSE("http://localhost/events"));

    act(() => {
      result.current.start();
    });

    const source = getLatestSource();

    act(() => {
      source.simulateMessage("hello ");
    });
    expect(result.current.data).toBe("hello ");

    act(() => {
      source.simulateMessage("world");
    });
    expect(result.current.data).toBe("hello world");
  });

  it("sets isStreaming to false on done event", () => {
    const { result } = renderHook(() => useSSE("http://localhost/events"));

    act(() => {
      result.current.start();
    });

    expect(result.current.isStreaming).toBe(true);

    const source = getLatestSource();

    act(() => {
      source.simulateDone();
    });

    expect(result.current.isStreaming).toBe(false);
    expect(source.close).toHaveBeenCalled();
  });

  it("sets error message and stops streaming on error", () => {
    const { result } = renderHook(() => useSSE("http://localhost/events"));

    act(() => {
      result.current.start();
    });

    const source = getLatestSource();

    act(() => {
      source.simulateError();
    });

    expect(result.current.error).toBe("Stream connection lost");
    expect(result.current.isStreaming).toBe(false);
    expect(source.close).toHaveBeenCalled();
  });

  it("closes EventSource and sets isStreaming to false on stop", () => {
    const { result } = renderHook(() => useSSE("http://localhost/events"));

    act(() => {
      result.current.start();
    });

    const source = getLatestSource();
    expect(result.current.isStreaming).toBe(true);

    act(() => {
      result.current.stop();
    });

    expect(result.current.isStreaming).toBe(false);
    expect(source.close).toHaveBeenCalled();
  });

  it("resets data and error on subsequent start calls", () => {
    const { result } = renderHook(() => useSSE("http://localhost/events"));

    act(() => {
      result.current.start();
    });

    const firstSource = getLatestSource();

    act(() => {
      firstSource.simulateMessage("first chunk");
    });

    expect(result.current.data).toBe("first chunk");

    act(() => {
      result.current.start();
    });

    expect(result.current.data).toBe("");
    expect(result.current.error).toBeNull();
    expect(result.current.isStreaming).toBe(true);
  });
});
