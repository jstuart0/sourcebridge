"use client";

import { useEffect, useMemo, useSyncExternalStore } from "react";
import { Provider as UrqlProvider } from "urql";
import { AppearanceProvider } from "@/components/layout/AppearanceProvider";
import { CommandPalette, CommandItem } from "@/components/command-palette";
import { createClient } from "@/lib/graphql/client";
import { getCommandNavigationItems, type ProductEdition } from "@/lib/navigation";
import { getStoredToken, subscribeToken } from "@/lib/auth-token-store";
import { initPostHog, identifyUser, resetPostHog } from "@/lib/posthog";
import { useRouter } from "next/navigation";

function getTokenSnapshot(): string | null {
  return getStoredToken();
}
function getTokenServerSnapshot(): string | null {
  return null;
}

function CommandPaletteWithRouter() {
  const router = useRouter();
  const edition: ProductEdition =
    process.env.NEXT_PUBLIC_EDITION === "enterprise" ? "enterprise" : "oss";

  const items: CommandItem[] = getCommandNavigationItems(edition).map((cmd) => ({
    ...cmd,
    onSelect: () => router.push(cmd.href),
  }));

  return <CommandPalette items={items} />;
}

export function Providers({ children }: { children: React.ReactNode }) {
  const token = useSyncExternalStore(subscribeToken, getTokenSnapshot, getTokenServerSnapshot);

  // Initialize PostHog once on mount
  useEffect(() => {
    initPostHog();
  }, []);

  // Identify or reset user when auth state changes
  useEffect(() => {
    if (token) {
      identifyUser(token);
    } else {
      resetPostHog();
    }
  }, [token]);

  const client = useMemo(() => {
    return createClient(token ?? undefined);
  }, [token]);

  return (
    <UrqlProvider value={client}>
      <AppearanceProvider>
        {children}
        <CommandPaletteWithRouter />
      </AppearanceProvider>
    </UrqlProvider>
  );
}
