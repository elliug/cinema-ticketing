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
//   - and that the demonstration session stays retired (identity-login T6 · S7).
import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { test } from "node:test";

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

/**
 * Every source file this application ships, under `src/`.
 *
 * TypeScript and TSX, and no test file except the caller's own: a rule about what the product
 * shows is a rule about what it renders, and the tests are not rendered.
 */
function sourceFiles(root: URL): URL[] {
  const found: URL[] = [];
  for (const entry of readdirSync(root, { recursive: true, withFileTypes: true })) {
    if (!entry.isFile() || !/\.tsx?$/.test(entry.name) || entry.name.endsWith(".test.ts")) {
      continue;
    }
    if (entry.name === "components.test.ts") {
      continue;
    }
    found.push(new URL(`${entry.parentPath}/${entry.name}`, "file://"));
  }
  return found;
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

// ── The demonstration session is gone (identity-login T6 · S7) ──────────────
//
// This file used to assert the opposite: that the header rendered the exported
// `DEMO_SESSION_LABEL` sentence rather than a copy of it. That assertion existed because the
// milestone kept a fake session in the header, and the only thing it could promise was that it
// granted nothing.
//
// `odd/tasks/m35-product-web.md` forbids (S2, S8, L3) presenting the demonstration session as
// authentication, and the day a real one arrived, keeping the fake one beside it became exactly
// that claim: two names in one header, one of them a real credential. So the switcher and its
// module were deleted, and what is pinned here is the absence — the module does not exist, no
// shipped source mentions it, and the header offers a link that claims nothing instead.

test("the demonstration session is gone from the tree, not merely hidden from the header", () => {
  for (const removed of ["../lib/demo-session.ts", "../lib/demo-session.test.ts"]) {
    assert.equal(
      existsSync(new URL(removed, import.meta.url)),
      false,
      `${removed} must not exist: a module nobody renders is a module somebody renders again`,
    );
  }
});

test("no shipped source mentions the demonstration session any more", () => {
  // Test files are read too, minus this one: an assertion that the session is gone has to be
  // able to name it. Everything that ships is scanned, because the rule is about what a person
  // can be shown, not about one component.
  const forbidden = ["demo-session", "DemoSession", "DEMO_SESSION_LABEL", "demo-switcher"];
  const offenders: string[] = [];

  for (const file of sourceFiles(new URL("../", import.meta.url))) {
    const source = readFileSync(file, "utf8");
    for (const term of forbidden) {
      if (source.includes(term)) {
        offenders.push(`${file.pathname.split("/src/")[1]}: ${term}`);
      }
    }
  }

  assert.deepEqual(
    offenders,
    [],
    "S7 retired the demonstration session; nothing shipped may name it again",
  );
});

test("the header offers the way in and claims nothing about who is signed in", () => {
  const source = sourceOf("site-header");

  // The label is the whole affordance. "Sign in" is a link to the screen that asks for one;
  // "signed in as Ana" would need the cookie, which this component cannot read, and a
  // client-side guess would hydrate differently from the server's HTML.
  assert.match(
    source,
    /href=\{SIGN_IN_PATH\}[\s\S]{0,120}>\s*Sign in\s*</,
    "site-header.tsx must link to the sign-in screen, labelled plainly",
  );
  for (const claim of ["Signed in as", "Signed out", "Sign out", "Your account"]) {
    assert.equal(
      source.includes(claim),
      false,
      `the header cannot know "${claim}": that sentence would have to come from the cookie`,
    );
  }
  // The constant, not a second copy of the path: `/sign-in` is one string in this application.
  assert.equal(
    source.includes('href="/sign-in"'),
    false,
    "site-header.tsx must use SIGN_IN_PATH rather than restating the route",
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
    /isCurrentEntry\(pathname, item\)/,
    "aria-current is decided by the pathname, not hard-coded per entry",
  );
});

// `aria-current` on a section (M3.5 · T9b · S3, S8)
//
// The rule itself is a pure function in site-header.tsx, and this file cannot import a
// `.tsx` module (no DOM test runner, no JSX loader), so the rule is pinned here as text
// and proven in a browser by the gate: while on `/admin/schedule`, the Administration
// entry is current. What is pinned is the shape that makes that true — a prefix declared
// by exactly one entry, and a rule that matches a prefix as well as an href.

test("the Administration entry owns its whole section, and only it declares a prefix", () => {
  const source = sourceOf("site-header");

  assert.ok(
    /\{ label: "Administration", href: "\/admin\/venues", prefix: "\/admin" \}/.test(
      source,
    ),
    "the Administration entry must declare the /admin prefix, because it links to one child of that section",
  );
  assert.equal(
    source.match(/\bprefix: /g)?.length,
    1,
    "only the Administration entry may widen its rule: a prefix on every entry would make several entries current at once",
  );
});

test("a declared prefix marks the entry current for every page under it", () => {
  const source = sourceOf("site-header");

  // The same rule every entry already used — exact, or a segment below — applied to the
  // prefix as well as to the href, so `/admin/schedule` and `/admin/layouts` are the
  // administration while `/administrator` is not.
  assert.ok(
    /function isCurrentPath\(pathname: string, base: string\): boolean \{/.test(source),
    "site-header.tsx must decide currency with one rule over a path base",
  );
  assert.ok(
    /isCurrentPath\(pathname, item\.href\)\s*\|[\s\S]{0,120}item\.prefix !== undefined[\s\S]{0,60}isCurrentPath\(pathname, item\.prefix\)/.test(
      source,
    ),
    "the current entry is its own href or, when it declares one, its prefix",
  );
  assert.ok(
    /pathname\.startsWith\(`\$\{base}\/`\)/.test(source),
    "a base is current for the pages below it, not for a sibling that merely starts with the same letters",
  );
});

test("both ticket surfaces call the PDF what the route delivers", () => {
  const library = readFileSync(
    new URL("../lib/issued-tickets.ts", import.meta.url),
    "utf8",
  );

  // One exported sentence, because only one of them can be true: the download route
  // declares `Content-Disposition: attachment`, so both screens promise a download and
  // neither calls it something else (S8).
  assert.equal(
    library.includes('export const TICKET_PDF_LINK_LABEL = "Download the ticket PDF";'),
    true,
    "lib/issued-tickets.ts must export the one sentence both ticket surfaces print",
  );
  for (const screen of [
    new URL("../app/tickets/[ticketID]/ticket-view.tsx", import.meta.url),
    new URL("../app/reservations/[reservationID]/payment-panel.tsx", import.meta.url),
  ]) {
    const source = readFileSync(screen, "utf8");
    assert.equal(
      source.includes("TICKET_PDF_LINK_LABEL"),
      true,
      `${screen.pathname} must print the exported sentence rather than its own`,
    );
    assert.equal(
      source.includes("Download the ticket PDF"),
      false,
      `${screen.pathname} must not carry a copy of the sentence: a copy is a promise that can drift`,
    );
  }
});

test("every navigation entry that has a screen points at it, and none is left as text", () => {
  const source = sourceOf("site-header");

  // L5's rule: an entry is plain text with a "not built yet" badge until its screen
  // exists, and the slice that builds that screen changes the href and nothing else.
  // T7 built `/tickets`, which was the last entry still waiting for its route.
  assert.match(
    source,
    /\{ label: "My tickets", href: "\/tickets" \}/,
    'site-header.tsx must point "My tickets" at the /tickets screen T7 built',
  );
  assert.equal(
    source.includes("href: null"),
    false,
    "no primary navigation entry may be left without an href while its route is served",
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
