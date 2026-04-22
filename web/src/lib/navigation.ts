import type { LucideIcon } from "lucide-react";
import {
  Building2,
  FolderKanban,
  Home,
  Search,
  TextSearch,
} from "lucide-react";

export type ProductEdition = "oss" | "enterprise";

export type NavigationItem = {
  label: string;
  href: string;
  icon: LucideIcon;
  commandLabel?: string;
  enterpriseOnly?: boolean;
};

const BASE_NAVIGATION: NavigationItem[] = [
  { label: "Overview", href: "/", icon: Home, commandLabel: "Go to Overview" },
  {
    label: "Repositories",
    href: "/repositories",
    icon: FolderKanban,
    commandLabel: "Go to Repositories",
  },
  {
    label: "Requirements",
    href: "/requirements",
    icon: TextSearch,
    commandLabel: "Go to Requirements",
  },
  { label: "Search", href: "/search", icon: Search, commandLabel: "Go to Search" },
];

const ENTERPRISE_NAVIGATION: NavigationItem[] = [
  ...BASE_NAVIGATION,
  {
    label: "Enterprise",
    href: "/admin/enterprise",
    icon: Building2,
    commandLabel: "Go to Enterprise",
    enterpriseOnly: true,
  },
];

export function getNavigation(edition: ProductEdition): NavigationItem[] {
  return edition === "enterprise" ? ENTERPRISE_NAVIGATION : BASE_NAVIGATION;
}

// Items that live in the top-right/avatar menu instead of the sidebar,
// but should still be reachable from the command palette.
const COMMAND_ONLY_ITEMS: { label: string; href: string }[] = [
  { label: "Go to Admin", href: "/admin" },
  { label: "Go to Settings", href: "/settings" },
  { label: "Go to Help", href: "/help" },
];

export function getCommandNavigationItems(edition: ProductEdition) {
  const fromSidebar = getNavigation(edition)
    .filter((item) => item.commandLabel)
    .map((item) => ({
      id: `nav-${item.href.replaceAll("/", "-") || "root"}`,
      label: item.commandLabel as string,
      href: item.href,
      group: "Navigation",
    }));

  const fromTopBar = COMMAND_ONLY_ITEMS.map((item) => ({
    id: `nav-${item.href.replaceAll("/", "-") || "root"}`,
    label: item.label,
    href: item.href,
    group: "Navigation",
  }));

  return [...fromSidebar, ...fromTopBar];
}
