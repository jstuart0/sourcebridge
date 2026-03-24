"use client";

import dynamic from "next/dynamic";
import { PageFrame } from "@/components/ui/page-frame";
import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";

const isEnterprise = process.env.NEXT_PUBLIC_EDITION === "enterprise";

const AdminShell = dynamic(() => import("@/components/enterprise/AdminShell"), {
  ssr: false,
  loading: () => (
    <PageFrame>
      <Panel>
        <p className="text-sm text-[var(--text-secondary)]">Loading enterprise admin…</p>
      </Panel>
    </PageFrame>
  ),
});

export default function EnterpriseAdminPage() {
  if (!isEnterprise) {
    return (
      <PageFrame>
        <PageHeader
          eyebrow="Enterprise"
          title="Enterprise admin"
          description="Enterprise-only governance, billing, identity, and organization controls."
        />
        <Panel>
          <p className="text-sm leading-7 text-[var(--text-secondary)]">
            Enterprise features are not available in the open-source edition. Build with{" "}
            <code>NEXT_PUBLIC_EDITION=enterprise</code> to enable them.
          </p>
        </Panel>
      </PageFrame>
    );
  }

  return <AdminShell />;
}
