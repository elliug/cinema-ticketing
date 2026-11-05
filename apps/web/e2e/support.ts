// Shared helpers for the M3.5 browser gate (M3.5 · T9 · S7).
//
// The three specs drive the same product through the same steps, so the steps live
// here once: an operator creates a film, schedules a screening against the room's
// published layout version with the price and the status that put it on sale in that one
// submit; a buyer opens it from the catalogue, chooses seats with the keyboard, pays and
// lands on the ticket.
//
// Three rules the helpers keep, because a gate that broke them would be asserting
// something the product never promised:
//
//   - **Everything is reached and read the way a person does it**: the paper a person
//     sees (text, an accessible name, a heading), never a CSS class or an internal
//     store. The one exception is deliberate and narrow: the ticket records are what
//     the *tab* keeps, so the ticket scenario writes one record into the same
//     `sessionStorage` key the application uses for it (ISSUED_TICKETS_STORAGE_NAME in
//     `src/lib/issued-tickets.ts`) — that is the tab's own state, not the API's.
//   - **Every navigation is checked**: one `h1` on the screen, exactly one primary
//     navigation entry marked `aria-current="page"` where the navigation claims one,
//     and no console error or hydration warning at the end of the test.
//   - **Nothing is retried and nothing is relaxed.** A selector that needs a retry to
//     pass is a product or a test defect, and this gate exists to find the first.
//
// The demo data the helpers rely on is the development seed the gate applies to its
// throwaway database (`db/seeds/dev_seed.sql`): the cinema "Cine Prueba", its room
// "Sala 1" and that room's published layout version 1, whose four enabled seats are
// A1, A2, A3 and B1 (B2 is disabled and never sold). Prices are per seat.
import { expect, type Locator, type Page, type Response } from "@playwright/test";

/** The web server the gate started. Set by `scripts/verify_m35.sh`. */
export const WEB_BASE_URL = process.env.WEB_BASE_URL ?? "http://127.0.0.1:3000";

/** The API the web server was pointed at; used only for the API's own 200 checks. */
export const API_BASE_URL = (process.env.API_BASE_URL ?? "http://127.0.0.1:8080").replace(
  /\/+$/,
  "",
);

/**
 * The one `sessionStorage` key a tab keeps its ticket ids under.
 *
 * It is `ISSUED_TICKETS_STORAGE_NAME` in `src/lib/issued-tickets.ts`, written here as a
 * literal because the browser-side module is not part of the running application bundle
 * this test imports. The ticket scenario writes one record into it on purpose: the
 * record belongs to the tab, so a tab whose purchase went stale is a state only the tab
 * can produce, and it is the only kind of record the list may forget.
 */
export const ISSUED_TICKETS_KEY = "cinema.issued-tickets";

/** The cinema, room and layout version the development seed creates. */
export const SEEDED = {
  cinema: "Cine Prueba",
  room: "Sala 1",
  layoutLabel: "v1 · publicada",
} as const;

/**
 * The start time the gate schedules, and what the venue's own clock prints for it.
 *
 * The form's label says the value is UTC — `datetime-local` carries no zone and
 * `parseStartsAt` reads it as one — so `18:30Z` is `20:30` at a venue on Europe/Madrid
 * summer time. The two strings are written down separately on purpose: the gate must
 * fail if the catalogue ever prints the UTC instant where it promised the venue's hour.
 */
export const STARTS_AT = {
  utcInput: "2031-05-04T18:30",
  venueHour: "2031-05-04T20:30:00+02:00",
  /**
   * The hour the board prints for that instant: the venue's own clock, as a person reads it.
   *
   * Written down separately from `venueHour` on purpose. The board prints the hour, so a
   * gate that derived one string from the other could not tell a screen printing the venue's
   * hour from one printing the UTC instant the form was given.
   */
  venueClock: "20:30",
  /**
   * The instant the markup carries in `datetime`: the same moment, written in UTC.
   *
   * The screen prints the venue's hour and hands a machine the served instant, so these are
   * two assertions and not one: `2026-…T18:30:00Z` is `20:30` at a Madrid venue in summer.
   */
  instant: "2031-05-04T18:30:00Z",
} as const;

