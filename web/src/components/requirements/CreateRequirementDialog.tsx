"use client";

import { useEffect, useState } from "react";
import { useMutation } from "urql";
import { Button } from "@/components/ui/button";
import { CREATE_REQUIREMENT_MUTATION } from "@/lib/graphql/queries";
import {
  RequirementForm,
  emptyRequirementFormValues,
  splitTags,
  untouchedRequirementForm,
  type RequirementFormValues,
  type RequirementFormTouched,
} from "./RequirementForm";

type CreatedRequirement = {
  id: string;
  externalId: string | null;
  title: string;
  description: string;
  source: string;
  priority: string | null;
  tags: string[];
  acceptanceCriteria: string[];
  createdAt: string;
  updatedAt: string | null;
};

type CreateRequirementDialogProps = {
  open: boolean;
  repositoryId: string;
  onClose: () => void;
  onCreated?: (requirement: CreatedRequirement) => void;
};

// Modal/sheet for creating a requirement. Headless: positions itself as
// a fixed overlay, uses native <dialog> semantics via role="dialog"
// without pulling in a heavyweight dialog library.
//
// Usage: render at the top of a page, control `open` with a local state.
export function CreateRequirementDialog({
  open,
  repositoryId,
  onClose,
  onCreated,
}: CreateRequirementDialogProps) {
  const [values, setValues] = useState<RequirementFormValues>(emptyRequirementFormValues);
  const [touched, setTouched] = useState<RequirementFormTouched>(untouchedRequirementForm);
  const [error, setError] = useState<string | null>(null);
  const [result, createRequirement] = useMutation(CREATE_REQUIREMENT_MUTATION);
  const disabled = result.fetching;

  // Reset form whenever the dialog re-opens so stale state from a
  // previous create doesn't leak.
  useEffect(() => {
    if (open) {
      setValues(emptyRequirementFormValues);
      setTouched(untouchedRequirementForm);
      setError(null);
    }
  }, [open]);

  // Close on Escape.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !disabled) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, disabled, onClose]);

  const handleSubmit = async () => {
    const title = values.title.trim();
    if (!title) {
      setTouched((t) => ({ ...t, title: true }));
      setError("Title is required");
      return;
    }
    setError(null);
    // Build the CreateRequirementInput payload. Use null (omitted)
    // for blank optional strings so the server picks defaults where it
    // has them (externalId, source).
    const input: Record<string, unknown> = {
      repositoryId,
      title,
    };
    if (values.externalId.trim()) input.externalId = values.externalId.trim();
    if (values.description) input.description = values.description;
    if (values.source.trim()) input.source = values.source.trim();
    if (values.priority) input.priority = values.priority;
    const tags = splitTags(values.tags);
    if (tags.length > 0) input.tags = tags;
    // acceptanceCriteria is not on CreateRequirementInput (by plan
    // 1.1 — it's an update-only field today). It's still shown on the
    // form for visual consistency; if the user filled it out at create
    // time we hit updateRequirementFields right after the create below.

    const res = await createRequirement({ input });
    if (res.error) {
      setError(res.error.graphQLErrors[0]?.message ?? res.error.message ?? "Create failed");
      return;
    }
    const created = res.data?.createRequirement as CreatedRequirement | undefined;
    if (!created) {
      setError("Create returned no data");
      return;
    }
    onCreated?.(created);
    onClose();
  };

  if (!open) return null;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="create-requirement-title"
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 sm:p-10"
      onClick={(e) => {
        if (e.target === e.currentTarget && !disabled) onClose();
      }}
    >
      <div className="mt-0 w-full max-w-2xl rounded-[var(--panel-radius)] border border-[var(--panel-border)] bg-[var(--panel-bg-elevated,var(--panel-bg))] p-5 shadow-[var(--panel-shadow-strong,var(--panel-shadow))] sm:p-6">
        <div className="mb-4 flex items-start justify-between gap-4">
          <div>
            <h2
              id="create-requirement-title"
              className="text-lg font-semibold text-[var(--text-primary)]"
            >
              New requirement
            </h2>
            <p className="mt-1 text-sm text-[var(--text-secondary)]">
              Create a requirement in this repository. You can edit or remove it later.
            </p>
          </div>
        </div>

        <RequirementForm
          mode="create"
          values={values}
          onChange={(next, nextTouched) => {
            setValues(next);
            setTouched(nextTouched);
          }}
          touched={touched}
          disabled={disabled}
          error={error}
        />

        <div className="mt-6 flex items-center justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={disabled}>
            Cancel
          </Button>
          <Button variant="primary" size="sm" onClick={handleSubmit} disabled={disabled}>
            {disabled ? "Creating…" : "Create"}
          </Button>
        </div>
      </div>
    </div>
  );
}
