"use client";

import { useId } from "react";

export type ModelOption = {
  id: string;
  name?: string;
  context_window?: number;
  max_output?: number;
  price_tier?: string;
};

type Props = {
  value: string;
  onChange: (v: string) => void;
  models: ModelOption[];
  placeholder?: string;
  className?: string;
  disabled?: boolean;
};

function formatCtx(n?: number): string {
  if (!n) return "";
  if (n >= 1000) return ` [${Math.round(n / 1000)}K ctx]`;
  return ` [${n} ctx]`;
}

/**
 * ModelCombobox: a single input that accepts either a known model id
 * (surfaced via a native datalist) or a free-typed custom id.
 *
 * Replaces the older dropdown+textbox pair where the `__custom__` option
 * was a dead end — users couldn't enter a custom model id that wasn't
 * already in the fetched list.
 */
export function ModelCombobox({
  value,
  onChange,
  models,
  placeholder,
  className,
  disabled,
}: Props) {
  const listId = useId();
  const hasOptions = models.length > 0;

  return (
    <>
      <input
        type="text"
        list={hasOptions ? listId : undefined}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        disabled={disabled}
        autoComplete="off"
        spellCheck={false}
        className={className}
      />
      {hasOptions ? (
        <datalist id={listId}>
          {models.map((m) => {
            const label = m.name ? `${m.name} (${m.id})` : m.id;
            return (
              <option key={m.id} value={m.id}>
                {label}
                {formatCtx(m.context_window)}
              </option>
            );
          })}
        </datalist>
      ) : null}
    </>
  );
}
