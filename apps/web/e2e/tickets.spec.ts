// The tickets this tab keeps, in a browser (M3.5 · T9 · S7 · S3, S8).
//
// There is no account in this application, so "My tickets" cannot be a query: it is the
// list of ticket ids this browser tab kept when a ticket was opened in it. This spec
// proves the four things that claim depends on, each one on its own screen state:
//
//   - the list holds what this tab opened, and a reload does not lose it;
//   - a record the box office does not know is one forgettable row — the only kind this
//     screen may delete — and forgetting the last record returns the list to its empty
//     state;
//   - a new context, which kept nothing, says it kept nothing;
//   - a context whose `sessionStorage` refuses the read says *that*, and never claims
//     the tab kept no ticket: "your purchase is gone" and "this browser would not tell
//     me" are different facts, and the screen keeps them apart.
//
// The last test is the negative the milestone asks for: an identifier the box office
// does not know is answered as unknown, in words, and the ticket surface still renders
// one headline.
import { randomUUID } from "node:crypto";

import { expect, test } from "@playwright/test";

import {
  collectProblems,
  createAndPublishScreening,
  expectNoProblems,
  expectOneH1,
  ISSUED_TICKETS_KEY,
  openTicket,
  purchaseTwoSeats,
  visit,
  WEB_BASE_URL,
} from "./support";

/** An identifier the box office has never issued, so every read of it is a 404. */
const STALE_TICKET_ID = "11111111-2222-4333-8444-555555555555";

