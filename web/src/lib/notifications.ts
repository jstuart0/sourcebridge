"use client";

export const JOB_ALERTS_ENABLED_KEY = "sourcebridge.jobAlerts.enabled";
const TOAST_EVENT = "sourcebridge:toast";

export interface AppToastDetail {
  message: string;
}

export function pushToast(message: string) {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent<AppToastDetail>(TOAST_EVENT, { detail: { message } }));
}

export function subscribeToToasts(listener: (detail: AppToastDetail) => void) {
  if (typeof window === "undefined") return () => {};
  const handler = (event: Event) => {
    const custom = event as CustomEvent<AppToastDetail>;
    if (custom.detail?.message) {
      listener(custom.detail);
    }
  };
  window.addEventListener(TOAST_EVENT, handler as EventListener);
  return () => window.removeEventListener(TOAST_EVENT, handler as EventListener);
}

export function jobAlertsEnabled(): boolean {
  if (typeof window === "undefined") return false;
  return window.localStorage.getItem(JOB_ALERTS_ENABLED_KEY) === "true";
}

export async function enableJobAlerts(): Promise<NotificationPermission | "unsupported"> {
  if (typeof window === "undefined" || !("Notification" in window)) {
    return "unsupported";
  }
  const permission = await window.Notification.requestPermission();
  if (permission === "granted") {
    window.localStorage.setItem(JOB_ALERTS_ENABLED_KEY, "true");
  }
  return permission;
}

export function disableJobAlerts() {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(JOB_ALERTS_ENABLED_KEY, "false");
}

export function notifyJobEvent(title: string, body: string) {
  pushToast(body);
  if (typeof window === "undefined" || !("Notification" in window)) return;
  if (!jobAlertsEnabled() || Notification.permission !== "granted") return;
  new Notification(title, { body });
}
