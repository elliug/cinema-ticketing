// Button (M3.5 · T3 · S1).
//
// One control with three variants (primary, secondary, danger) and two states
// (disabled, busy), because every screen in the product asks the visitor to do one
// of three things and nothing else: go ahead, hold back, or destroy something.
//
// No `"use client"`: the component holds no state. A client parent can pass it a
// handler (it then compiles into that parent's client graph), and a server page can
// render it as plain HTML. `busy` is a prop, not a hook, so the same markup works
// in both worlds.
//
// Accessibility, which is the reason this is a component rather than CSS:
//
//   - a busy button keeps its label and gains a spinner that is `aria-hidden`, so
//     the name a screen reader announces does not change under the user;
//   - `aria-busy="true"` says the action was accepted, and `disabled` stops a
//     second click from sending it twice;
//   - the visible focus ring comes from the global `:focus-visible` rule, so a
//     button is never a control you cannot locate with the keyboard;
//   - a navigation action renders a link (Next's `Link` for a same-site path, an
//     anchor otherwise), never a button that pretends to be one.
import Link from "next/link";
import type { ButtonHTMLAttributes, ReactNode } from "react";

/** What the action does: proceed, hold back, or destroy. */
export type ButtonVariant = "primary" | "secondary" | "danger";

interface ButtonBase {
  children: ReactNode;
  variant?: ButtonVariant;
  /** The action was accepted and is in flight. */
  busy?: boolean;
  className?: string;
  id?: string;
}

/** A button that performs an action in place. */
export interface ButtonActionProps
  extends ButtonBase,
    Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children" | "className" | "id"> {
  href?: undefined;
}

/**
 * A link that looks like a button, for an action that is really navigation.
 *
 * Form attributes do not apply: a navigation target has no value to submit.
 */
export interface ButtonLinkProps extends ButtonBase {
  href: string;
  title?: string;
  target?: string;
  rel?: string;
}

export type ButtonProps = ButtonActionProps | ButtonLinkProps;

/** The props this component consumes itself and must not forward to the element. */
const CHROME_PROPS = ["children", "variant", "busy", "className", "id"];

/**
 * Everything else, forwarded to the `<button>`.
 *
 * A rest spread (`const { children, ...rest } = props`) would leave five names
 * assigned only to be dropped, which this repository's lint configuration reports
 * as unused. Copying the remaining entries keeps the full `ButtonHTMLAttributes`
 * surface for callers without that noise.
 */
function buttonAttributes(
  props: ButtonActionProps,
): Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children" | "className" | "id"> {
  const entries = Object.entries(props).filter(
    ([key]) => !CHROME_PROPS.includes(key),
  );
  return Object.fromEntries(entries) as Omit<
    ButtonHTMLAttributes<HTMLButtonElement>,
    "children" | "className" | "id"
  >;
}

export function Button(props: ButtonProps) {
  const { children, variant = "primary", busy = false, className, id } = props;
  const classes = ["btn", `btn--${variant}`, className]
    .filter((part): part is string => typeof part === "string" && part !== "")
    .join(" ");

  const content = busy ? (
    <>
      <span className="btn__spinner" aria-hidden="true" />
      {children}
    </>
  ) : (
    children
  );

  if (props.href !== undefined) {
    const { href, title, target, rel } = props;
    const linkProps = {
      id,
      title,
      target,
      className: classes,
      "aria-busy": busy || undefined,
    };
    // A same-site path gets client-side navigation; anything else is a plain
    // anchor, so an API download or an external page is never routed by Next.
    if (href.startsWith("/")) {
      return (
        <Link {...linkProps} href={href}>
          {content}
        </Link>
      );
    }
    return (
      <a {...linkProps} href={href} rel={rel ?? "noreferrer"}>
        {content}
      </a>
    );
  }

  // Narrowed by the discriminant: without `href` this is a form control.
  const buttonAttrs = buttonAttributes(props);

  return (
    <button
      {...buttonAttrs}
      id={id}
      type={buttonAttrs.type ?? "button"}
      className={classes}
      aria-busy={busy || undefined}
      disabled={busy || buttonAttrs.disabled === true}
    >
      {content}
    </button>
  );
}
