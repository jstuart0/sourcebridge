import Link from "next/link";

export interface BreadcrumbItem {
  label: string;
  href?: string;
}

export function Breadcrumb({ items }: { items: BreadcrumbItem[] }) {
  if (items.length === 0) return null;

  return (
    <nav className="flex flex-wrap items-center gap-1 text-sm text-[var(--text-secondary)]">
      {items.map((item, idx) => (
        <span key={`${item.href || item.label}-${idx}`} className="flex items-center gap-1">
          {idx > 0 && <span className="mx-1 text-[var(--text-tertiary)]">/</span>}
          {item.href && idx < items.length - 1 ? (
            <Link href={item.href} className="transition-colors hover:text-[var(--accent-primary)]">
              {item.label}
            </Link>
          ) : (
            <span className="font-medium text-[var(--text-primary)]">{item.label}</span>
          )}
        </span>
      ))}
    </nav>
  );
}