/** One screening the gate creates through the administration screens. */
export interface ScreeningFixture {
  filmTitle: string;
  runtimeMinutes: string;
  rating: string;
  /** The price a person types, in major units. */
  price: string;
  /** The status option's visible word in the select. */
  statusLabel: string;
}

/** What the administration journey produced. */
export interface CreatedScreening {
  /** The identifier the schedule screen itself put in the address bar. */
  eventId: string;
  filmTitle: string;
  price: string;
  /**
   * What the venue's own clock prints for the start that was scheduled.
   *
   * It is written down here and compared against the served `startsAtLocal` by the
   * administration spec, so a catalogue that printed the UTC instant the form was given
   * fails instead of quietly agreeing with itself.
   */
  venueHour: string;
  /** The same instant as the board prints it: the hour on the chip, "20:30". */
  venueClock: string;
}

// ── The browser's own complaints ────────────────────────────────────────────

/** Everything the browser said that the gate refuses to ignore. */
export interface PageProblems {
  /** Every `console.error` and every `console.warn` carrying a warning. */
  consoleErrors: string[];
  /** The subset of the above that is a hydration mismatch. */
  hydrationWarnings: string[];
  /** Uncaught exceptions in the page. */
  pageErrors: string[];
}

/**
 * A hydration mismatch, whatever wording React or Next uses for it.
 *
 * React 19 says "Hydration failed because the server rendered HTML didn't match the
 * client"; older versions say the text content did not match. Both are the one defect
 * this gate must never smooth over: a screen that renders differently on the server and
 * the client is a screen a person sees flicker and a keyboard user may see lose focus.
 */
const HYDRATION_PATTERN =
  /hydrat|did not match|didn't match|server rendered (html|text)|text content does not match/i;

/**
 * How long a visit waits for the route's own prefetches to settle before returning.
 *
 * Long enough for a prefetch of this application's own routes, short enough that a stuck
 * request cannot stall the whole gate.
 */
const NETWORK_IDLE_TIMEOUT_MS = 5_000;

/** Starts collecting the browser's console for one page. Never throws. */
export function collectProblems(page: Page): PageProblems {
  const problems: PageProblems = {
    consoleErrors: [],
    hydrationWarnings: [],
    pageErrors: [],
  };

  page.on("console", (message) => {
    const type = message.type();
    if (type !== "error" && type !== "warning") {
      return;
    }
    const text = message.text();
    // A warning is only a problem when it is a hydration mismatch; everything the page
    // logs as an error is a problem on its own.
    if (type === "warning" && !HYDRATION_PATTERN.test(text)) {
      return;
    }
    problems.consoleErrors.push(`${type}: ${text}`);
    if (HYDRATION_PATTERN.test(text)) {
      problems.hydrationWarnings.push(text);
    }
  });

  page.on("pageerror", (error) => {
    problems.pageErrors.push(error.message);
  });

  return problems;
}

/** Fails the test with the browser's own words when the page complained at all. */
export function expectNoProblems(problems: PageProblems): void {
  expect(problems.hydrationWarnings, "hydration mismatches during the run").toEqual([]);
  expect(problems.pageErrors, "uncaught exceptions in the page").toEqual([]);
  expect(problems.consoleErrors, "console errors during the run").toEqual([]);
}

// ── Reading a screen the way a person does ──────────────────────────────────

/**
 * Asserts the screen has exactly one top-level heading.
 *
 * Every screen of this product is one page with one `h1`, and a second one is not a
 * cosmetic slip: a screen reader's heading list is how a keyboard user finds the page.
 */
export async function expectOneH1(page: Page): Promise<void> {
  const headings = page.locator("h1");
  await expect(headings, "one h1 per screen").toHaveCount(1);
  await expect(headings.first()).toBeVisible();
}

/**
 * The primary navigation marks this entry as the current page.
 *
 * `SITE_NAV` claims a current entry for the catalogue and for "Mis entradas", and for the
 * administration on every screen of its section: that entry declares the `/admin` prefix
 * as well as the venue href it links to. The gate asserts it exactly where the navigation
 * claims it; a page the navigation does not claim is not asserted to.
 */
export async function expectActiveNav(page: Page, label: string): Promise<void> {
  const primary = page.getByRole("navigation", { name: "Principal" });
  await expect(primary.getByRole("link", { name: label })).toHaveAttribute(
    "aria-current",
    "page",
  );
  await expect(primary.locator('[aria-current="page"]')).toHaveCount(1);
}