test.describe("tickets", () => {
  test("the list keeps the ticket this tab opened, and a reload keeps it", async ({
    page,
  }) => {
    const problems = collectProblems(page);

    const screening = await createAndPublishScreening(page, {
      filmTitle: "La entrada del navegador",
      runtimeMinutes: "97",
      rating: "12",
      price: "12.50",
      statusLabel: "On sale",
    });
    const purchase = await purchaseTwoSeats(page, screening);
    const ticketId = purchase.ticketIds[0] ?? "";
    expect(ticketId, "the payment issued a ticket").not.toBe("");

    // Opening the ticket is the moment this tab learns about it.
    await openTicket(page, ticketId);

    await visit(page, "/tickets");
    const primary = page.getByRole("navigation", { name: "Primary" });
    await expect(primary.getByRole("link", { name: "My tickets" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    await expect(primary.locator('[aria-current="page"]')).toHaveCount(1);

    // What the list holds: the ticket this tab opened, re-read from the box office.
    const kept = page.getByRole("heading", { name: "Tickets this tab kept" });
    await expect(kept).toBeVisible();
    await expect(
      page.getByRole("heading", { name: screening.filmTitle }),
      "the ticket this tab opened is listed by its screening",
    ).toBeVisible();
    // The seat is the one the keyboard chose, printed the way a ticket prints it (the
    // row and the number as painted at the door).
    await expect(page.getByText("Seat A1")).toBeVisible();
    await expect(
      page.getByRole("link", { name: `Open ticket ${ticketId}` }),
    ).toBeVisible();

    // A reload reads the same tab's storage and asks the box office again.
    await page.reload();
    await expectOneH1(page);
    await expect(
      page.getByRole("heading", { name: "Tickets this tab kept" }),
      "the list survived the reload",
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: `Open ticket ${ticketId}` }),
      "the same ticket is still listed, exactly once",
    ).toHaveCount(1);

    expectNoProblems(problems);
  });

  test("a record the box office does not know is forgotten, and forgetting the last one empties the list", async ({
    page,
  }) => {
    const problems = collectProblems(page);

    // A fresh tab kept nothing, and says so before anything is written.
    await visit(page, "/tickets");
    await expect(page.getByRole("heading", { name: "This tab has kept no ticket" })).toBeVisible();

    // The tab keeps one record — the state a stale purchase leaves behind, and the only
    // record this screen may delete. It is written into the same `sessionStorage` key the
    // application uses for the list (ISSUED_TICKETS_STORAGE_NAME in
    // `src/lib/issued-tickets.ts`), because that record belongs to the tab, not the API.
    await page.evaluate(
      ([key, id]) => {
        window.sessionStorage.setItem(key, JSON.stringify([id]));
      },
      [ISSUED_TICKETS_KEY, STALE_TICKET_ID] as const,
    );
    await page.reload();
    await expectOneH1(page);

    const staleRow = page.getByRole("heading", {
      name: "The box office does not know this ticket",
    });
    await expect(staleRow, "the stale record is one row, not a lost list").toBeVisible();
    await expect(
      page.getByRole("heading", { name: "This tab has kept no ticket" }),
      "a record the tab kept and the list it is not",
    ).toHaveCount(0);

    await page
      .getByRole("button", { name: "Forget this record on this device" })
      .click();

    // Forgetting the last record is what empties the list, and the empty state is what
    // the screen shows — never a list with nothing in it.
    await expect(
      page.getByRole("heading", { name: "This tab has kept no ticket" }),
      "the list is empty after the last record was forgotten",
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Tickets this tab kept" }),
      "no list is shown once it holds nothing",
    ).toHaveCount(0);
    expect(
      await page.evaluate((key) => window.sessionStorage.getItem(key), ISSUED_TICKETS_KEY),
      "the forgotten record is gone from this tab's storage",
    ).toBeNull();

    expectNoProblems(problems);
  });

  test("a new context shows the empty state, because it kept nothing", async ({
    browser,
  }) => {
    const context = await browser.newContext({ baseURL: WEB_BASE_URL });
    const page = await context.newPage();
    const problems = collectProblems(page);

    await visit(page, "/tickets");
    await expect(
      page.getByRole("heading", { name: "This tab has kept no ticket" }),
      "a tab that kept nothing says so",
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Tickets this tab kept" }),
      "and shows no list",
    ).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name: "This browser will not show what it kept" }),
      "an empty list is not an unreadable one",
    ).toHaveCount(0);

    expectNoProblems(problems);
    await context.close();
  });

  test("a context that denies sessionStorage says it cannot read the list, never that the list is empty", async ({
    browser,
  }) => {
    const context = await browser.newContext({ baseURL: WEB_BASE_URL });
    // A privacy mode that refuses the property outright: every read of
    // `window.sessionStorage` throws, which is the state `keptTicketStorage` reports as
    // unknown rather than as nothing.
    await context.addInitScript(() => {
      Object.defineProperty(window, "sessionStorage", {
        configurable: true,
        get() {
          throw new DOMException("storage denied", "SecurityError");
        },
      });
    });
    const page = await context.newPage();
    const problems = collectProblems(page);

    await visit(page, "/tickets");
    await expect(
      page.getByRole("heading", { name: "This browser will not show what it kept" }),
      "the screen says the browser would not answer",
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "This tab has kept no ticket" }),
      "an unreadable list is never drawn as an empty one",
    ).toHaveCount(0);
    await expect(page.getByText("This is not the same as having no ticket")).toBeVisible();

    expectNoProblems(problems);
    await context.close();
  });

  test("an identifier the box office does not know is answered as unknown", async ({
    page,
  }) => {
    const problems = collectProblems(page);
    const unknown = randomUUID();

    await visit(page, `/tickets/${unknown}`);
    await expect(
      page.getByRole("heading", { name: "The box office does not know this ticket" }),
      "the box office's answer is printed as unknown",
    ).toBeVisible();
    // The refusal is printed as the API answered it: the code and the status, unchanged.
    await expect(page.locator("main")).toContainText("not_found");
    await expect(page.locator("main")).toContainText("HTTP 404");
    await expect(
      page.getByRole("heading", { name: "This ticket is valid" }),
      "an unknown identifier is never drawn as a valid ticket",
    ).toHaveCount(0);

    expectNoProblems(problems);
  });
});
