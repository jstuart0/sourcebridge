"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";

import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import { authFetch } from "@/lib/auth-fetch";

interface AdminConfig {
  security: { csrf_enabled: boolean; mode: string; oidc_configured: boolean };
}

export default function AdminAuthPage() {
  const [config, setConfig] = useState<AdminConfig | null>(null);

  const load = useCallback(async () => {
    const res = await authFetch("/api/v1/admin/config");
    if (res.ok) setConfig(await res.json());
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <PageFrame>
      <PageHeader
        eyebrow="Admin"
        title="Authentication"
        description="View how authentication is configured on this instance."
      />

      {config && (
        <Panel className="mb-4">
          <h3 className="mb-3 text-base font-semibold text-[var(--text-primary)]">Authentication</h3>
          <div className="grid gap-2 text-sm text-[var(--text-primary)]">
            <div>
              <span className="text-[var(--text-secondary)]">Mode: </span>
              <span className="font-medium">{config.security.mode}</span>
            </div>
            <div>
              <span className="text-[var(--text-secondary)]">CSRF Enabled: </span>
              <span>{config.security.csrf_enabled ? "Yes" : "No"}</span>
            </div>
            <div>
              <span className="text-[var(--text-secondary)]">OIDC Configured: </span>
              <span>{config.security.oidc_configured ? "Yes" : "No"}</span>
            </div>
          </div>
        </Panel>
      )}

      <Panel>
        <h3 className="mb-3 text-base font-semibold text-[var(--text-primary)]">Change Password</h3>
        <p className="text-sm text-[var(--text-secondary)]">
          Use the{" "}
          <Link href="/settings/security" className="text-[var(--accent-primary)]">
            Security
          </Link>{" "}
          settings to change your password.
        </p>
      </Panel>
    </PageFrame>
  );
}
