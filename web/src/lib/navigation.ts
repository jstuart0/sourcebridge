import type { LucideIcon } from "lucide-react";
import {
  Building2,
  CircleHelp,
  FolderKanban,
  Home,
  Search,
  Settings,
  Shield,
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
  { label: "Admin", href: "/admin", icon: Shield, commandLabel: "Go to Admin" },
  { label: "Settings", href: "/settings", icon: Settings, commandLabel: "Go to Settings" },
  { label: "Help", href: "/help", icon: CircleHelp, commandLabel: "Go to Help" },
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

export function getCommandNavigationItems(edition: ProductEdition) {
  return getNavigation(edition)
    .filter((item) => item.commandLabel)
    .map((item) => ({
      id: `nav-${item.href.replaceAll("/", "-") || "root"}`,
      label: item.commandLabel as string,
      href: item.href,
      group: "Navigation",
    }));
}
