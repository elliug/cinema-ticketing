// What this tab remembers about its tickets, and how the ticket surfaces read the
// box office's answer (M3.5 · T7 · S3, S8).
//
// Two things are decided here and nowhere else, so they are tested here:
//
//   1. **the kept-id store.** There is no identity in this application (milestone log,
//      L3): no route lists tickets, so a "my tickets" screen can only show what this
//      browser tab kept from its own purchases. That store is browser storage, which
//      can be missing, full, read-only, or full of a previous build's rubbish — so
//      every one of those is a state, and none of them may blank the list or invent a
//      ticket;
//   2. **the verify → display mapping**, and the map from one failed ticket read to the
//      words the screen prints. Validity is the API's answer (`POST /tickets/verify`),
//      never this application's opinion, and a refusal is not an invalid ticket: an
//      unreadable check is a different sentence from a forged code.
//
// The module is storage-agnostic on purpose (it is handed a `getItem`/`setItem`/
// `removeItem` triple), so all of it is testable without a DOM. The last section is the
// one thing a typechecker cannot see: that the two screens left the legacy stylesheet
// and that the PDF is a link and the QR image is named after the ticket.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import type {
  IssuedTicketStorage,
  TicketCheck,
} from "./issued-tickets.ts";
import {
  ISSUED_TICKETS_STORAGE_NAME,
  MAX_KEPT_TICKETS,
  describeKeptTicket,
  describeTicketValidity,
  forgetIssuedTicket,
  keptTicketStorage,
  readKeptTickets,
  rememberIssuedTicket,
  ticketSeatLabel,
} from "./issued-tickets.ts";
import type {
  PublicProblem,
  PublicTicket,
  PublicVerification,
  TicketResponse,
} from "./public-api.ts";

/** An in-memory stand-in for `window.sessionStorage`. */
function memoryStorage(initial?: Record<string, string>): IssuedTicketStorage & {
  entries: Map<string, string>;
} {
  const entries = new Map<string, string>(Object.entries(initial ?? {}));
  return {
    entries,
    getItem: (key) => entries.get(key) ?? null,
    setItem: (key, value) => {
      entries.set(key, value);
    },
    removeItem: (key) => {
      entries.delete(key);
    },
  };
}

/** A storage that refuses everything, like a browsing mode that denies it. */
const refusingStorage: IssuedTicketStorage = {
  getItem: () => {
    throw new Error("storage is denied");
  },
  setItem: () => {
    throw new Error("storage is denied");
  },
  removeItem: () => {
    throw new Error("storage is denied");
  },
};

/** A storage that reads but refuses every write, like a full quota. */
function readOnlyStorage(initial?: Record<string, string>): IssuedTicketStorage {
  const entries = new Map<string, string>(Object.entries(initial ?? {}));
  return {
    getItem: (key) => entries.get(key) ?? null,
    setItem: () => {
      throw new Error("quota exceeded");
    },
    removeItem: () => {
      throw new Error("quota exceeded");
    },
  };
}

function storedValue(storage: { entries: Map<string, string> }): string {
  return storage.entries.get(ISSUED_TICKETS_STORAGE_NAME) ?? "";
}

function keptIds(storage: IssuedTicketStorage): string[] {
  const read = readKeptTickets(storage);
  return read.kind === "kept" ? read.ids : [];
}

function ticket(overrides: Partial<PublicTicket> = {}): PublicTicket {
  return {
    ticketId: "ticket-1",
    status: "issued",
    valid: true,
    issuedAt: "2026-03-01T10:00:00Z",
    reservationId: "reservation-1",
    event: {
      id: "event-1",
      filmTitle: "The Long Take",
      startsAt: "2026-03-02T19:30:00Z",
      startsAtLocal: "2026-03-02 20:30",
      roomName: "Sala 1",
      cinemaName: "Cine Centro",
      timezone: "Europe/Madrid",
      status: "on_sale",
    },
    seats: [{ eventSeatId: 7, rowLabel: "A", seatNumber: "3" }],
    orderAmountCents: 1250,
    orderAmountDisplay: "12.50",
    qrToken: "signed-token",
    ...overrides,
  };
}

