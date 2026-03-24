import { describe, it, expect } from "vitest";
import { createClient } from "./client";
import { Client } from "urql";

describe("createClient", () => {
  it("returns a urql Client instance", () => {
    const client = createClient();
    expect(client).toBeInstanceOf(Client);
  });

  it("creates client without a token", () => {
    const client = createClient();
    expect(client).toBeDefined();
    expect(client).toBeInstanceOf(Client);
  });

  it("creates client with a token", () => {
    const client = createClient("test-token-123");
    expect(client).toBeDefined();
    expect(client).toBeInstanceOf(Client);
  });

  it("returns distinct client instances on each call", () => {
    const client1 = createClient();
    const client2 = createClient();
    expect(client1).not.toBe(client2);
  });

  it("returns distinct clients for different tokens", () => {
    const clientNoToken = createClient();
    const clientWithToken = createClient("my-token");
    expect(clientNoToken).not.toBe(clientWithToken);
  });

  it("exposes query and mutation methods", () => {
    const client = createClient();
    expect(typeof client.query).toBe("function");
    expect(typeof client.mutation).toBe("function");
  });
});
