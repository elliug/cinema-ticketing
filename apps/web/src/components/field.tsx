// Field (M3.5 · T3 · S1).
//
// One label, one control, an optional hint and an optional error, wired together
// once so no form has to get the wiring right on its own. The three ids are derived
// from the field's `name` — deterministically, because a value from `useId` would
// differ between the server render and the client hydration of a form.
//
// No `"use client"`: the component renders markup and holds no state. Its `value`
// and `onChange` reach it from a client parent, like any other input.
//
// What the wiring buys (WCAG 1.3.1, 3.3.1, 3.3.2):
//
//   - `<label for="…">` names the control, so clicking the label focuses the input
//     and a screen reader announces the name;
//   - `aria-describedby` points at the hint and the error together, so the hint is
//     available while typing and the error is read when it appears;
//   - `aria-invalid="true"` marks a refused value for assistive technology, which
//     is what makes the red border and glyph a redundant cue rather than the only
//     one;
//   - `required` is the platform's own, so the field is announced as required.
import type { InputHTMLAttributes, ReactNode } from "react";

/** The ids of one field, for a form that supplies its own control. */
export interface FieldIds {
  control: string;
  hint: string;
  error: string;
}

/**
 * The ids a field's parts must carry, derived from its name.
 *
 * Exported because a `<select>` or a `<textarea>` needs the same wiring: render
 * [FieldLabel] with `htmlFor={fieldIds(name).control}` and set
 * `aria-describedby` to whichever of hint/error the form renders.
 */
export function fieldIds(name: string): FieldIds {
  return {
    control: name,
    hint: `${name}-hint`,
    error: `${name}-error`,
  };
}

export interface FieldLabelProps {
  htmlFor: string;
  children: ReactNode;
  required?: boolean;
}

/** The label half of a field, for a control this module does not render. */
export function FieldLabel({ htmlFor, children, required = false }: FieldLabelProps) {
  return (
    <label className="field__label" htmlFor={htmlFor}>
      {children}
      {required ? <span className="field__required"> (required)</span> : null}
    </label>
  );
}

export interface FieldProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "id" | "name" | "required"> {
  /** Name and id stem: the control is `name`, its parts `name-hint`/`name-error`. */
  name: string;
  label: ReactNode;
  /** How to fill it in; announced with the control. */
  hint?: ReactNode;
  /** Why the value was refused; announced with the control and marked invalid. */
  error?: string | null;
  required?: boolean;
  className?: string;
}

export function Field({
  name,
  label,
  hint,
  error,
  required = false,
  className,
  ...inputAttrs
}: FieldProps) {
  const ids = fieldIds(name);
  const hasError = typeof error === "string" && error !== "";

  // The hint and the error are both descriptions of the same control, so they are
  // listed together; an absent one contributes no id.
  const describedBy = [
    hint === undefined ? null : ids.hint,
    hasError ? ids.error : null,
  ].filter((id): id is string => id !== null);

  return (
    <div className={["field", className ?? ""].filter((part) => part !== "").join(" ")}>
      <FieldLabel htmlFor={ids.control} required={required}>
        {label}
      </FieldLabel>
      <input
        {...inputAttrs}
        id={ids.control}
        name={name}
        className="field__input"
        required={required}
        aria-invalid={hasError ? true : undefined}
        aria-describedby={describedBy.length === 0 ? undefined : describedBy.join(" ")}
      />
      {hint === undefined ? null : (
        <p className="field__hint" id={ids.hint}>
          {hint}
        </p>
      )}
      {hasError ? (
        <p className="field__error" id={ids.error}>
          <span aria-hidden="true">✕ </span>
          {error}
        </p>
      ) : null}
    </div>
  );
}
