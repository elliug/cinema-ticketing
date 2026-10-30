// The administration, in a browser (M3.5 · T9 · S7 · S3, S4, S8).
//
// The milestone's other half: an operator creates a film, then schedules a screening in a
// room against the room's published layout version **with the price and the status**, so one
// submit puts it on sale — without a SQL prompt and without pasting an identifier.
//
// The form that schedules it and the form that edits an existing screening are two
// different requests asking the same two questions, and the create carrying both fields must
// not cost the edit form its coverage. So the screen has to be honest four times:
//
//   - the create lands on sale in one request, and the price-and-status form below it opens
//     on exactly what that request stored;
//   - a submit that changes nothing is refused locally, in the API's own words,
//     instead of being sent as a request the server would refuse with the same
//     sentence;
//   - a price typed with three decimals is refused, and the *price input* is the
//     control the form marks — the API names the field at the start of its detail, and
//     the form points at exactly that field;
//   - and a real edit still saves, because the two refusals above never prove a write.
//
// Neither refusal may change what is stored, so the stored screening is read back from
// the public API after them; then the edit travels, and the catalogue has to list the
// screening at the venue's own hour and at the price the edit left.
import { expect, test } from "@playwright/test";

import {
  collectProblems,
  createAndPublishScreening,
  expectActiveNav,
  expectNoProblems,
  readServedScreening,
  saleStateForm,
  STARTS_AT,
  visit,
} from "./support";

test.describe("administration", () => {
  test("a screening is created, priced and put on sale from the screens, and refusals change nothing", async ({
    page,
  }) => {
    const problems = collectProblems(page);

    const screening = await createAndPublishScreening(page, {
      filmTitle: "La película del navegador",
      runtimeMinutes: "104",
      rating: "PG",
      price: "12.50",
      statusLabel: "On sale",
    });

    // ── A submit that changed nothing ─────────────────────────────────────────
    // The edit form opened on the price and status the API served — the ones the create
    // just stored — so pressing Save again is a request that would change nothing. It is
    // refused here, in the API's words, and it never reaches the network.
    const edit = saleStateForm(page);
    const save = edit.getByRole("button", { name: "Save price and status" });
    await save.click();
    const noChange = edit
      .getByRole("alert")
      .filter({ hasText: "nothing to change" });
    await expect(noChange, "the no-op submit is refused").toContainText(
      "nothing to change: provide priceCents or status",
    );
    await expect(
      edit.getByLabel("Price per seat"),
      "a refusal that named no field marks no input",
    ).not.toHaveAttribute("aria-invalid", "true");

    // ── A price with a third decimal ──────────────────────────────────────────
    // Money has two decimal places, and rounding what the operator typed would store a
    // price they did not enter — so 12.505 is refused and the price input is the one
    // marked invalid.
    const price = edit.getByLabel("Price per seat");
    await price.fill("12.505");
    await save.click();
    const badPrice = edit
      .getByRole("alert")
      .filter({ hasText: "at most two decimal places" });
    await expect(badPrice, "the three-decimal price is refused").toContainText(
      "priceCents: must have at most two decimal places",
    );
    await expect(price, "the price input is marked invalid").toHaveAttribute(
      "aria-invalid",
      "true",
    );

    // ── Neither refusal wrote anything ────────────────────────────────────────
    const served = await readServedScreening(page, screening.eventId);
    expect(served.filmTitle, "the screening the administration named").toBe(
      screening.filmTitle,
    );
    expect(served.priceDisplay, "the stored price after both refusals").toBe(
      screening.price,
    );
    expect(served.onSale, "the stored status after both refusals").toBe(true);
    expect(served.startsAtLocal, "the hour the API serves for it").toBe(
      STARTS_AT.venueHour,
    );

    // ── A real edit still saves ───────────────────────────────────────────────
    // The create carried the price, so nothing above has proven this form can write at
    // all: the two refusals exist to prove it refuses. This is the write — a new price
    // through the same form, with the status left where it was.
    const editedPrice = "13.00";
    await price.fill(editedPrice);
    await save.click();
    await expect(
      edit.getByRole("status").filter({ hasText: "Saved." }),
      "the edited price and status",
    ).toContainText(
      `The screening now holds ${editedPrice} per seat and its stored status is on_sale.`,
    );
    const edited = await readServedScreening(page, screening.eventId);
    expect(edited.priceDisplay, "the stored price after the edit").toBe(editedPrice);
    expect(edited.onSale, "the status the edit left alone").toBe(true);

    // ── The catalogue answers it ──────────────────────────────────────────────
    const response = await visit(page, "/events");
    expect(response.status(), "the catalogue answers 200").toBe(200);

    const primary = page.getByRole("navigation", { name: "Primary" });
    await expect(primary.getByRole("link", { name: "Now showing" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    await expect(primary.locator('[aria-current="page"]')).toHaveCount(1);

    const entry = page.getByRole("listitem").filter({ hasText: screening.filmTitle });
    await expect(entry, "the catalogue lists the new screening").toHaveCount(1);
    await expect(entry.locator("time"), "the venue's own hour").toHaveText(
      STARTS_AT.venueHour,
    );
    await expect(entry, "the price the administration set").toContainText(editedPrice);
    await expect(entry).toContainText("On sale");

    // The navigation's own claim. "Administration" links to the venue screen, and it is
    // the administration on every screen of the section: the entry declares the `/admin`
    // prefix, so the schedule and the layouts screens carry it too. Before that rule
    // existed only `/admin/venues` was marked, and the other two had no current entry at
    // all — which the previous run of this gate recorded rather than fixed.
    await visit(page, "/admin/venues");
    await expectActiveNav(page, "Administration");
    await visit(page, "/admin/schedule");
    await expectActiveNav(page, "Administration");
    await visit(page, "/admin/layouts");
    await expectActiveNav(page, "Administration");

    expectNoProblems(problems);
  });
});