/**
 * Opens one address and checks the response and the screen's shape.
 *
 * The status is asserted because "the catalogue answers 200" is part of what the
 * milestone claims: an error page that happens to contain the right words would still
 * be a broken catalogue.
 */
export async function visit(page: Page, path: string): Promise<Response> {
  const response = await page.goto(path);
  expect(response, `${path} answered no response`).not.toBeNull();
  expect(response?.status(), `${path} status`).toBe(200);
  await expectOneH1(page);
  // Let the route's own prefetches finish before the caller navigates away.
  //
  // The App Router prefetches the links a screen renders, and a `goto` issued while one is
  // in flight aborts it. Chromium swallows that abort; Firefox reports it as a console error
  // — `Failed to fetch RSC payload … Falling back to browser navigation` — and the gate
  // treats a console error as a failure, correctly, because until proven otherwise it is a
  // defect. This abort is the test's, not the product's, so the wait belongs here instead of
  // in `collectProblems`: the gate keeps its rule and pays half a second per navigation.
  // Bounded, and tolerant of a page that never goes idle, because a stream that stays open
  // is not a defect either.
  await page.waitForLoadState("networkidle", { timeout: NETWORK_IDLE_TIMEOUT_MS }).catch(() => {});
  return response as Response;
}

/**
 * Chooses the one option of a native `<select>` whose visible text starts with `prefix`.
 *
 * A cinema's option carries what the API serves about it — its name, its time zone and
 * its address — so the gate picks it by the name a person would look for rather than by
 * the exact sentence the screen composed, which is the screen's business and not this
 * helper's.
 */
export async function selectByLabelPrefix(
  select: Locator,
  prefix: string,
): Promise<void> {
  const value = await select.evaluate((element, wanted) => {
    const options = Array.from((element as HTMLSelectElement).options);
    const match = options.find((option) =>
      (option.textContent ?? "").trim().startsWith(wanted),
    );
    return match?.value ?? "";
  }, prefix);
  expect(value, `the select offers an option starting with "${prefix}"`).not.toBe("");
  await select.selectOption(value);
}

// ── The administration journey ──────────────────────────────────────────────

/**
 * The form that schedules a screening.
 *
 * Both this form and the one that edits an existing screening ask for a "Precio por butaca"
 * and an "Estado", because they are the same two questions about the same columns. Scoping
 * by the form's own accessible name is how the gate — and a screen reader user, whose
 * focus enters a form with a name — says which of the two it means. The names are Castilian
 * since the Marquesina redesign, like everything these screens print.
 */
export function createScreeningForm(page: Page): Locator {
  return page.getByRole("form", { name: "Programar una sesión" });
}

/** The form that edits one existing screening's price and status, whichever is open. */
export function saleStateForm(page: Page): Locator {
  return page.getByRole("form", { name: /^Precio y estado de / });
}

/**
 * Runs the administration journey the milestone describes, in one page.
 *
 * Create a film, then schedule it in the seeded room against the room's published layout
 * version (both chosen from the selects, never pasted) **with the price and the status**, so
 * one `POST` is what puts it on sale (M4.1 · T3b · S9). The create form still accepts
 * neither, which is the draft path this journey used to take; the helper fills both in
 * because "one submit sells it" is the claim that has to be driven.
 *
 * It then stops on the price-and-status form, opened on the screening the create just
 * stored. That form is not the create's replacement — it is what edits a screening that
 * already exists — and the administration spec exercises it immediately afterwards.
 *
 * Returns the event id the screen itself put in the address bar, plus what the venue's
 * clock is expected to print for the start that was scheduled. The helper derives
 * neither from the API's answer: the administration spec compares the two, which is what
 * makes "the catalogue prints the venue's hour" a claim about the product.
 */
/**
 * A film title that belongs to this test alone.
 *
 * Both engines drive **one** stack, and a stack keeps what the other engine created: a
 * constant title then matches twice, and `toHaveCount(1)` fails on whichever engine runs
 * second. That is exactly what happened when the gate grew a second engine, and the
 * assertion was right both times — the fixture was not.
 *
 * The suffix keeps every screening distinguishable from every other run of the same spec,
 * in the same engine, in another engine, or in a database a developer reused by hand.
 */
