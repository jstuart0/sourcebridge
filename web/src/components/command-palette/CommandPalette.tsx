"use client";

import React, { useEffect, useState } from "react";
import { Command } from "cmdk";
import { cn } from "@/lib/utils";

export interface CommandItem {
  id: string;
  label: string;
  group?: string;
  icon?: React.ReactNode;
  shortcut?: string;
  onSelect: () => void;
}

export interface CommandPaletteProps {
  items: CommandItem[];
  placeholder?: string;
}

export function CommandPalette({ items, placeholder = "Search commands..." }: CommandPaletteProps) {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen((prev) => !prev);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);

  const groups = new Map<string, CommandItem[]>();
  for (const item of items) {
    const group = item.group || "Actions";
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group)!.push(item);
  }

  if (!open) return null;

  return (
    <div
      data-testid="command-palette-overlay"
      onClick={() => setOpen(false)}
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 px-4 pt-[20vh]"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-[32.5rem] overflow-hidden rounded-[var(--radius-lg,8px)] border border-[var(--border-default,#334155)] bg-[var(--bg-elevated,#1e293b)] shadow-2xl"
      >
        <Command data-testid="command-palette" label="Command palette">
          <Command.Input
            data-testid="command-palette-input"
            placeholder={placeholder}
            className="w-full border-0 border-b border-[var(--border-default,#334155)] bg-transparent px-4 py-3 text-sm text-[var(--text-primary,#e2e8f0)] outline-none"
          />
          <Command.List className="max-h-[18.75rem] overflow-y-auto p-2">
            <Command.Empty className="px-4 py-8 text-center text-sm text-[var(--text-secondary,#94a3b8)]">
              No results found.
            </Command.Empty>
            {[...groups.entries()].map(([group, groupItems]) => (
              <Command.Group
                key={group}
                heading={group}
                className="mb-2"
              >
                {groupItems.map((item) => (
                  <Command.Item
                    key={item.id}
                    value={item.label}
                    onSelect={() => {
                      item.onSelect();
                      setOpen(false);
                    }}
                    data-testid={`command-item-${item.id}`}
                    className={cn(
                      "flex cursor-pointer items-center gap-2 rounded-[var(--radius-md,6px)] px-3 py-2 text-sm text-[var(--text-primary,#e2e8f0)]",
                      "data-[selected=true]:bg-[var(--bg-hover)]"
                    )}
                  >
                    {item.icon && <span className="shrink-0">{item.icon}</span>}
                    <span className="flex-1">{item.label}</span>
                    {item.shortcut && (
                      <kbd className="rounded-[var(--radius-sm,4px)] border border-[var(--border-default,#334155)] px-1.5 py-0.5 text-xs text-[var(--text-secondary,#94a3b8)]">
                        {item.shortcut}
                      </kbd>
                    )}
                  </Command.Item>
                ))}
              </Command.Group>
            ))}
          </Command.List>
        </Command>
      </div>
    </div>
  );
}
