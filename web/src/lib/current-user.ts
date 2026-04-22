"use client";

import { useEffect, useState } from "react";
import { getStoredToken, subscribeToken } from "@/lib/auth-token-store";

export type CurrentUser = {
  userId: string;
  email: string;
  role: string;
  orgId: string;
};

function decodeJWTPayload(token: string): Record<string, unknown> | null {
  try {
    const parts = token.split(".");
    if (parts.length !== 3) return null;
    const payload = atob(parts[1].replace(/-/g, "+").replace(/_/g, "/"));
    return JSON.parse(payload);
  } catch {
    return null;
  }
}

export function readCurrentUser(): CurrentUser | null {
  const token = getStoredToken();
  if (!token) return null;
  const payload = decodeJWTPayload(token);
  if (!payload) return null;
  return {
    userId: (payload.uid as string) || (payload.sub as string) || "",
    email: (payload.email as string) || "",
    role: (payload.role as string) || "admin",
    orgId: (payload.org as string) || "",
  };
}

export function useCurrentUser(): CurrentUser | null {
  const [user, setUser] = useState<CurrentUser | null>(null);

  useEffect(() => {
    setUser(readCurrentUser());
    return subscribeToken(() => setUser(readCurrentUser()));
  }, []);

  return user;
}

export function isAdminRole(role: string | undefined): boolean {
  if (!role) return true;
  return role === "admin" || role === "owner";
}