function uniqueTitle(base: string): string {
  return `${base} · ${Math.random().toString(36).slice(2, 8)}`;
}

export async function createAndPublishScreening(
  page: Page,
  fixture: ScreeningFixture,
): Promise<CreatedScreening> {
  const filmTitle = uniqueTitle(fixture.filmTitle);
  await visit(page, "/admin/schedule");

  // 1. The film: created once, then chosen from the repertoire below.
  await page.getByLabel("Título").fill(filmTitle);
  await page.getByLabel("Duración (minutos)").fill(fixture.runtimeMinutes);
  await page.getByLabel("Calificación").fill(fixture.rating);
  await page.getByRole("button", { name: "Crear película" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Creada " }),
    "the film's confirmation",
  ).toContainText(`Creada ${filmTitle}`);

  // 2. The room and its published layout version: two reads, neither of them a guess.
  await selectByLabelPrefix(
    page.getByLabel("Cine", { exact: true }),
    SEEDED.cinema,
  );
  await page.getByRole("button", { name: "Ver las salas de este cine" }).click();
  await page.getByLabel("Sala", { exact: true }).selectOption({ label: SEEDED.room });
  await page.getByRole("button", { name: "Ver las versiones del plano" }).click();

  // 3. The screening itself: film and layout version chosen from the selects.
  await page
    .getByRole("combobox", { name: "Película" })
    .click();
  // `.first()`: the title carries this test's own suffix, so this row is the one the test
  // just created; the `.first()` is belt and braces for a reused database, not a guess.
  await page
    .getByRole("option", {
      name: `${filmTitle} (${fixture.runtimeMinutes} min, ${fixture.rating})`,
    })
    .first()
    .click();
  await page
    .getByRole("combobox", { name: "Versión publicada del plano" })
    .click();
  await page.getByRole("option", { name: SEEDED.layoutLabel }).click();
  await page.getByLabel("Hora de inicio (UTC)").fill(STARTS_AT.utcInput);

  // 4. The price and the status travel in that same create: this one submit is what puts
  //    the screening on sale, and the confirmation has to say so.
  const create = createScreeningForm(page);
  await create.getByLabel("Precio por butaca").fill(fixture.price);
  await create.getByRole("combobox", { name: "Estado" }).click();
  await page.getByRole("option", { name: fixture.statusLabel }).click();
  await create.getByRole("button", { name: "Programar la sesión" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Creada la sesión" }),
    "the screening's confirmation says it is already on sale",
  ).toContainText(`con precio ${fixture.price}, en venta`);

  // The screen opens the new screening's own form, and its address is where the identifier
  // comes from: this helper never builds one.
  await expect(page).toHaveURL(/[?&]eventId=/);
  const eventId = new URL(page.url()).searchParams.get("eventId") ?? "";
  expect(eventId, "the schedule screen named the new screening").not.toBe("");

  // 5. The price-and-status form, opened on what the create stored. It is the form the
  //    administration spec exercises next, and it opens on the served values — which is why
  //    "a submit that changes nothing" is a state this screen can actually be in.
  await expect(
    page.getByRole("heading", { name: "Precio y estado" }),
  ).toBeVisible();
  const edit = saleStateForm(page);
  await expect(
    edit.getByLabel("Precio por butaca"),
    "the edit form opens on the price the create stored",
  ).toHaveValue(fixture.price);
  await expect(
    edit.getByRole("combobox", { name: "Estado" }),
    "the edit form opens on the status the create stored",
  ).toContainText(fixture.statusLabel);

  return {
    eventId,
    filmTitle,
    price: fixture.price,
    venueHour: STARTS_AT.venueHour,
    venueClock: STARTS_AT.venueClock,
  };
}

/** The screening the API serves for one identifier, as far as the gate reads it. */
export interface ServedScreening {
  filmTitle: string;
  startsAtLocal: string;
  priceDisplay: string;
  onSale: boolean;
}

/** Reads one screening from the public API, so the gate can compare it with the screen. */
export async function readServedScreening(
  page: Page,
  eventId: string,
): Promise<ServedScreening> {
  const response = await page.request.get(`${API_BASE_URL}/api/v1/events/${eventId}`);
  expect(response.status(), `GET /api/v1/events/${eventId}`).toBe(200);
  const body = (await response.json()) as Partial<ServedScreening>;
  return {
    filmTitle: body.filmTitle ?? "",
    startsAtLocal: body.startsAtLocal ?? "",
    priceDisplay: body.priceDisplay ?? "",
    onSale: body.onSale === true,
  };
}

// ── The purchase ────────────────────────────────────────────────────────────

/** What one purchase produced. */
export interface Purchase {
  reservationId: string;
  /** The ticket every seat of the order was issued, in the order served. */
  ticketIds: string[];
}

/**
 * Buys two seats of one screening from the catalogue, keyboard only.
 *
 * The catalogue is opened rather than addressed by identifier, because that is what the
 * milestone promises a stranger can do. From the seat map onwards nothing is clicked:
 * the map is entered with Tab, the roving seat is walked with the arrow keys, Space
 * selects, the hold is taken with the keyboard and the payment is sent with Enter. If
 * any of that stops working, a person without a mouse cannot buy a ticket.
 */
export async function purchaseTwoSeats(
  page: Page,
  screening: CreatedScreening,
): Promise<Purchase> {
  await visit(page, "/events");
  await expectActiveNav(page, "Cartelera");

  // The entry the administration just published, found by its own words. A row that is on
  // sale prints what is left under the hour ("quedan 3"), because the board has no status
  // pill: the seats it still has are the sale state.
  const entry = page.getByRole("listitem").filter({ hasText: screening.filmTitle });
  await expect(entry, "the catalogue lists the new screening").toHaveCount(1);
  await expect(entry).toContainText(screening.venueClock);
  // The board prints the amount the way the application does — comma and euro sign — so
  // the expectation is the typed price repunctuated, never a second formatting rule.
  await expect(entry).toContainText(screening.price.replace(".", ","));
  await expect(entry).toContainText("quedan");

  await entry
    .getByRole("link", { name: screening.filmTitle, exact: true })
    .click();

  // The seat page is a screen of its own: its `h1` is the deed, and the strip above the map
  // prints the screening as served.
  await expectOneH1(page);
  await expect(page.locator("h1")).toHaveText("Elegir butacas");
  await expect(
    page.getByRole("region", { name: "Sesión" }),
    "the strip prints the screening the row opened",
  ).toContainText(screening.filmTitle);
  await expect(page.locator("time")).toHaveText(screening.venueClock);

  // Keyboard only. The map is one tab stop: Tab reaches its roving seat. The scroll
  // container is the named region that holds the seats, not the section around it.
  const map = page.getByRole("region", {
    name: "Plano de butacas, se desplaza en horizontal para llegar a todas",
  });
  await expect(map).toBeVisible();
  await map.focus();
  await page.keyboard.press("Tab");
  const focused = page.locator(":focus");
  await expect(focused, "the map's roving seat has the focus").toHaveAttribute(
    "aria-label",
    /^Fila A, butaca 1: libre$/,
  );
  await page.keyboard.press("Space");
  await expect(
    page.getByRole("status").filter({ hasText: "1 butaca seleccionada" }),
    "the first seat is selected by the keyboard",
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: /^Fila A, butaca 1\b/ }),
    "the seat the keyboard is on reports itself as pressed",
  ).toHaveAttribute("aria-pressed", "true");
  await page.keyboard.press("ArrowRight");
  await expect(page.locator(":focus")).toHaveAttribute(
    "aria-label",
    /^Fila A, butaca 2: libre$/,
  );
  await page.keyboard.press("Space");
  await expect(
    page.getByRole("status").filter({ hasText: "2 butacas seleccionadas" }),
    "the second seat is selected by the keyboard",
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: /^Fila A, butaca 2\b/ }),
    "the second seat reports itself as pressed",
  ).toHaveAttribute("aria-pressed", "true");

  // The hold, and then the way to the payment, both reached without a mouse.
  const hold = page.getByRole("button", { name: "Reservar 2 butacas" });
  await focusByTab(page, hold);
  await expectVisibleFocus(hold, "the hold button");
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("heading", { name: "Butacas reservadas: queda un paso" }),
    "the hold was accepted",
  ).toBeVisible();

  const toPayment = page.getByRole("link", { name: "Ir al pago" });
  await focusByTab(page, toPayment);
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/reservations\/[0-9a-f-]{36}/);
  await expectOneH1(page);
  const reservationId = page.url().split("/reservations/")[1]?.split("?")[0] ?? "";
  expect(reservationId, "the checkout's own address").not.toBe("");

  // The payment: reached by the keyboard, its focus ring visible before Enter, and the
  // amount is the order's — two seats at 12.50 is the box office's number, not this
  // page's.
  const pay = page.getByRole("button", { name: /^Pagar/ });
  await focusByTab(page, pay);
  await expectVisibleFocus(pay, "the Pay button");
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("heading", { name: "Pago aprobado" }),
    "the payment was approved",
  ).toBeVisible();

  const ticketLinks = page.getByRole("link", { name: /^Abrir la entrada / });
  await expect(ticketLinks, "one ticket per seat").toHaveCount(2);
  const ticketIds: string[] = [];
  for (const href of await ticketLinks.evaluateAll((links) =>
    links.map((link) => (link as HTMLAnchorElement).getAttribute("href") ?? ""),
  )) {
    ticketIds.push(href.split("/tickets/")[1] ?? "");
  }
  expect(ticketIds.filter((id) => id !== "")).toHaveLength(2);

  return { reservationId, ticketIds };
}

