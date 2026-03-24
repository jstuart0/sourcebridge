"use client";

import { Monitor, MoonStar, Sparkles, SunMedium } from "lucide-react";
import { useAppearance } from "@/components/layout/AppearanceProvider";
import type { ThemeMode, UiMode } from "@/lib/ui-mode";
import { cn } from "@/lib/utils";

const themes: Array<{ value: ThemeMode; label: string; icon: typeof SunMedium }> = [
  { value: "dark", label: "Dark", icon: MoonStar },
  { value: "light", label: "Light", icon: SunMedium },
];

const modes: Array<{ value: UiMode; label: string; icon: typeof Monitor }> = [
  { value: "editorial", label: "Editorial", icon: Monitor },
  { value: "glass", label: "Glass", icon: Sparkles },
  { value: "control", label: "Control", icon: Monitor },
];

export function ModeSwitcher() {
  const { theme, uiMode, setTheme, setUiMode } = useAppearance();

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
          Theme
        </p>
        <div className="grid grid-cols-2 gap-2">
          {themes.map((item) => {
            const Icon = item.icon;
            const active = item.value === theme;
            return (
              <button
                key={item.value}
                type="button"
                onClick={() => setTheme(item.value)}
                className={cn(
                  "flex items-center gap-2 rounded-[var(--control-radius)] border px-3 py-2 text-sm transition-colors",
                  active
                    ? "border-[var(--accent-primary)] bg-[var(--accent-quiet)] text-[var(--text-primary)]"
                    : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                )}
              >
                <Icon className="h-4 w-4" />
                {item.label}
              </button>
            );
          })}
        </div>
      </div>

      <div className="space-y-2">
        <p className="text-xs font-medium uppercase tracking-[0.16em] text-[var(--text-tertiary)]">
          Presentation
        </p>
        <div className="grid gap-2">
          {modes.map((item) => {
            const Icon = item.icon;
            const active = item.value === uiMode;
            return (
              <button
                key={item.value}
                type="button"
                onClick={() => setUiMode(item.value)}
                className={cn(
                  "flex items-center justify-between rounded-[var(--control-radius)] border px-3 py-2.5 text-left text-sm transition-colors",
                  active
                    ? "border-[var(--accent-primary)] bg-[var(--accent-quiet)] text-[var(--text-primary)]"
                    : "border-[var(--border-default)] bg-[var(--bg-base)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] hover:text-[var(--text-primary)]"
                )}
              >
                <span className="flex items-center gap-2">
                  <Icon className="h-4 w-4" />
                  {item.label}
                </span>
                {active ? <span className="text-xs text-[var(--text-tertiary)]">Active</span> : null}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}