function verification(overrides: Partial<PublicVerification> = {}): PublicVerification {
  return {
    ticketId: "ticket-1",
    eventId: "event-1",
    seats: [{ eventSeatId: 7, rowLabel: "A", seatNumber: "3" }],
    ticketStatus: "issued",
    eventStatus: "on_sale",
    valid: true,
    ...overrides,
  };
}

function problem(overrides: Partial<PublicProblem> = {}): PublicProblem {
  return {
    type: "https://cinema.local/problems/service-unavailable",
    title: "Service unavailable",
    status: 503,
    code: "service_unavailable",
    detail: "the ticket store could not be read",
    ...overrides,
  };
}

function verified(value: PublicVerification): TicketCheck {
  return { kind: "verified", verification: value };
}

function refused(value: PublicProblem): TicketCheck {
  return { kind: "refused", problem: value };
}

// ── The kept-id store ───────────────────────────────────────────────────────

test("a fresh tab keeps no ticket, and says so without inventing one", () => {
  const read = readKeptTickets(memoryStorage());

  assert.equal(read.kind, "empty");
  assert.deepEqual(read.kind === "empty" ? read.unreadable : null, 0);
  // The store names the artifact under the `cinema.` namespace the payment key and
  // the demo session already use, so it is never mistaken for either.
  assert.equal(ISSUED_TICKETS_STORAGE_NAME, "cinema.issued-tickets");
});

test("a ticket this tab bought is kept, newest first", () => {
  const storage = memoryStorage();

  rememberIssuedTicket(storage, "ticket-1");
  rememberIssuedTicket(storage, "ticket-2");

  assert.deepEqual(keptIds(storage), ["ticket-2", "ticket-1"]);
  // One key, one JSON list: a second record shape would need a second reader.
  assert.deepEqual(JSON.parse(storedValue(storage)), ["ticket-2", "ticket-1"]);
});

test("remembering the same ticket twice keeps one record, at the front", () => {
  const storage = memoryStorage();

  rememberIssuedTicket(storage, "ticket-1");
  rememberIssuedTicket(storage, "ticket-2");
  const read = rememberIssuedTicket(storage, "ticket-1");

  assert.deepEqual(keptIds(storage), ["ticket-1", "ticket-2"]);
  assert.equal(read.kind, "kept");
  assert.deepEqual(JSON.parse(storedValue(storage)), ["ticket-1", "ticket-2"]);
});

test("forgetting one ticket leaves the others alone", () => {
  const storage = memoryStorage();
  rememberIssuedTicket(storage, "ticket-1");
  rememberIssuedTicket(storage, "ticket-2");

  forgetIssuedTicket(storage, "ticket-1");

  assert.deepEqual(keptIds(storage), ["ticket-2"]);
});

test("forgetting a ticket this tab never kept changes nothing", () => {
  const storage = memoryStorage();
  rememberIssuedTicket(storage, "ticket-1");

  const read = forgetIssuedTicket(storage, "ticket-9");

  assert.deepEqual(read.kind === "kept" ? read.ids : null, ["ticket-1"]);
  assert.deepEqual(JSON.parse(storedValue(storage)), ["ticket-1"]);
});

test("forgetting the last ticket empties the store instead of storing an empty list", () => {
  const storage = memoryStorage();
  rememberIssuedTicket(storage, "ticket-1");

  const read = forgetIssuedTicket(storage, "ticket-1");

  assert.equal(read.kind, "empty");
  assert.equal(storage.entries.has(ISSUED_TICKETS_STORAGE_NAME), false);
});

test("the kept list is capped, so a tab that buys for years still reads", () => {
  const storage = memoryStorage();

  for (let index = 0; index < MAX_KEPT_TICKETS + 5; index += 1) {
    rememberIssuedTicket(storage, `ticket-${index}`);
  }

  const ids = keptIds(storage);
  assert.equal(ids.length, MAX_KEPT_TICKETS);
  assert.equal(ids[0], `ticket-${MAX_KEPT_TICKETS + 4}`, "the newest record leads");
  assert.equal(
    ids.includes("ticket-0"),
    false,
    "the oldest record is the one dropped",
  );
});

