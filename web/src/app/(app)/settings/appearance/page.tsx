"use client";

import { ModeSwitcher } from "@/components/ui/mode-switcher";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";

export default function SettingsAppearancePage() {
  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Preferences"
        title="Appearance"
        description="Choose a workspace presentation mode."
      />

      <Panel className="space-y-6">
        <div className="space-y-1">
          <h2 className="text-lg font-semibold tracking-[-0.02em] text-[var(--text-primary)]">
            Presentation mode
          </h2>
          <p className="text-sm leading-7 text-[var(--text-secondary)]">
            Editorial is the default workspace mode. Glass and control are available as alternate
            presentation layers.
          </p>
        </div>
        <ModeSwitcher />
      </Panel>

      <Panel className="space-y-3">
        <div className="space-y-1">
          <h2 className="text-lg font-semibold tracking-[-0.02em] text-[var(--text-primary)]">
            API endpoint
          </h2>
          <p className="text-sm leading-7 text-[var(--text-secondary)]">
            The web application reads its endpoint from build-time environment variables.
          </p>
        </div>
        <input
          type="text"
          value={process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080"}
          readOnly
          className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-tertiary)]"
        />
        <p className="text-xs text-[var(--text-tertiary)]">
          Configure via the <code>NEXT_PUBLIC_API_URL</code> environment variable.
        </p>
      </Panel>
    </div>
  );
}
