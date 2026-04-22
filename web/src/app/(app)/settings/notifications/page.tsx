"use client";

import { useEffect, useState } from "react";

import { PageHeader } from "@/components/ui/page-header";
import { Panel } from "@/components/ui/panel";
import {
  disableJobAlerts,
  enableJobAlerts,
  jobAlertsEnabled,
} from "@/lib/notifications";

export default function SettingsNotificationsPage() {
  const [jobAlerts, setJobAlerts] = useState(false);
  const [permissionState, setPermissionState] = useState<NotificationPermission | "unsupported">(
    "default"
  );

  useEffect(() => {
    if (typeof window === "undefined") return;
    setJobAlerts(jobAlertsEnabled());
    if ("Notification" in window) {
      setPermissionState(Notification.permission);
    } else {
      setPermissionState("unsupported");
    }
  }, []);

  async function handleToggle() {
    if (jobAlerts) {
      disableJobAlerts();
      setJobAlerts(false);
      return;
    }
    const perm = await enableJobAlerts();
    setJobAlerts(perm === "granted");
    if (typeof window !== "undefined" && "Notification" in window) {
      setPermissionState(Notification.permission);
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Preferences"
        title="Notifications"
        description="Control which events surface as browser notifications."
      />

      <Panel className="space-y-4">
        <div className="flex items-center justify-between gap-4">
          <div className="space-y-1">
            <h3 className="text-base font-semibold text-[var(--text-primary)]">Job alerts</h3>
            <p className="text-sm text-[var(--text-secondary)]">
              Get a desktop notification when a long-running AI job completes or fails.
            </p>
          </div>
          <label className="relative inline-flex cursor-pointer items-center">
            <input
              type="checkbox"
              checked={jobAlerts}
              onChange={handleToggle}
              className="peer sr-only"
              disabled={permissionState === "unsupported"}
            />
            <div className="peer h-5 w-9 rounded-full bg-[var(--border-default)] after:absolute after:left-[2px] after:top-[2px] after:h-4 after:w-4 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-[hsl(var(--accent-hue,250),60%,60%)] peer-checked:after:translate-x-full peer-checked:after:border-white" />
          </label>
        </div>
        <p className="text-xs text-[var(--text-tertiary)]">
          {permissionState === "unsupported"
            ? "This browser does not support desktop notifications."
            : permissionState === "denied"
            ? "Notifications are blocked for this site. Enable them in your browser settings to use job alerts."
            : permissionState === "granted"
            ? "Permission granted. Alerts will appear when jobs finish while the tab is in the background."
            : "You'll be asked for permission the first time you enable alerts."}
        </p>
      </Panel>
    </div>
  );
}
