import posthog from "posthog-js";

const POSTHOG_KEY = process.env.NEXT_PUBLIC_POSTHOG_KEY;
const POSTHOG_HOST = process.env.NEXT_PUBLIC_POSTHOG_HOST || "https://us.i.posthog.com";

let initialized = false;

/**
 * Initialize the PostHog client. Safe to call multiple times — only
 * initializes once, and silently no-ops if no API key is configured.
 */
export function initPostHog() {
  if (initialized || !POSTHOG_KEY || typeof window === "undefined") return;

  posthog.init(POSTHOG_KEY, {
    api_host: POSTHOG_HOST,
    autocapture: true,
    capture_pageview: true,
    capture_pageleave: true,
    persistence: "localStorage",
    // Respect Do Not Track browser setting
    respect_dnt: true,
  });

  initialized = true;
}

/**
 * Identify the current user to PostHog. Call after login.
 * Extracts user info from the JWT token payload.
 */
export function identifyUser(token: string) {
  if (!POSTHOG_KEY || typeof window === "undefined") return;

  try {
    const parts = token.split(".");
    if (parts.length !== 3) return;
    const payload = JSON.parse(atob(parts[1].replace(/-/g, "+").replace(/_/g, "/")));

    const userId = payload.sub || payload.user_id;
    if (!userId) return;

    posthog.identify(userId, {
      email: payload.email,
      name: payload.name,
      tenant_id: payload.tenant_id,
    });
  } catch {
    // Silently ignore malformed tokens
  }
}

/**
 * Reset PostHog identity. Call on logout.
 */
export function resetPostHog() {
  if (!POSTHOG_KEY || typeof window === "undefined") return;
  posthog.reset();
}

export { posthog };
