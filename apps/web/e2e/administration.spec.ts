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
      statusLabel: "En venta",
    });

    // ── A submit that changed nothing ─────────────────────────────────────────
    // The edit form opened on the price and status the API served — the ones the create
    // just stored — so pressing Save again is a request that would change nothing. It is
    // refused here, in the API's words, and it never reaches the network.
    const edit = saleStateForm(page);
    const save = edit.getByRole("button", { name: "Guardar precio y estado" });
    await save.click();
    const noChange = edit
      .getByRole("alert")
      .filter({ hasText: "nothing to change" });
    await expect(noChange, "the no-op submit is refused").toContainText(
      "nothing to change: provide priceCents or status",
    );
    await expect(
      edit.getByLabel("Precio por butaca"),
      "a refusal that named no field marks no input",
    ).not.toHaveAttribute("aria-invalid", "true");

    // ── A price with a third decimal ──────────────────────────────────────────
    // Money has two decimal places, and rounding what the operator typed would store a
    // price they did not enter — so 12.505 is refused and the price input is the one
    // marked invalid.
    const price = edit.getByLabel("Precio por butaca");
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
      edit.getByRole("status").filter({ hasText: "Guardado." }),
      "the edited price and status",
    ).toContainText(
      `La sesión tiene ahora ${editedPrice} por butaca y su estado guardado es on_sale.`,
    );
    const edited = await readServedScreening(page, screening.eventId);
    expect(edited.priceDisplay, "the stored price after the edit").toBe(editedPrice);
    expect(edited.onSale, "the status the edit left alone").toBe(true);

    // ── The catalogue answers it ──────────────────────────────────────────────
    const response = await visit(page, "/events");
    expect(response.status(), "the catalogue answers 200").toBe(200);

    const primary = page.getByRole("navigation", { name: "Principal" });
    await expect(primary.getByRole("link", { name: "Cartelera" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    await expect(primary.locator('[aria-current="page"]')).toHaveCount(1);

    const entry = page.getByRole("listitem").filter({ hasText: screening.filmTitle });
    await expect(entry, "the catalogue lists the new screening").toHaveCount(1);
    // The chip carries the served instant as its machine value and the venue's own hour as
    // what a person reads, so the two are asserted apart: `datetime` is the UTC instant the
    // API serves, the text is the hour a person reads at that venue.
    await expect(entry.locator("time"), "the served instant as a machine value").toHaveAttribute(
      "datetime",
      STARTS_AT.instant,
    );
    await expect(entry.locator("time"), "the venue's own hour").toHaveText(
      STARTS_AT.venueClock,
    );
    await expect(entry, "the price the administration set").toContainText(
      editedPrice.replace(".", ","),
    );
    // A session on sale prints the seats it still has; the board has no status pill.
    await expect(entry).toContainText("quedan");

    // The navigation's own claim. "Administration" links to the venue screen, and it is
    // the administration on every screen of the section: the entry declares the `/admin`
    // prefix, so the schedule and the layouts screens carry it too. Before that rule
    // existed only `/admin/venues` was marked, and the other two had no current entry at
    // all — which the previous run of this gate recorded rather than fixed.
    await visit(page, "/admin/venues");
    await expectActiveNav(page, "Administración");
    await visit(page, "/admin/schedule");
    await expectActiveNav(page, "Administración");
    await visit(page, "/admin/layouts");
    await expectActiveNav(page, "Administración");

    expectNoProblems(problems);
  });

  // Issue #92: two of the four screens had no way in at all, and `/admin` itself was a 404 —
  // an operator looking for "Create a film" landed on the venue screen and concluded the
  // feature did not exist. The section navigation exists so that never happens again, and
  // this test walks it rather than reading it: a link that renders but lands nowhere is
  // exactly the failure the issue describes.
  test("every administration screen is reachable from the section's own navigation", async ({
    page,
  }) => {
    const problems = collectProblems(page);

    // `/admin` is a map now, not a 404. `visit` asserts the 200 and the screen's single h1.
    await visit(page, "/admin");
    await expectActiveNav(page, "Administración");

    const sections = page.getByRole("navigation", {
      name: "Secciones de administración",
    });
    for (const label of [
      "Cines, salas y planos",
      "Editor de planos",
      "Películas y sesiones",
      "Acceso",
    ]) {
      await expect(sections.getByRole("link", { name: label })).toBeVisible();
    }

    // Walking it is the claim: the index reaches a screen, and that screen reaches another,
    // without anyone typing an address. The URL assertions wait for the navigation the click
    // causes, which is the discipline this gate has had to learn twice.
    await sections.getByRole("link", { name: "Editor de planos" }).click();
    await expect(page).toHaveURL(/\/admin\/layouts$/);
    await sections.getByRole("link", { name: "Películas y sesiones" }).click();
    await expect(page).toHaveURL(/\/admin\/schedule$/);
    await expectActiveNav(page, "Administración");

    expectNoProblems(problems);
  });
});