test("a blank id is never written, so the store cannot hold a nameless record", () => {
  const storage = memoryStorage();

  const read = rememberIssuedTicket(storage, "   ");

  assert.equal(read.kind, "empty");
  assert.equal(storage.entries.has(ISSUED_TICKETS_STORAGE_NAME), false);
});

test("a storage that refuses the read is unavailable, never empty", () => {
  const read = readKeptTickets(refusingStorage);

  assert.equal(read.kind, "unavailable");
  // Unavailable and empty are different facts: one says nothing was bought, the
  // other says this browser would not answer.
  assert.match(read.kind === "unavailable" ? read.detail : "", /storage|browser/i);
});

test("a storage that refuses the write says so instead of claiming the ticket was kept", () => {
  const read = rememberIssuedTicket(readOnlyStorage(), "ticket-1");

  assert.equal(read.kind, "unavailable");
  assert.match(read.kind === "unavailable" ? read.detail : "", /store|storage|browser/i);
});

test("no storage at all is unavailable, and remembering does not pretend", () => {
  assert.equal(readKeptTickets(null).kind, "unavailable");
  assert.equal(rememberIssuedTicket(null, "ticket-1").kind, "unavailable");
  assert.equal(forgetIssuedTicket(null, "ticket-1").kind, "unavailable");
});

test("a stored value that is not a list of ids is unavailable", () => {
  for (const stored of ["not json", "{}", '"ticket-1"', "17"]) {
    const read = readKeptTickets(
      memoryStorage({ [ISSUED_TICKETS_STORAGE_NAME]: stored }),
    );
    assert.equal(
      read.kind,
      "unavailable",
      `${JSON.stringify(stored)} is not a record this page can read`,
    );
  }
});

test("one unreadable record does not blank the rest of the list", () => {
  const storage = memoryStorage({
    [ISSUED_TICKETS_STORAGE_NAME]: JSON.stringify([
      42,
      "ticket-2",
      "",
      null,
      "  ",
      "ticket-3",
      "ticket-2",
    ]),
  });

  const read = readKeptTickets(storage);

  assert.equal(read.kind, "kept");
  assert.deepEqual(read.kind === "kept" ? read.ids : null, ["ticket-2", "ticket-3"]);
  // Four entries could not be read as an id (a number, a blank string, a null and a
  // string of spaces), and the page says so rather than silently showing a shorter
  // list; the duplicate `ticket-2` is the same ticket twice, not unreadable data.
  assert.equal(read.kind === "kept" ? read.unreadable : -1, 4);
});

test("a list whose every record is unreadable is empty, and counts what it dropped", () => {
  const read = readKeptTickets(
    memoryStorage({ [ISSUED_TICKETS_STORAGE_NAME]: JSON.stringify([1, "", null]) }),
  );

  assert.equal(read.kind, "empty");
  assert.equal(read.kind === "empty" ? read.unreadable : -1, 3);
});

test("a browser that hides its storage behaves like one that has none", () => {
  const storage = memoryStorage();
  assert.equal(keptTicketStorage({ sessionStorage: storage }), storage);
  assert.equal(keptTicketStorage(null), null);
  assert.equal(keptTicketStorage(undefined), null);
  assert.equal(keptTicketStorage({}), null);
  // Reading the property itself can throw, not only its methods.
  assert.equal(
    keptTicketStorage({
      get sessionStorage(): IssuedTicketStorage {
        throw new Error("denied");
      },
    }),
    null,
  );
});

// ── The verify answer, in words ─────────────────────────────────────────────

test("a valid ticket says it admits its holder, and says who answered", () => {
  const view = describeTicketValidity(verified(verification()));

  assert.equal(view.kind, "valid");
  assert.equal(view.admits, true);
  assert.equal(view.retryable, false);
  assert.equal(view.reason, null);
  assert.equal(view.tone, "success");
  assert.match(view.headline, /valid/i);
  // The claim is the check's, not the page's: the box office just answered it.
  assert.match(`${view.message} ${view.nextStep}`, /box office|check/i);
});

