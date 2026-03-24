const kindColors: Record<string, string> = {
  function: "border-blue-500/40 text-blue-500 bg-blue-500/10",
  method: "border-violet-500/40 text-violet-500 bg-violet-500/10",
  class: "border-amber-500/40 text-amber-500 bg-amber-500/10",
  struct: "border-amber-500/40 text-amber-500 bg-amber-500/10",
  interface: "border-emerald-500/40 text-emerald-500 bg-emerald-500/10",
  enum: "border-orange-500/40 text-orange-500 bg-orange-500/10",
  constant: "border-cyan-500/40 text-cyan-500 bg-cyan-500/10",
  variable: "border-slate-400/40 text-slate-400 bg-slate-400/10",
  module: "border-teal-500/40 text-teal-500 bg-teal-500/10",
  package: "border-teal-500/40 text-teal-500 bg-teal-500/10",
  type: "border-pink-500/40 text-pink-500 bg-pink-500/10",
  test: "border-lime-500/40 text-lime-500 bg-lime-500/10",
};

export function kindBadgeClass(kind: string): string {
  const color = kindColors[kind.toLowerCase()] || "border-slate-400/40 text-slate-400 bg-slate-400/10";
  return `inline-flex shrink-0 items-center justify-center rounded border px-1 py-0 text-[10px] font-semibold uppercase leading-4 tabular-nums ${color}`;
}

export function kindLabel(kind: string): string {
  const labels: Record<string, string> = {
    function: "fn",
    method: "md",
    class: "cls",
    struct: "st",
    interface: "if",
    enum: "en",
    constant: "co",
    variable: "var",
    module: "mod",
    package: "pkg",
    type: "ty",
    test: "test",
  };
  return labels[kind.toLowerCase()] || kind.slice(0, 3).toLowerCase();
}

export const SYMBOL_KINDS = [
  { value: "FUNCTION", label: "Functions" },
  { value: "METHOD", label: "Methods" },
  { value: "CLASS", label: "Classes" },
  { value: "STRUCT", label: "Structs" },
  { value: "INTERFACE", label: "Interfaces" },
  { value: "ENUM", label: "Enums" },
  { value: "TEST", label: "Tests" },
  { value: "TYPE", label: "Types" },
  { value: "MODULE", label: "Modules" },
] as const;
