"use client";

import { TOKEN_KEY } from "@/lib/token-key";

let tokenListeners: Array<() => void> = [];

export function subscribeToken(cb: () => void) {
  tokenListeners.push(cb);
  return () => {
    tokenListeners = tokenListeners.filter((listener) => listener !== cb);
  };
}

export function notifyTokenChanged() {
  tokenListeners.forEach((listener) => listener());
}

export function getStoredToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem(TOKEN_KEY);
}

export function setStoredToken(token: string) {
  if (typeof window === "undefined") return;
  localStorage.setItem(TOKEN_KEY, token);
  notifyTokenChanged();
}

export function clearStoredToken() {
  if (typeof window === "undefined") return;
  localStorage.removeItem(TOKEN_KEY);
  notifyTokenChanged();
}