test("a voided ticket says it was withdrawn, and names the reason", () => {
  const view = describeTicketValidity(
    verified(verification({ valid: false, reason: "voided", ticketStatus: "void" })),
  );

  assert.equal(view.kind, "voided");
  assert.equal(view.admits, false);
  assert.equal(view.reason, "voided");
  assert.equal(view.retryable, false);
  assert.match(view.message, /withdrawn|voided/i);
});

test("a cancelled screening does not make the ticket a forgery", () => {
  const view = describeTicketValidity(
    verified(
      verification({ valid: false, reason: "event_cancelled", eventStatus: "cancelled" }),
    ),
  );

  assert.equal(view.kind, "event_cancelled");
  assert.equal(view.admits, false);
  assert.equal(view.reason, "event_cancelled");
  assert.match(view.message, /cancel/i);
});

test("a reason this build does not know is printed, never interpreted", () => {
  const view = describeTicketValidity(
    verified(verification({ valid: false, reason: "transferred" })),
  );

  assert.equal(view.kind, "reason_unrecognised");
  assert.equal(view.admits, false);
  assert.equal(view.reason, "transferred", "the served reason is carried verbatim");
});

test("an invalid ticket with no reason is still reported as not admitting anyone", () => {
  const view = describeTicketValidity(verified(verification({ valid: false })));

  assert.equal(view.kind, "reason_unrecognised");
  assert.equal(view.admits, false);
  assert.equal(view.reason, null);
});

test("a code the box office never signed is refused, and asking again will not help", () => {
  const view = describeTicketValidity(
    refused(
      problem({
        status: 400,
        code: "invalid_token",
        title: "Invalid token",
        detail: "the presented code is not a ticket",
      }),
    ),
  );

  assert.equal(view.kind, "invalid_token");
  assert.equal(view.admits, false);
  assert.equal(view.retryable, false);
  assert.equal(view.reason, null);
  assert.match(view.headline, /not recognised/i);
});

test("an unreadable check is not a bad ticket: it is retryable and changes nothing", () => {
  for (const code of ["service_unavailable", "network_error", "coordination_unavailable"] as const) {
    const view = describeTicketValidity(refused(problem({ code, status: 503 })));

    assert.equal(view.kind, "unavailable", `${code} is a failed check`);
    assert.equal(view.admits, false);
    assert.equal(view.retryable, true);
    assert.match(view.nextStep, /again|reload/i);
  }
});

test("a 5xx that is not one of the named codes is still a failed check", () => {
  const view = describeTicketValidity(
    refused(problem({ code: "unexpected_response", status: 500 })),
  );

  assert.equal(view.kind, "unavailable");
  assert.equal(view.retryable, true);
});

test("a refusal this build does not interpret keeps its own words", () => {
  const view = describeTicketValidity(
    refused(problem({ code: "invalid_input", status: 0, detail: "a QR code is needed" })),
  );

  assert.equal(view.kind, "refused");
  assert.equal(view.retryable, true);
  assert.match(view.message, /invalid_input/, "the code is not hidden");
});

// ── One failed ticket read, in words ────────────────────────────────────────

test("a ticket that is still being read is a state of its own", () => {
  assert.equal(describeKeptTicket(null).kind, "reading");
});

test("a ticket the box office answered is shown as read", () => {
  const row = describeKeptTicket({ ok: true, ticket: ticket() });

  assert.equal(row.kind, "found");
  assert.equal(row.kind === "found" ? row.ticket.ticketId : "", "ticket-1");
});

test("a stale id the box office does not know is named, and asking again will not help", () => {
  const row: TicketResponse = {
    ok: false,
    problem: problem({ status: 404, code: "not_found", title: "Not found" }),
  };

  const described = describeKeptTicket(row);

  assert.equal(described.kind, "unknown");
  assert.equal(described.kind === "unknown" ? described.retryable : true, false);
  assert.match(
    described.kind === "unknown" ? described.message : "",
    /does not know|unknown|no ticket/i,
  );
});

