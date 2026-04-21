"use client";

import { cn } from "@/lib/utils";

// Shape of a requirement for form purposes. Kept narrow — only the
// fields a user can author. Deliberately not importing the generated
// GraphQL types here so this component stays portable across create
// (fresh blank form) and edit (prefilled from a Requirement) callers.
export type RequirementFormValues = {
  externalId: string;
  title: string;
  description: string;
  source: string;
  priority: string;
  tags: string; // comma / whitespace separated in the UI
  acceptanceCriteria: string; // one-per-line in the UI
};

// Per-field "has the user touched this?" tracking. Drives the
// pointer-semantics contract to the backend: only touched fields are
// sent to updateRequirementFields.
export type RequirementFormTouched = {
  [K in keyof RequirementFormValues]: boolean;
};

export const emptyRequirementFormValues: RequirementFormValues = {
  externalId: "",
  title: "",
  description: "",
  source: "",
  priority: "",
  tags: "",
  acceptanceCriteria: "",
};

export const untouchedRequirementForm: RequirementFormTouched = {
  externalId: false,
  title: false,
  description: false,
  source: false,
  priority: false,
  tags: false,
  acceptanceCriteria: false,
};

type RequirementFormProps = {
  mode: "create" | "edit";
  values: RequirementFormValues;
  onChange: (next: RequirementFormValues, touched: RequirementFormTouched) => void;
  touched: RequirementFormTouched;
  disabled?: boolean;
  error?: string | null;
  className?: string;
};

const PRIORITY_OPTIONS = ["", "critical", "high", "medium", "low"];

export function RequirementForm({
  mode,
  values,
  onChange,
  touched,
  disabled,
  error,
  className,
}: RequirementFormProps) {
  const update = <K extends keyof RequirementFormValues>(key: K, value: RequirementFormValues[K]) => {
    onChange({ ...values, [key]: value }, { ...touched, [key]: true });
  };

  const titleMissing = mode === "create" && touched.title && values.title.trim() === "";

  return (
    <div className={cn("space-y-4", className)}>
      <Field label="Title" required hint={titleMissing ? "Title is required" : undefined} hintTone={titleMissing ? "error" : undefined}>
        <input
          type="text"
          value={values.title}
          disabled={disabled}
          onChange={(e) => update("title", e.target.value)}
          className={inputClass}
          placeholder="A short, action-oriented name"
          autoFocus={mode === "create"}
        />
      </Field>

      <Field
        label="External ID"
        hint={
          mode === "create"
            ? "Leave blank and the server will generate one (e.g. REQ-abc12345)"
            : "Changing this will not break existing links (links use an internal ID)"
        }
      >
        <input
          type="text"
          value={values.externalId}
          disabled={disabled}
          onChange={(e) => update("externalId", e.target.value)}
          className={inputClass}
          placeholder="PROJ-123"
        />
      </Field>

      <Field label="Description">
        <textarea
          value={values.description}
          disabled={disabled}
          onChange={(e) => update("description", e.target.value)}
          className={cn(inputClass, "min-h-[96px]")}
          rows={4}
          placeholder="What the requirement is about."
        />
      </Field>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Field label="Source" hint={mode === "create" ? 'Defaults to "manual" if blank' : undefined}>
          <input
            type="text"
            value={values.source}
            disabled={disabled}
            onChange={(e) => update("source", e.target.value)}
            className={inputClass}
            placeholder="manual, markdown, jira, linear…"
          />
        </Field>

        <Field label="Priority">
          <select
            value={values.priority}
            disabled={disabled}
            onChange={(e) => update("priority", e.target.value)}
            className={inputClass}
          >
            {PRIORITY_OPTIONS.map((p) => (
              <option key={p || "__blank"} value={p}>
                {p === "" ? "— none —" : p}
              </option>
            ))}
          </select>
        </Field>
      </div>

      <Field label="Tags" hint="Comma or whitespace separated. Server trims, dedupes, and normalizes.">
        <input
          type="text"
          value={values.tags}
          disabled={disabled}
          onChange={(e) => update("tags", e.target.value)}
          className={inputClass}
          placeholder="auth, security, backend"
        />
      </Field>

      <Field label="Acceptance criteria" hint="One per line.">
        <textarea
          value={values.acceptanceCriteria}
          disabled={disabled}
          onChange={(e) => update("acceptanceCriteria", e.target.value)}
          className={cn(inputClass, "min-h-[96px] font-mono text-sm")}
          rows={4}
          placeholder={"User can sign in\nSession persists across reload"}
        />
      </Field>

      {error ? (
        <div className="rounded-[var(--control-radius)] border border-[var(--color-error,#ef4444)] bg-[color:var(--color-error-subtle,rgba(239,68,68,0.08))] px-3 py-2 text-sm text-[var(--color-error,#ef4444)]">
          {error}
        </div>
      ) : null}
    </div>
  );
}

const inputClass =
  "w-full rounded-[var(--control-radius)] border border-[var(--border-default)] bg-[var(--bg-surface)] px-3 py-2 text-sm text-[var(--text-primary)] placeholder:text-[var(--text-tertiary)] focus:outline-none focus:ring-2 focus:ring-[var(--accent-focus)] disabled:opacity-60";

function Field({
  label,
  required,
  hint,
  hintTone,
  children,
}: {
  label: string;
  required?: boolean;
  hint?: string;
  hintTone?: "error" | "info";
  children: React.ReactNode;
}) {
  return (
    <label className="block space-y-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-xs font-medium uppercase tracking-wide text-[var(--text-secondary)]">
          {label}
          {required ? <span className="text-[var(--color-error,#ef4444)]"> *</span> : null}
        </span>
      </div>
      {children}
      {hint ? (
        <span
          className={cn(
            "block text-xs",
            hintTone === "error" ? "text-[var(--color-error,#ef4444)]" : "text-[var(--text-tertiary)]"
          )}
        >
          {hint}
        </span>
      ) : null}
    </label>
  );
}

// Split "auth, security  backend" into ["auth", "security", "backend"].
// Empties and dupes filtered. The server normalizes too — this is just
// a gentle UX.
export function splitTags(raw: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw2 of raw.split(/[\s,]+/)) {
    const t = raw2.trim();
    if (!t) continue;
    if (seen.has(t)) continue;
    seen.add(t);
    out.push(t);
  }
  return out;
}

// "one\n\ntwo\n three \n" -> ["one", "two", "three"]
export function splitLines(raw: string): string[] {
  const out: string[] = [];
  for (const line of raw.split(/\r?\n/)) {
    const t = line.trim();
    if (t) out.push(t);
  }
  return out;
}

// Prefill the form from a Requirement-shaped object (for edit mode).
export function valuesFromRequirement(r: {
  externalId?: string | null;
  title: string;
  description?: string | null;
  source?: string | null;
  priority?: string | null;
  tags?: readonly string[] | null;
  acceptanceCriteria?: readonly string[] | null;
}): RequirementFormValues {
  return {
    externalId: r.externalId ?? "",
    title: r.title,
    description: r.description ?? "",
    source: r.source ?? "",
    priority: r.priority ?? "",
    tags: (r.tags ?? []).join(", "),
    acceptanceCriteria: (r.acceptanceCriteria ?? []).join("\n"),
  };
}
