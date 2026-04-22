"use client";

import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { useCurrentUser } from "@/lib/current-user";

export default function SettingsProfilePage() {
  const user = useCurrentUser();

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Preferences"
        title="Profile"
        description="Account identity for the signed-in user."
      />

      <Panel className="space-y-4">
        <div className="space-y-1">
          <label className="text-sm font-medium text-[var(--text-primary)]">Email</label>
          <input
            type="text"
            value={user?.email || ""}
            readOnly
            placeholder="Not set"
            className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm text-[var(--text-tertiary)]"
          />
        </div>
        <div className="space-y-1">
          <label className="text-sm font-medium text-[var(--text-primary)]">Role</label>
          <input
            type="text"
            value={user?.role || "admin"}
            readOnly
            className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm capitalize text-[var(--text-tertiary)]"
          />
        </div>
        {user?.orgId ? (
          <div className="space-y-1">
            <label className="text-sm font-medium text-[var(--text-primary)]">Organization</label>
            <input
              type="text"
              value={user.orgId}
              readOnly
              className="h-11 w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-base)] px-3 text-sm font-mono text-[var(--text-tertiary)]"
            />
          </div>
        ) : null}
        <p className="text-xs text-[var(--text-tertiary)]">
          Profile fields come from the authenticated session. Editing support is planned for a future
          release.
        </p>
      </Panel>
    </div>
  );
}