/**
 * Opens one ticket from the confirmation and returns what the browser fetched for it.
 *
 * The QR image is requested by the browser, not by this test: the responses are captured
 * as the page loads, so the spec can assert the route the image really came from.
 */
export async function openTicket(
  page: Page,
  ticketId: string,
): Promise<{ qrResponses: Response[] }> {
  const qrResponses: Response[] = [];
  // The suffix of this application's own route, not the API's: the page asks its own origin
  // so that the image works from whatever device opened it. An API path would not match.
  const suffix = `/api/tickets/${ticketId}/qr.svg`;
  const listener = (response: Response) => {
    if (response.url().endsWith(suffix)) {
      qrResponses.push(response);
    }
  };
  page.on("response", listener);

  await page.getByRole("link", { name: `Abrir la entrada ${ticketId}` }).click();
  await expect(page).toHaveURL(new RegExp(`/tickets/${ticketId}$`));
  await expectOneH1(page);
  await expectActiveNav(page, "Mis entradas");

  // Validity is the box office's answer, and the page says so in words.
  await expect(
    page.getByRole("heading", { name: "Esta entrada es válida" }),
    "the door's answer",
  ).toBeVisible();

  page.off("response", listener);
  return { qrResponses };
}

/** Moves the focus to one control with the Tab key alone, and proves it arrived. */
export async function focusByTab(page: Page, target: Locator): Promise<void> {
  // Starting from no focus at all, so the walk begins at the top of the document and the
  // control is proven reachable instead of merely focusable.
  await page.evaluate(() => {
    (document.activeElement as HTMLElement | null)?.blur();
  });
  for (let press = 0; press < 120; press += 1) {
    await page.keyboard.press("Tab");
    const arrived = await target.evaluate((element) => element === document.activeElement);
    if (arrived) {
      return;
    }
  }
  throw new Error(
    "the control was not reachable with the Tab key within 120 presses: a keyboard user cannot reach it",
  );
}

/**
 * Asserts a focused control paints a focus indicator.
 *
 * The product draws focus with a Tailwind ring (a box shadow) on `focus-visible`, and
 * the shell's stylesheet may add an outline; either one is a visible indicator, and
 * neither is a claim this gate invents.
 */
export async function expectVisibleFocus(
  target: Locator,
  label: string,
): Promise<void> {
  const indicator = await target.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      boxShadow: style.boxShadow,
      outlineStyle: style.outlineStyle,
      outlineWidth: style.outlineWidth,
    };
  });
  const ring = indicator.boxShadow !== "none" && indicator.boxShadow !== "";
  const outline =
    indicator.outlineStyle !== "none" &&
    indicator.outlineWidth !== "0px" &&
    indicator.outlineWidth !== "";
  expect(
    ring || outline,
    `${label} has no visible focus indicator: ${JSON.stringify(indicator)}`,
  ).toBe(true);
}
