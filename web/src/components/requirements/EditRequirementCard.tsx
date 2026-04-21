"use client";

import { useEffect, useState } from "react";
import { useMutation } from "urql";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { UPDATE_REQUIREMENT_FIELDS_MUTATION } from "@/lib/graphql/queries";
import {
  RequirementForm,
  splitLines,
  splitTags,
  untouchedRequirementForm,
  valuesFromRequirement,
  type RequirementFormTouched,
  type RequirementFormValues,
} from "./RequirementForm";

type EditableRequirement = {
  id: string;
  externalId: string | null;
  title: string;
  description: string;
  source: string;
  priority: string | null;
  tags: readonly string[];
  acceptanceCriteria?: readonly string[] | null;
};

type UpdatedRequirement = EditableRequirement & {
  createdAt: string;
  updatedAt: string | null;
};

type EditRequirementCardProps = {
  requirement: EditableRequirement;
  onSaved?: (requirement: UpdatedRequirement) => void;
  onCancel?: () => void;
};

// Renders an editable form for a requirement in-place. The parent
// chooses when to render this (typically a toggle from "view" mode on
// the detail page). Uses pointer-semantics to the server: only
// user-touched fields are sent, so an untouched field keeps its current
// value even if the local state looks blank.
export function EditRequirementCard({
  requirement,
  onSaved,
  onCancel,
}: EditRequirementCardProps) {
  const [values, setValues] = useState<RequirementFormValues>(() =>
    valuesFromRequirement(requirement)
  );
  const [touched, setTouched] = useState<RequirementFormTouched>(untouchedRequirementForm);
  const [error, setError] = useState<string | null>(null);
  const [result, updateRequirement] = useMutation(UPDATE_REQUIREMENT_FIELDS_MUTATION);
  const disabled = result.fetching;

  // Keep form in sync if the upstream requirement changes (e.g. a
  // sibling refresh). Reset touched because any further edits begin
  // fresh against the new baseline. Key on title + externalId as a
  // lightweight mtime proxy; the parent is responsible for passing a
  // fresh requirement object after a save.
  useEffect(() => {
    setValues(valuesFromRequirement(requirement));
    setTouched(untouchedRequirementForm);
    setError(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [requirement.id, requirement.title, requirement.externalId]);

  const handleSave = async () => {
    if (touched.title && values.title.trim() === "") {
      setError("Title is required");
      return;
    }
    setError(null);

    // Build the partial-update payload. Only include fields the user
    // actually touched. This preserves the pointer-vs-empty-string
    // distinction the backend honors: untouched = preserve,
    // touched-but-empty = clear.
    const input: Record<string, unknown> = { id: requirement.id };
    if (touched.externalId) input.externalId = values.externalId;
    if (touched.title) input.title = values.title.trim();
    if (touched.description) input.description = values.description;
    if (touched.source) input.source = values.source;
    if (touched.priority) input.priority = values.priority;
    if (touched.tags) input.tags = splitTags(values.tags);
    if (touched.acceptanceCriteria) input.acceptanceCriteria = splitLines(values.acceptanceCriteria);

    // Nothing was touched → no-op.
    if (Object.keys(input).length === 1) {
      onCancel?.();
      return;
    }

    const res = await updateRequirement({ input });
    if (res.error) {
      setError(res.error.graphQLErrors[0]?.message ?? res.error.message ?? "Update failed");
      return;
    }
    const updated = res.data?.updateRequirementFields as UpdatedRequirement | undefined;
    if (!updated) {
      setError("Update returned no data");
      return;
    }
    onSaved?.(updated);
  };

  return (
    <Panel variant="elevated" padding="lg" className="space-y-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-base font-semibold text-[var(--text-primary)]">Edit requirement</h3>
          <p className="mt-1 text-xs text-[var(--text-secondary)]">
            Untouched fields keep their current value. Clearing a field with an empty string
            removes that value on the server.
          </p>
        </div>
      </div>

      <RequirementForm
        mode="edit"
        values={values}
        onChange={(next, nextTouched) => {
          setValues(next);
          setTouched(nextTouched);
        }}
        touched={touched}
        disabled={disabled}
        error={error}
      />

      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={disabled}>
          Cancel
        </Button>
        <Button variant="primary" size="sm" onClick={handleSave} disabled={disabled}>
          {disabled ? "Saving…" : "Save"}
        </Button>
      </div>
    </Panel>
  );
}
