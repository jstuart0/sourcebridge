"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { ChevronLeft, ChevronRight, X } from "lucide-react";
import { getNavigation, type ProductEdition } from "@/lib/navigation";
import { Brand, BrandEnterprise } from "@/components/brand/Logo";
import { cn } from "@/lib/utils";

const edition: ProductEdition =
  process.env.NEXT_PUBLIC_EDITION === "enterprise" ? "enterprise" : "oss";
const navItems = getNavigation(edition);

export function Sidebar({
  onCollapseChange,
  mobileOpen,
  onMobileOpenChange,
}: {
  onCollapseChange?: (collapsed: boolean) => void;
  mobileOpen: boolean;
  onMobileOpenChange: (open: boolean) => void;
}) {
  const pathname = usePathname();
  const [collapsed, setCollapsed] = useState(false);

  // Close mobile menu on route change
  useEffect(() => {
    onMobileOpenChange(false);
  }, [pathname, onMobileOpenChange]);

  // Prevent body scroll when mobile menu is open
  useEffect(() => {
    if (mobileOpen) {
      document.body.style.overflow = "hidden";
    } else {
      document.body.style.overflow = "";
    }
    return () => {
      document.body.style.overflow = "";
    };
  }, [mobileOpen]);

  const navContent = (
    <>
      <div className="mb-4 flex items-center justify-between border-b border-[var(--border-subtle)] px-2 pb-4">
        {!collapsed ? (
          edition === "enterprise" ? (
            <BrandEnterprise size="sm" />
          ) : (
            <Brand size="sm" showTagline />
          )
        ) : null}

        {/* Desktop collapse toggle */}
        <button
          type="button"
          onClick={() => setCollapsed((value) => { const next = !value; onCollapseChange?.(next); return next; })}
          className="hidden rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] p-2 text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)] md:inline-flex"
          aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
        >
          {collapsed ? <ChevronRight className="h-4 w-4" /> : <ChevronLeft className="h-4 w-4" />}
        </button>

        {/* Mobile close button */}
        <button
          type="button"
          onClick={() => onMobileOpenChange(false)}
          className="inline-flex rounded-[var(--control-radius)] border border-[var(--border-subtle)] bg-[var(--bg-surface)] p-2 text-[var(--text-secondary)] transition-colors hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)] md:hidden"
          aria-label="Close menu"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      <nav className="flex-1 space-y-1.5">
        {navItems.map((item) => {
          const isActive =
            item.href === "/" ? pathname === "/" : pathname.startsWith(item.href);
          const Icon = item.icon;

          return (
            <Link
              key={item.href}
              href={item.href}
              className={cn(
                "flex items-center gap-3 rounded-[var(--control-radius)] border px-3 py-2.5 text-sm transition-colors",
                "min-h-[44px]", // touch target
                isActive
                  ? "border-[var(--nav-item-border)] bg-[var(--nav-item-bg-active)] font-medium text-[var(--text-primary)]"
                  : "border-transparent bg-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
              )}
            >
              <Icon className="h-4 w-4 shrink-0" />
              {!collapsed ? <span>{item.label}</span> : null}
            </Link>
          );
        })}
      </nav>

    </>
  );

  return (
    <>
      {/* Mobile overlay backdrop */}
      {mobileOpen && (
        <div
          className="fixed inset-0 z-40 bg-black/50 md:hidden"
          onClick={() => onMobileOpenChange(false)}
          aria-hidden="true"
        />
      )}

      {/* Mobile slide-out sidebar */}
      <aside
        className={cn(
          "fixed inset-y-0 left-0 z-50 flex w-[var(--sidebar-width)] flex-col border-r border-[var(--border-subtle)] bg-[var(--nav-bg)] px-3 py-4 shadow-[var(--panel-shadow-strong)] transition-transform duration-200 md:hidden",
          mobileOpen ? "translate-x-0" : "-translate-x-full"
        )}
      >
        {navContent}
      </aside>

      {/* Desktop sidebar */}
      <aside
        data-collapsed={collapsed}
        className={cn(
          "hidden h-screen flex-col border-r border-[var(--border-subtle)] bg-[var(--nav-bg)]/95 px-3 py-4 shadow-[var(--panel-shadow-soft)] transition-[width] duration-200 md:flex md:overflow-y-auto",
          collapsed ? "w-[var(--sidebar-collapsed-width)]" : "w-[var(--sidebar-width)]"
        )}
      >
        {navContent}
      </aside>
    </>
  );
}
