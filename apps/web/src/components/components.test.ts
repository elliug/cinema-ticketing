// Structural checks for the shell's component set (M3.5 · T3 · S1, S2, S8).
//
// The components themselves are covered by what `pnpm check` already runs: the
// typecheck and the production build prove they compile and that every page can
// render them, and this repository ships no DOM test runner that would let a test
// mount a React tree without adding a dependency the other tasks do not share.
//
// What no type checker can see is the set of promises the *file text* makes, and
// those are the ones that decay quietly:
//
//   - that no component carries its own colour instead of a token from
//     globals.css, which is what keeps "one place changes the look" true;
//   - that the field keeps the label wired to its control, and the error wired to
//     the description — the wiring is what makes the red border a redundant cue
//     rather than the only one;
//   - that the alert keeps telling interruptive messages from polite ones;
//   - that the `"use client"` boundary stays in the one component that has state;
//   - and above all that the demo-session sentence in the header is the exported
//     constant rather than a copy of it, because a copy is a claim that can drift
//     from the documented one (S2, S8).
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { DEMO_SESSION_LABEL } from "../lib/demo-session.ts";

/** Every module of the component set, in the order components/index.ts exports it. */
const COMPONENT_MODULES = [
  "alert",
  "badge",
  "button",
  "card",
  "empty-state",
  "field",
  "page-header",
  "skeleton",
  "site-footer",
  "site-header",
] as const;

function sourceOf(moduleName: string): string {
  return readFileSync(new URL(`./${moduleName}.tsx`, import.meta.url), "utf8");
}

test("no component carries its own colour; every value comes from a token", () => {
  for (const moduleName of COMPONENT_MODULES) {
    const source = sourceOf(moduleName);
    for (const pattern of [/#[0-9a-fA-F]{3,8}\b/, /\brgba?\(/, /\bhsla?\(/]) {
      assert.equal(
        pattern.test(source),
        false,
        `${moduleName}.tsx must use a token from globals.css, not a literal colour`,
      );
    }
  }
});

test("the field keeps label, hint and error wired to the control", () => {
  const source = sourceOf("field");

  for (const attribute of ["htmlFor", "aria-describedby", "aria-invalid"]) {
    assert.equal(
      source.includes(attribute),
      true,
      `field.tsx must keep ${attribute} so the control is named and described`,
    );
  }
  assert.match(
    source,
    /export function fieldIds/,
    "fieldIds stays exported so a select or textarea can be wired the same way",
  );
});

test("the alert interrupts for a problem and waits its turn for a confirmation", () => {
  const source = sourceOf("alert");

  // The mapping is the whole behavior: an error the user just caused is assertive,
  // a success message is polite, and neither is left to the caller to decide.
  for (const [tone, role] of [
    ["info", "status"],
    ["success", "status"],
    ["warning", "alert"],
    ["error", "alert"],
  ] as const) {
    assert.match(
      source,
      new RegExp(`${tone}:\\s*"${role}"`),
      `alert.tsx must announce ${tone} with role="${role}"`,
    );
  }
});

test("the header renders the exported demo-session sentence instead of a copy", () => {
  const source = sourceOf("site-header");

  assert.equal(
    source.includes("DEMO_SESSION_LABEL"),
    true,
    "site-header.tsx must import DEMO_SESSION_LABEL",
  );
  // The first half of the exported sentence: if it appears verbatim in the
  // component, the header has its own copy and the two can drift.
  const snippet = DEMO_SESSION_LABEL.slice(0, 40);
  assert.equal(
    source.includes(snippet),
    false,
    "site-header.tsx must render DEMO_SESSION_LABEL, not a copy of its text",
  );
});

test("the navigation marks the current page for assistive technology", () => {
  const source = sourceOf("site-header");

  // The underline is the visible half; `aria-current` is the half a screen reader
  // hears, and it is what stops the current page from being just another link.
  assert.equal(
    source.includes("aria-current"),
    true,
    "site-header.tsx must mark the active entry with aria-current",
  );
  assert.match(
    source,
    /isCurrentPage\(pathname, item\.href\)/,
    "aria-current is decided by the pathname, not hard-coded per entry",
  );
});

test("only the header is a client component, because only the header has state", () => {
  const clientModules = COMPONENT_MODULES.filter((moduleName) =>
    sourceOf(moduleName).trimStart().startsWith('"use client"'),
  );

  assert.deepEqual(
    clientModules,
    ["site-header"],
    "the shell is server-rendered by default; add a client boundary only for state or handlers",
  );
});

test("every component module is reachable through the barrel", () => {
  const barrel = readFileSync(new URL("./index.ts", import.meta.url), "utf8");

  for (const moduleName of COMPONENT_MODULES) {
    assert.equal(
      barrel.includes(`from "./${moduleName}"`),
      true,
      `components/index.ts must export ${moduleName}.tsx`,
    );
  }
});

// ── The shadcn layer (M3.5 · T3b) ────────────────────────────────────────────
//
// `ui/` is the primitive layer every screen from T4 on composes. It is imported by
// path (`@/components/ui/button`), the shadcn convention, so the barrel above stays
// the semantic set the shell uses and the two layers cannot shadow each other.

test("every ui primitive exists and depends on nothing above it", () => {
  const primitives = [
    "alert",
    "badge",
    "button",
    "card",
    "dialog",
    "input",
    "label",
    "select",
    "skeleton",
    "tabs",
    "textarea",
    "tooltip",
  ];
  for (const name of primitives) {
    const source = sourceOf(`ui/${name}`);
    // A primitive may import the `cn` helper and its own Radix package, never one of
    // ours: a cycle here would make the layer impossible to reason about.
    assert.ok(
      !/from "\.\.\//.test(source) && !/from "@\/components"/.test(source),
      `ui/${name}.tsx must not import from components/`,
    );
  }
});

test("no ui primitive carries a literal colour", () => {
  for (const name of ["alert", "badge", "button", "card", "input", "select", "skeleton"]) {
    const source = sourceOf(`ui/${name}`);
    assert.ok(
      !/#[0-9a-fA-F]{3,8}\b/.test(source),
      `ui/${name}.tsx must use theme tokens, not a hex colour`,
    );
  }
});

test("the interactive primitives declare their client boundary", () => {
  for (const name of ["dialog", "select", "tabs", "tooltip", "label", "button"]) {
    const source = sourceOf(`ui/${name}`);
    assert.ok(
      source.startsWith('"use client";'),
      `ui/${name}.tsx needs "use client" for its Radix behaviour`,
    );
  }
  for (const name of ["card", "badge", "input", "textarea", "alert", "skeleton"]) {
    const source = sourceOf(`ui/${name}`);
    assert.ok(
      !source.startsWith('"use client";'),
      `ui/${name}.tsx has no state and must stay a server component`,
    );
  }
});
