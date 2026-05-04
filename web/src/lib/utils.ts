import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// Returns the current server origin (protocol + host) when running in the
// browser, or a placeholder when evaluated server-side (SSR / build time).
export function getServerOrigin(): string {
  return typeof window !== "undefined"
    ? `${window.location.protocol}//${window.location.host}`
    : "<your-server-url>";
}
