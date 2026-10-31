// The catalogue screen's pages, as a person meets them (M3.5 · T5b · S3, S8).
//
// The API answers one page of screenings and says where the next one starts. What the
// screen does with that answer is two decisions, and both are pure functions here so they
// can be read without a browser:
//
//   - where the next page lives. A cursor in the URL means the page a buyer reaches is a
//     URL they can bookmark, share and reload; a button that appends to the list loses all
//     three, so the next page is a link and nothing else;
//   - whether a problem is the catalogue refusing the cursor this screen arrived with. A
//     stale bookmark must not be a dead end, and it must not quietly render page one either,
//     which would look like the page it asked for.
//
// The last page says nothing about a next page: no disabled control, and no "page 1 of N",
// because the API does not say how many pages exist and a number here would be invented.
//
// This repository ships no DOM test runner (see components/components.test.ts), so the
// promises page.tsx makes in its file text are asserted from that text; the typecheck and
// the production build prove it compiles and renders.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import type { PublicCataloguePage, PublicProblem } from "../../lib/public-api.ts";
import {
  CATALOGUE_PATH,
  cursorFromParams,
  isRefusedCursor,
  nextPageHref,
} from "./catalogue-paging.ts";

const SOURCE = readFileSync(new URL("./page.tsx", import.meta.url), "utf8");

/** The file text of one function, so an assertion cannot be satisfied by another state. */
function functionSource(name: string): string {
  const start = SOURCE.indexOf(`function ${name}(`);
  assert.notEqual(start, -1, `page.tsx no longer declares ${name}`);
  const next = SOURCE.indexOf("\nfunction ", start + 1);
  return SOURCE.slice(start, next === -1 ? undefined : next);
}

function servedPage(overrides: Partial<PublicCataloguePage> = {}): PublicCataloguePage {
  return { hasMore: false, nextCursor: null, ...overrides };
}

function problem(overrides: Partial<PublicProblem> = {}): PublicProblem {
  return {
    type: "https://cinema.local/problems/invalid-input",
    title: "Invalid input",
    status: 400,
    code: "invalid_input",
    detail: "cursor is not a cursor this catalogue issued; read the first page again",
    ...overrides,
  };
}

// ── The next page is a URL ──────────────────────────────────────────────────

test("a first page with more behind it offers the next page at a URL", () => {
  const cursor = "djE6MjAyNi0wNS0wNFQyMDowMDowMFo6ZXZlbnQtbGF0ZQ";

  const href = nextPageHref(servedPage({ hasMore: true, nextCursor: cursor }));

  // The cursor the API served, in an address: following it, reloading it and sharing it all
  // reach the same page, because the page is the address and not a client's memory.
  assert.equal(href, `${CATALOGUE_PATH}?cursor=${cursor}`);
  assert.match(functionSource("CatalogueList"), /<Link href=\{nextHref\}>/);
});

test("the last page offers nothing to continue to", () => {
  // Three ways of having no next page, and none of them may produce a link: the API said
  // this is the end, or it served no cursor, or there is simply no page to continue to.
  assert.equal(nextPageHref(servedPage()), null);
  assert.equal(nextPageHref(servedPage({ nextCursor: "ignored-on-the-last-page" })), null);
  assert.equal(nextPageHref(servedPage({ hasMore: true })), null);
});

test("the last page says nothing about a next page", () => {
  const list = functionSource("CatalogueList");

  // Absent, not disabled: a control that cannot be used is a claim that something is there.
  assert.equal(/disabled/.test(list), false, "a last page renders no disabled control");
  assert.match(list, /nextHref === null \? null/, "the continuation is absent on the last page");
  // And no count of pages: the API says whether more exists, never how much of it there is.
  assert.equal(/page\s*\d+\s*of\s*\d+/i.test(SOURCE), false, "no invented \"page 1 of N\"");
});

// ── The page a buyer reached ────────────────────────────────────────────────

test("a cursor is taken from the URL as it was carried", () => {
  // Verbatim: the screen's job is to hand it back to the API, which is the only layer that
  // can read it.
  assert.equal(cursorFromParams({ cursor: "djE6K2FiYy89-_" }), "djE6K2FiYy89-_");
  // A repeated parameter is the URL's problem; the first is what the browser sent.
  assert.equal(cursorFromParams({ cursor: ["first", "second"] }), "first");
  // No cursor is the first page, and a blank one is no cursor at all — the API refuses an
  // empty `cursor` by name, so asking with one would earn a refusal nothing was wrong with.
  assert.equal(cursorFromParams({}), null);
  assert.equal(cursorFromParams({ cursor: "" }), null);
  assert.equal(cursorFromParams({ cursor: "   " }), null);
});

test("the page a buyer reached is read with the cursor it was reached by", () => {
  // The screen asks for the page its address names; the entries it prints are the ones the
  // API served for that cursor, never a second read of the first page.
  assert.match(SOURCE, /listCataloguePage\(\{/);
  assert.match(SOURCE, /cursor: cursor \?\? undefined/);
  assert.match(SOURCE, /screenings=\{result\.value\.events\}/);
});

// ── A bookmark that outlived its page ───────────────────────────────────────

test("a refused cursor is recognised as the cursor's refusal", () => {
  assert.equal(isRefusedCursor(problem(), "stale"), true);
  // Nothing was sent, so nothing was refused: a 400 here is about something else.
  assert.equal(isRefusedCursor(problem(), null), false);
  // A catalogue that could not be read keeps its own state and its own retry sentence.
  assert.equal(
    isRefusedCursor(
      problem({ status: 503, code: "service_unavailable", detail: "the catalogue is unavailable" }),
      "stale",
    ),
    false,
  );
  // Same status and code, another field: not this screen's page.
  assert.equal(isRefusedCursor(problem({ detail: "limit must be between 1 and 100" }), "stale"), false);
});

test("a refused cursor offers the catalogue from the beginning, in words", () => {
  const stale = functionSource("CatalogueStaleLink");

  // A sentence carries the state; greyscale and a screen reader read the same thing, so no
  // colour is the only cue.
  assert.match(stale, /no longer valid/);
  // A real link back to the catalogue's own address — the first page, which is the one
  // without a cursor — rather than a retry that would send the same cursor again.
  assert.match(stale, /<Link href=\{CATALOGUE_PATH\}>/);
  assert.match(stale, /from the beginning/);
  // It does not pretend to be page one: the screen says what happened instead of quietly
  // rendering the first page as if that were the page the link named.
  assert.match(SOURCE, /isRefusedCursor\(result\.error, cursor\)/);
});

// ── One screen, one h1 ─────────────────────────────────────────────────────

test("the screen keeps exactly the one h1 PageHeader owns", () => {
  // The gate asserts one `h1` per screen. PageHeader renders it, so a page that declares
  // its own would be the second one, and every state below it stays an `h2`.
  assert.match(SOURCE, /<PageHeader/);
  assert.equal((SOURCE.match(/<h1\b/g) ?? []).length, 0, "PageHeader owns the screen's h1");
  for (const heading of SOURCE.matchAll(/<h([1-6])\b/g)) {
    assert.equal(heading[1], "2", "every heading below the title is an h2");
  }
});
