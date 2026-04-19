"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { clearStoredToken, getStoredToken, setStoredToken } from "@/lib/auth-token-store";
import { isTokenExpired } from "@/lib/auth-utils";
import { Brand } from "@/components/brand/Logo";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";

export default function LoginPage() {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [needsSetup, setNeedsSetup] = useState<boolean | null>(null);
  const [checkingAuth, setCheckingAuth] = useState(true);

  useEffect(() => {
    const token = getStoredToken();
    if (token) {
      if (isTokenExpired(token)) {
        // Token exists but is expired — clear it and continue to login
        clearStoredToken();
      } else {
        router.replace("/repositories");
        return;
      }
    }

    let cancelled = false;

    async function loadAuthInfo() {
      try {
        const res = await fetch("/auth/info", { cache: "no-store" });
        if (!res.ok) throw new Error("Could not determine authentication state");

        const data = await res.json();
        if (!cancelled) {
          setNeedsSetup(data?.setup_done === false);
        }
      } catch {
        if (!cancelled) {
          setNeedsSetup(false);
        }
      } finally {
        if (!cancelled) {
          setCheckingAuth(false);
        }
      }
    }

    loadAuthInfo();
    return () => {
      cancelled = true;
    };
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);

    if (needsSetup && password !== confirmPassword) {
      setError("Passwords do not match");
      return;
    }

    if (needsSetup && password.length < 8) {
      setError("Password must be at least 8 characters");
      return;
    }

    setLoading(true);

    try {
      const endpoint = needsSetup ? "/auth/setup" : "/auth/login";
      const res = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password }),
      });

      if (!res.ok) {
        const data = await res.json().catch(() => null);
        const errMsg = data?.error || "Authentication failed";

        if (errMsg.toLowerCase().includes("not set up")) {
          setNeedsSetup(true);
          setError("Server needs initial setup. Enter a password to create your account.");
          return;
        }

        throw new Error(errMsg);
      }

      const data = await res.json();
      if (data.token) {
        setStoredToken(data.token);
      }

      router.push("/repositories");
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : "Failed to connect. Make sure the SourceBridge.ai API server is running."
      );
    } finally {
      setLoading(false);
    }
  }

  if (checkingAuth) {
    return (
      <main className="flex min-h-screen items-center justify-center px-6">
        <p className="text-sm text-[var(--text-secondary)]">Checking server status…</p>
      </main>
    );
  }

  return (
    <main className="grid min-h-screen px-6 py-10 lg:grid-cols-[1.05fr_0.95fr] lg:gap-10 lg:px-10">
      <section className="hidden flex-col justify-between rounded-[var(--panel-radius)] border border-[var(--panel-border)] bg-[var(--panel-bg-glass)] p-8 shadow-[var(--panel-shadow-soft)] backdrop-blur-[var(--panel-blur)] lg:flex">
        <div className="space-y-4">
          <Brand size="xl" />
          <h1 className="max-w-xl text-5xl font-semibold tracking-[-0.05em] text-[var(--text-primary)]">
            Understand any codebase, fast.
          </h1>
          <p className="max-w-lg text-base leading-8 text-[var(--text-secondary)]">
            Build a field guide for unfamiliar codebases, explain what matters, review risky changes,
            and connect specs to implementation when you have them.
          </p>
        </div>

        <div className="grid gap-4 md:grid-cols-3">
          {[
            { label: "Field Guide", value: "Repo → file → symbol understanding" },
            { label: "Change Impact", value: "See what recent commits affect" },
            { label: "Specs", value: "Optional links from intent to code" },
          ].map((item) => (
            <div
              key={item.label}
              className="rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-surface)]/70 p-4"
            >
              <p className="text-[11px] font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
                {item.label}
              </p>
              <p className="mt-2 text-sm leading-6 text-[var(--text-secondary)]">{item.value}</p>
            </div>
          ))}
        </div>
      </section>

      <section className="flex items-center justify-center">
        <Panel variant="elevated" padding="lg" className="w-full max-w-[28rem]">
          <div className="mb-8 text-center">
            <p className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--text-tertiary)]">
              {needsSetup ? "Initial Setup" : "Workspace Sign In"}
            </p>
            <h2 className="mt-3 text-3xl font-semibold tracking-[-0.04em] text-[var(--text-primary)]">
              {needsSetup ? "Create the initial admin password" : "Connect to your workspace"}
            </h2>
            <p className="mt-3 text-sm leading-7 text-[var(--text-secondary)]">
              {needsSetup
                ? "This instance has not been configured yet. Create the first local admin account to continue."
                : "Sign in with the local admin password configured for this SourceBridge.ai instance."}
            </p>
          </div>

          <form onSubmit={handleSubmit} className="space-y-5">
            <div className="space-y-2">
              <label className="block text-sm font-medium text-[var(--text-primary)]">
                {needsSetup ? "Create Password" : "Password"}
              </label>
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={needsSetup ? "Choose a strong password…" : "Enter your password…"}
                required
                autoFocus
                className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)] outline-none transition-colors placeholder:text-[var(--text-tertiary)] focus:border-[var(--accent-primary)]"
              />
            </div>

            {needsSetup ? (
              <div className="space-y-2">
                <label className="block text-sm font-medium text-[var(--text-primary)]">
                  Confirm Password
                </label>
                <input
                  type="password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                  placeholder="Confirm your password…"
                  required
                  className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-primary)] outline-none transition-colors placeholder:text-[var(--text-tertiary)] focus:border-[var(--accent-primary)]"
                />
              </div>
            ) : null}

            {error ? (
              <div className="rounded-[var(--control-radius)] border border-[var(--danger-border)] bg-[var(--danger-bg)] px-3 py-2.5 text-sm text-[var(--danger-text)]">
                {error}
              </div>
            ) : null}

            <Button type="submit" disabled={loading} className="w-full">
              {loading ? "Connecting…" : needsSetup ? "Create Account" : "Sign In"}
            </Button>
          </form>
        </Panel>
      </section>
    </main>
  );
}
