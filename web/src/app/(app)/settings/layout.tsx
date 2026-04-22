"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { BellRing, KeyRound, Palette, ShieldCheck, UserCircle2 } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";

type SettingsNavItem = {
  href: string;
  label: string;
  icon: LucideIcon;
};

const SETTINGS_NAV: SettingsNavItem[] = [
  { href: "/settings/profile", label: "Profile", icon: UserCircle2 },
  { href: "/settings/appearance", label: "Appearance", icon: Palette },
  { href: "/settings/notifications", label: "Notifications", icon: BellRing },
  { href: "/settings/tokens", label: "API Tokens", icon: KeyRound },
  { href: "/settings/security", label: "Security", icon: ShieldCheck },
];

export default function SettingsLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();

  return (
    <div className="mx-auto flex w-full max-w-[var(--content-max-width)] flex-col gap-6 px-3 py-4 sm:px-4 sm:py-6 md:flex-row md:gap-8 md:px-8 md:py-8">
      <aside className="md:w-56 md:shrink-0">
        <div className="mb-4 space-y-1">
          <p className="text-[11px] font-semibold uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
            Preferences
          </p>
          <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
            Settings
          </h2>
        </div>
        <nav className="-mx-1 flex gap-1 overflow-x-auto md:flex-col md:overflow-visible">
          {SETTINGS_NAV.map((item) => {
            const active =
              pathname === item.href ||
              (item.href !== "/settings" && pathname.startsWith(item.href));
            const Icon = item.icon;
            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "flex shrink-0 items-center gap-2 rounded-[var(--control-radius)] border px-3 py-2 text-sm transition-colors md:w-full",
                  active
                    ? "border-[var(--nav-item-border)] bg-[var(--nav-item-bg-active)] font-medium text-[var(--text-primary)]"
                    : "border-transparent text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                )}
              >
                <Icon className="h-4 w-4 shrink-0" />
                <span className="whitespace-nowrap">{item.label}</span>
              </Link>
            );
          })}
        </nav>
      </aside>

      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}
