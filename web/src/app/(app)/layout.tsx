"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { getStoredToken } from "@/lib/auth-token-store";
import { isTokenExpired, msUntilExpiry, forceLogout } from "@/lib/auth-utils";
import { Sidebar } from "@/components/layout/Sidebar";
import { ErrorBoundary } from "@/components/layout/ErrorBoundary";
import { Notifications } from "@/components/layout/Notifications";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const [authed, setAuthed] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);

  useEffect(() => {
    const token = getStoredToken();
    if (!token || isTokenExpired(token)) {
      forceLogout();
      return;
    }
    setAuthed(true);

    // Schedule auto-logout when the token expires
    const remaining = msUntilExpiry(token);
    if (remaining <= 0) {
      forceLogout();
      return;
    }

    const timer = setTimeout(() => {
      forceLogout();
    }, remaining);

    return () => clearTimeout(timer);
  }, [router]);

  if (!authed) {
    return (
      <div className="ca-shell flex min-h-screen items-center justify-center px-6">
        <div className="text-center">
          <div className="ca-loading-spinner mx-auto mb-3 h-8 w-8 rounded-full border-2 border-[var(--border-default)] border-t-[var(--accent-primary)]" />
          <p className="text-sm text-[var(--text-secondary)]">Loading workspace…</p>
        </div>
      </div>
    );
  }


  return (
    <div className="ca-shell ca-shell-grid" data-collapsed={sidebarCollapsed}>
      <Sidebar onCollapseChange={setSidebarCollapsed} />
      <main className="min-w-0 overflow-x-hidden overflow-y-auto pt-14 md:pt-0">
        <ErrorBoundary>{children}</ErrorBoundary>
      </main>
      <Notifications />
    </div>
  );
}
