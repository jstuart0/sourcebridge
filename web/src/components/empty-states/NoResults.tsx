"use client";

import React from "react";
import { Search } from "lucide-react";
import { EmptyState } from "./EmptyState";

export interface NoResultsProps {
  query?: string;
  onClear?: () => void;
}

export function NoResults({ query, onClear }: NoResultsProps) {
  return (
    <EmptyState
      icon={<Search size={48} />}
      title="No results found"
      description={
        query
          ? `No results match "${query}". Try adjusting your search terms.`
          : "No results match your current filters."
      }
      action={onClear ? { label: "Clear Search", onClick: onClear } : undefined}
    />
  );
}
