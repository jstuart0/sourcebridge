"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import {
  CircleHelp,
  LogOut,
  Menu,
  Settings as SettingsIcon,
  Shield,
  User as UserIcon,
} from "lucide-react";
import { clearStoredToken } from "@/lib/auth-token-store";
import { authFetch } from "@/lib/auth-fetch";
import { isAdminRole, useCurrentUser } from "@/lib/current-user";
import { cn } from "@/lib/utils";
import { ThemeToggle } from "@/components/ThemeToggle";

function initialsOf(email: string): string {
  if (!email) return "?";
  const local = email.split("@")[0] || email;
  const parts = local.split(/[._-]+/).filter(Boolean);
  if (parts.length >= 2) {
    return (parts[0][0] + parts[1][0]).toUpperCase();
  }
  return local.slice(0, 2).toUpperCase();
}

export function TopBar({ onMobileNavOpen }: { onMobileNavOpen?: () => void }) {
  const router = useRouter();
  const pathname = usePathname();
  const user = useCurrentUser();
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  const showAdmin = isAdminRole(user?.role);
  const adminActive = pathname.startsWith("/admin");

  useEffect(() => setMenuOpen(false), [pathname]);

  // Focus first menuitem when menu opens; restore focus to trigger when it closes.
  // menuRef and triggerRef are stable refs — intentionally omitted from deps.
  useEffect(() => {
    if (menuOpen) {
      const first = menuRef.current?.querySelector<HTMLElement>('[role="menuitem"]');
      first?.focus();
    } else if (menuRef.current) {
      triggerRef.current?.focus();
    }
  }, [menuOpen]);

  useEffect(() => {
    if (!menuOpen) return;
    function handleClick(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    }
    function handleKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        setMenuOpen(false);
        return;
      }
      if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
      const items = Array.from(
        menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []
      );
      if (items.length === 0) return;
      const focused = document.activeElement as HTMLElement;
      const idx = items.indexOf(focused);
      if (e.key === "ArrowDown") {
        e.preventDefault();
        items[(idx + 1) % items.length]?.focus();
      } else {
        e.preventDefault();
        items[(idx - 1 + items.length) % items.length]?.focus();
      }
    }
    document.addEventListener("mousedown", handleClick);
    document.addEventListener("keydown", handleKey);
    return () => {
      document.removeEventListener("mousedown", handleClick);
      document.removeEventListener("keydown", handleKey);
    };
  }, [menuOpen]);

  const handleLogout = useCallback(async () => {
    try {
      // Use authFetch so that Bearer + X-CSRF-Token are injected. When Phase 2's
      // CSRFFullCoverageEnabled flag is turned on, /auth/logout will require the
      // CSRF token. authFetch handles the transparent refresh-and-retry path.
      await authFetch("/auth/logout", { method: "POST" });
    } catch {
      // ignore — proceed with local logout regardless of server response
    }
    clearStoredToken();
    router.push("/login");
  }, [router]);

  const displayName = user?.email || "Signed in";
  const displayRole = user?.role ? user.role : "admin";

  return (
    <header className="sticky top-0 z-30 flex h-12 items-center gap-1 border-b border-[var(--border-subtle)] bg-[var(--bg-base)]/80 px-3 backdrop-blur md:h-14 md:px-4">
      {/* Mobile hamburger */}
      <button
        type="button"
        onClick={() => onMobileNavOpen?.()}
        aria-label="Open menu"
        className="inline-flex items-center justify-center rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] p-2 text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)] md:hidden"
      >
        <Menu className="h-5 w-5" />
      </button>

      <div className="ml-auto flex items-center gap-1">
      {/* CA-276 (U-L8): keyboard-shortcut hint for the command palette.
          Hidden on mobile (kbd labels are desktop-only UX). */}
      <span
        className="mr-2 hidden items-center gap-1 text-[11px] text-[var(--text-tertiary)] md:inline-flex"
        title="Press ⌘K (or Ctrl+K) to open the command palette"
        aria-label="Press Command K or Control K to open the command palette"
      >
        <kbd className="rounded border border-[var(--border-subtle)] bg-[var(--bg-surface)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--text-secondary)]">
          ⌘K
        </kbd>
      </span>
      <ThemeToggle />
      {showAdmin ? (
        <Link
          href="/admin"
          className={cn(
            "inline-flex items-center gap-2 rounded-[var(--control-radius)] border px-3 py-1.5 text-sm transition-colors",
            adminActive
              ? "border-[var(--nav-item-border)] bg-[var(--nav-item-bg-active)] font-medium text-[var(--text-primary)]"
              : "border-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
          )}
          aria-current={adminActive ? "page" : undefined}
        >
          <Shield className="h-4 w-4" />
          <span className="hidden sm:inline">Admin</span>
        </Link>
      ) : null}

      <div
        className="relative"
        ref={menuRef}
        onBlur={(e) => {
          // Close the menu when focus leaves the container entirely (click outside
          // or Shift-Tab past the first item), while keeping it open during
          // internal navigation between menu items.
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
            setMenuOpen(false);
          }
        }}
      >
        <button
          ref={triggerRef}
          type="button"
          onClick={() => setMenuOpen((open) => !open)}
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          aria-label="Account menu"
          className={cn(
            "inline-flex items-center gap-2 rounded-full border border-[var(--border-subtle)] bg-[var(--bg-surface)] py-1 pl-1 pr-2 text-sm text-[var(--text-primary)] transition-colors hover:bg-[var(--bg-hover)]",
            menuOpen && "bg-[var(--bg-hover)]"
          )}
        >
          <span
            className="flex h-7 w-7 items-center justify-center rounded-full bg-[var(--accent-primary)] text-[11px] font-semibold text-[var(--accent-contrast)]"
            aria-hidden="true"
          >
            {initialsOf(user?.email || "")}
          </span>
          <span className="hidden text-[var(--text-secondary)] sm:inline">
            {user?.email ? user.email.split("@")[0] : "Account"}
          </span>
        </button>

        {menuOpen ? (
          <div
            role="menu"
            className="absolute right-0 top-[calc(100%+6px)] w-64 overflow-hidden rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] shadow-[var(--panel-shadow-strong)]"
          >
            <div className="border-b border-[var(--border-subtle)] px-3 py-3">
              <p className="truncate text-sm font-medium text-[var(--text-primary)]">
                {displayName}
              </p>
              <p className="mt-0.5 text-xs capitalize text-[var(--text-tertiary)]">
                {displayRole}
              </p>
            </div>

            <div className="py-1">
              <MenuLink href="/settings/profile" icon={UserIcon} label="Profile" />
              <MenuLink href="/settings" icon={SettingsIcon} label="Settings" />
              <MenuLink href="/help" icon={CircleHelp} label="Help" />
            </div>

            <div className="border-t border-[var(--border-subtle)] py-1">
              <button
                type="button"
                role="menuitem"
                onClick={handleLogout}
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
              >
                <LogOut className="h-4 w-4" />
                <span>Sign out</span>
              </button>
            </div>
          </div>
        ) : null}
      </div>
      </div>
    </header>
  );
}

function MenuLink({
  href,
  icon: Icon,
  label,
}: {
  href: string;
  icon: React.ComponentType<{ className?: string }>;
  label: string;
}) {
  return (
    <Link
      href={href}
      role="menuitem"
      className="flex items-center gap-2 px-3 py-2 text-sm text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
    >
      <Icon className="h-4 w-4" />
      <span>{label}</span>
    </Link>
  );
}