test("an unreadable ticket is retryable, and never drawn as a stale one", () => {
  const row = describeKeptTicket({ ok: false, problem: problem() });

  assert.equal(row.kind, "unavailable");
  assert.equal(row.kind === "unavailable" ? row.retryable : false, true);
});

// ── The seat, as the door paints it ─────────────────────────────────────────

test("a seat reads as the venue paints it, never as a reconstructed label", () => {
  assert.equal(ticketSeatLabel({ rowLabel: "A", seatNumber: "3" }), "A3");
  assert.equal(ticketSeatLabel({ rowLabel: "A", seatNumber: "" }), "row A");
  assert.equal(ticketSeatLabel({ rowLabel: "", seatNumber: "3" }), "3");
  assert.equal(ticketSeatLabel({ rowLabel: "", seatNumber: "" }), "unknown seat");
  assert.equal(ticketSeatLabel({ rowLabel: " A ", seatNumber: " 3 " }), "A3");
});

// ── What the two screens promise in their file text ─────────────────────────
//
// The screens themselves are covered by the typecheck and the production build, and
// this repository ships no DOM test runner (see components/components.test.ts). What
// no typechecker sees is that the ticket surfaces left the legacy stylesheet, that the
// PDF is a link a browser follows, and that the QR image is named after the ticket
// rather than "QR".

function sourceAt(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), "utf8");
}

const TICKET_SCREENS = [
  "../app/tickets/page.tsx",
  "../app/tickets/[ticketID]/page.tsx",
  "../app/tickets/[ticketID]/ticket-view.tsx",
];

test("the ticket screens no longer use the legacy stylesheet", () => {
  const legacy = [
    'className="card',
    'className="problems',
    'className="notice"',
    'className="meta"',
    'className="ticket-facts"',
    'className="qr-image"',
    'className="download"',
    'className="feedback',
    'className="eyebrow"',
    'className="skip-link"',
  ];

  for (const path of TICKET_SCREENS) {
    const source = sourceAt(path);
    for (const className of legacy) {
      assert.equal(
        source.includes(className),
        false,
        `${path} still uses ${className}, which T7 migrated to utilities`,
      );
    }
  }
});

test("globals.css no longer carries the rules only the ticket screen used", () => {
  const css = sourceAt("../app/globals.css");

  // The rules, not the words: the inventory above still names what T7 deleted, which is
  // how the next migration knows it was deliberate.
  for (const selector of ["\n.ticket-facts", "\n.qr-image", "\na.download", "\n.qr {"]) {
    assert.equal(
      css.includes(selector),
      false,
      `globals.css still carries the ${selector.trim()} rule, and no remaining screen uses it`,
    );
  }
  // The rules the administration screens still need stay where they are.
  for (const selector of [".feedback", ".card {", ".problems", ".meta {"]) {
    assert.equal(
      css.includes(selector),
      true,
      `globals.css must keep ${selector}: administration still needs it`,
    );
  }
});

test("the QR image is named after the ticket and the PDF is a link", () => {
  const view = sourceAt("../app/tickets/[ticketID]/ticket-view.tsx");

  // An `alt` of "QR" names the object, not the ticket: a screen reader user hears the
  // same thing for every image on the page.
  assert.match(view, /alt=\{`[^`]*\$\{ticket\.ticketId\}/, "the alt must name the ticket");
  // A link, not a JS download: the browser follows the API's short-lived redirect and
  // the page never handles the bytes.
  assert.match(view, /href=\{pdfUrl\}/, "the PDF is an anchor target");
});

test("the ticket list reuses the checkout's own link shape", () => {
  const view = sourceAt("../app/tickets/[ticketID]/ticket-view.tsx");

  assert.match(
    view,
    /import \{[^}]*IssuedTickets[^}]*\} from "@\/app\/reservations\/\[reservationID\]\/payment-panel"/,
    "the list must reuse IssuedTickets rather than write a second link shape",
  );
});
