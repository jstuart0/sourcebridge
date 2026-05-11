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
 *
 * Privacy: only opaque identifiers are sent — no email, name, or other PII.
 * The userId is the JWT subject (an opaque UUID). DNT is honoured both here
 * and via posthog.init's respect_dnt option.
 */
export function identifyUser(token: string) {
  if (!POSTHOG_KEY || typeof window === "undefined") return;

  // Honour Do Not Track before any identify() or capture() call.
  if (
    navigator.doNotTrack === "1" ||
    (window as Window & { doNotTrack?: string }).doNotTrack === "1"
  ) {
    return;
  }

  try {
    const parts = token.split(".");
    if (parts.length !== 3) return;
    const payload = JSON.parse(
      atob(parts[1].replace(/-/g, "+").replace(/_/g, "/")),
    );

    // userId is payload.sub / payload.user_id — opaque JWT subject UUID.
    // Never use email or name here: those are PII.
    const userId = payload.sub || payload.user_id;
    if (!userId) return;

    // Phase 6: identify with opaque user ID only — no tenant_id per plan.
    posthog.identify(userId);
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
