"use client";

import { ModeSwitcher } from "@/components/ui/mode-switcher";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";

export default function SettingsPage() {
  return (
    <PageFrame>
      <PageHeader
        eyebrow="Preferences"
        title="Settings"
        description="Adjust presentation defaults and inspect the environment exposed to the web application."
      />

      <div className="grid gap-6 xl:grid-cols-[1fr_0.9fr]">
        <Panel className="space-y-6">
          <div className="space-y-1">
            <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
              Appearance
            </h2>
            <p className="text-sm leading-7 text-[var(--text-secondary)]">
              Editorial is the default workspace mode. Glass and control are available as alternate
              presentation layers.
            </p>
          </div>

          <ModeSwitcher />
        </Panel>

        <Panel variant="elevated" className="space-y-6">
          <div className="space-y-1">
            <h2 className="text-xl font-semibold tracking-[-0.03em] text-[var(--text-primary)]">
              API Configuration
            </h2>
            <p className="text-sm leading-7 text-[var(--text-secondary)]">
              The web application currently reads its endpoint from build-time environment variables.
            </p>
          </div>

          <div className="space-y-2">
            <label className="block text-sm font-medium text-[var(--text-primary)]">
              API Endpoint
            </label>
            <input
              type="text"
              value={process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080"}
              readOnly
              className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-tertiary)]"
            />
            <p className="text-xs text-[var(--text-tertiary)]">
              Configure via the <code>NEXT_PUBLIC_API_URL</code> environment variable.
            </p>
          </div>
        </Panel>
      </div>
    </PageFrame>
  );
}
