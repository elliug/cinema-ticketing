// The purchase, in a browser (M3.5 · T9 · S7 · S3, S6, S8).
//
// The milestone's exit criterion: a stranger opens the catalogue, chooses two seats
// without a mouse, pays, and lands on a ticket whose validity the box office answered
// and whose entry code the browser really fetched. Everything here is reached by
// clicking, tabbing, pressing arrow keys and pressing Enter — the same screen a person
// uses, driven the way a person using only a keyboard would drive it.
//
// What this spec deliberately does not do: re-derive what the API serves. The venue
// hour and the price are asserted against what the administration put there, the two
// seats are asserted by the names the map gives them, and the amount is asserted to be
// the order's own number. A gate that recomputed those would be testing its own
// arithmetic instead of the product's.
import { readFile, stat } from "node:fs/promises";

import { expect, test } from "@playwright/test";

import {
  API_BASE_URL,
  collectProblems,
  createAndPublishScreening,
  expectNoProblems,
  expectOneH1,
  openTicket,
  purchaseTwoSeats,
} from "./support";

test.describe("purchase", () => {
  test("a buyer picks two seats with the keyboard, pays, and lands on a valid ticket", async ({
    page,
  }) => {
    const problems = collectProblems(page);

    // The screening exists because the administration created and published it — the
    // same journey the administration spec asserts, so this purchase is not standing on
    // a fixture written straight into the database.
    const screening = await createAndPublishScreening(page, {
      filmTitle: "La compra del navegador",
      runtimeMinutes: "104",
      rating: "PG",
      price: "12.50",
      statusLabel: "On sale",
    });

    const purchase = await purchaseTwoSeats(page, screening);

    // The order carries the two seats the keyboard chose, in the map's own words.
    await expect(
      page.getByText("2 seats: Row A seat 1, Row A seat 2"),
      "the order holds the two seats the keyboard selected",
    ).toBeVisible();

    // The money: one charge for the whole order, two seats at 12.50, and the number is
    // the payment answer's rather than anything this page multiplied.
    await expect(page.getByText("25.00 was taken for this order")).toBeVisible();

    // ── The ticket ────────────────────────────────────────────────────────────
    const ticketId = purchase.ticketIds[0] ?? "";
    expect(ticketId, "the payment issued a ticket").not.toBe("");
    const { qrResponses } = await openTicket(page, ticketId);

    // The code the page shows is the API's own image, fetched by the browser.
    const qr = page.getByRole("img", { name: `Entry code for ticket ${ticketId}` });
    await expect(qr).toBeVisible();
    await expect(qr).toHaveAttribute(
      "src",
      new RegExp(`^${escapeRegExp(API_BASE_URL)}/api/v1/tickets/${ticketId}/qr\\.svg$`),
    );
    expect(qrResponses, "the browser fetched the entry code once").toHaveLength(1);
    expect(qrResponses[0]?.status(), "the entry code's status").toBe(200);
    expect(
      qrResponses[0]?.headers()["content-type"],
      "the entry code's media type",
    ).toContain("image/svg+xml");
    expect(
      await qr.evaluate((image) => {
        const img = image as HTMLImageElement;
        return img.complete && img.naturalWidth > 0;
      }),
      "the entry code's image decoded in the browser",
    ).toBe(true);

    // The PDF: a real download, named after the ticket, with a real document inside.
    const pdfLink = page.getByRole("link", { name: "Open the ticket PDF" });
    const [download] = await Promise.all([
      page.waitForEvent("download"),
      pdfLink.click(),
    ]);
    expect(download.suggestedFilename(), "the download is named as a PDF").toMatch(
      /\.pdf$/,
    );
    const path = await download.path();
    expect(path, "the browser stored the download").not.toBeNull();
    const downloaded = await stat(path as string);
    expect(downloaded.size, "the downloaded document is not empty").toBeGreaterThan(1000);
    const bytes = await readFile(path as string);
    expect(bytes.subarray(0, 5).toString("latin1"), "the document header").toBe("%PDF-");

    await expectOneH1(page);
    expectNoProblems(problems);
  });
});

/** Escapes one string for a regular expression, so a URL can be compared literally. */
function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
