// What this browser tab remembers about its tickets, and how the ticket surfaces read
// the box office's answer (M3.5 · T7 · S3, S8).
//
// There is identity in this application since the magic-link work, and `/tickets` uses it when this
// browser holds a session: the account's list is read from the box office and this module is not
// consulted at all. What is left here is the **guest** record — the ids this tab kept from the
// purchases it made in it — which is still the only thing a buyer without an account has, and
// still cannot be a server-side query because there is no account to query for (D4: buying and
// opening a ticket never require one). That is the whole of [readKeptTickets] and its
// two neighbours, and it is why the screen says what it is instead of looking like an
// account.
//
// The store is browser storage, so it is also the part of this surface that can be
// missing: a privacy mode that denies the property, a full quota, a read-only profile,
// and a value written by a different build are all states, not crashes. Three rules
// follow, and each of them is tested:
//
//   - **unavailable is not empty.** A storage that refuses the read answers
//     `unavailable`; only a store that answered with nothing is `empty`. The screen
//     says which of the two happened, because "you have no tickets" is a claim about a
//     purchase and "this browser would not tell me" is not;
//   - **one bad record does not blank the list.** A stored list that holds a number, a
//     null and a blank string still yields its readable ids; the rest are counted and
//     the screen prints how many it could not read;
//   - **nothing is invented.** An id is written only when it is a non-blank string, and
//     the list is capped, so a tab that buys for years still reads.
//
// The second half of the module is the ticket surfaces' *words*: one verification answer
// or one failed ticket read turned into what a person reads. They live here, beside the
// store, because both screens print them and neither may disagree with the other — and
// because a rule that only exists inside JSX cannot be tested. Validity in particular is
// never this module's opinion: [describeTicketValidity] receives the API's answer and
// only chooses the sentence for it (S8).
//
// `sessionStorage` rather than `localStorage` is the caller's choice, and it is the same
// area the payment key and the demonstration session already use: a record kept here
// belongs to the tab that made the purchase, and a shared machine does not inherit the
// tickets someone else bought.
import type {
  PublicProblem,
  PublicTicket,
  PublicTicketSeat,
  PublicVerification,
  TicketResponse,
} from "./public-api.ts";

/**
 * What both ticket surfaces call the PDF link.
 *
 * One sentence, because there is one fact: the download route signs
 * `Content-Disposition: attachment` with a filename derived from the ticket, so every
 * browser saves the document instead of deciding from its media type whether to render
 * it. Before that was declared, the ticket screen said "Open the ticket PDF" while the
 * checkout panel said "Download the PDF", and the panel was promising something the
 * route never said — two screens describing one link differently is how a claim drifts
 * from the system (S8).
 *
 * It lives here, beside the rest of the ticket vocabulary, because the ticket page and the
 * confirmation panel both already read this module, and a sentence copied into each of
 * them could disagree again.
 */
export const TICKET_PDF_LINK_LABEL = "Descargar la entrada en PDF";

/** The storage surface this module needs, satisfied by `window.sessionStorage`. */
export interface IssuedTicketStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

/**
 * The one storage name this module reads and writes.
 *
 * It names the artifact ("issued-tickets") under the same `cinema.` namespace as the
 * payment key and the demonstration session, so a record can never be mistaken for
 * either of them.
 */
export const ISSUED_TICKETS_STORAGE_NAME = "cinema.issued-tickets";

/**
 * How many ticket ids one tab keeps.
 *
 * A cap exists so the record cannot grow without end; it is far above any plausible
 * purchase, and the oldest record is the one dropped, because the newest ticket is the
 * one a person is most likely to be looking for.
 */
export const MAX_KEPT_TICKETS = 50;

/** What this tab remembers, as the screen needs to print it. */
export type KeptTickets =
  /** The tab kept these ids; `unreadable` counts records that were not ids. */
  | { kind: "kept"; ids: string[]; unreadable: number }
  /** Nothing readable is kept, and `unreadable` says whether anything was there. */
  | { kind: "empty"; unreadable: number }
  /** This browser would not answer, so nothing can be said about what it holds. */
  | { kind: "unavailable"; detail: string };

/** What an unreadable store says, so both callers use one sentence. */
const UNAVAILABLE_DETAIL =
  "Esta pestaña no ha dejado leer lo que guarda, así que no puede mostrar la lista de entradas. La taquilla sigue teniendo las entradas: abre una desde su propio enlace.";

/** What a refused write says, so a screen cannot claim a ticket was kept. */
const UNWRITABLE_DETAIL =
  "Esta pestaña no ha querido guardar la entrada, así que puede que la lista no la conserve. La entrada sigue valiendo: ábrela desde su propio enlace cuando quieras.";

/**
 * The ticket ids this tab kept, in the order it kept them (newest first).
 *
 * Never throws. A `null` storage, a storage that denies the read, and a stored value
 * that is not a list are `unavailable`; an empty store is `empty`; everything else is
 * `kept`, including a list whose unreadable records were dropped — those are counted
 * rather than silently forgotten.
 */
export function readKeptTickets(
  storage: IssuedTicketStorage | null,
): KeptTickets {
  if (storage === null) {
    return { kind: "unavailable", detail: UNAVAILABLE_DETAIL };
  }

  let stored: string | null;
  try {
    stored = storage.getItem(ISSUED_TICKETS_STORAGE_NAME);
  } catch {
    return { kind: "unavailable", detail: UNAVAILABLE_DETAIL };
  }
  if (stored === null || stored.trim() === "") {
    return { kind: "empty", unreadable: 0 };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(stored);
  } catch {
    // A half-written value holds no records at all: it is not a list this page can
    // partially read, so it is unavailable rather than quietly empty.
    return { kind: "unavailable", detail: UNAVAILABLE_DETAIL };
  }
  if (!Array.isArray(parsed)) {
    return { kind: "unavailable", detail: UNAVAILABLE_DETAIL };
  }

  const ids: string[] = [];
  let unreadable = 0;
  for (const record of parsed) {
    const id = typeof record === "string" ? record.trim() : "";
    if (id === "") {
      unreadable += 1;
      continue;
    }
    // A duplicate is not unreadable data, it is the same ticket twice: it collapses
    // and keeps its first (newest) position.
    if (!ids.includes(id)) {
      ids.push(id);
    }
  }

  if (ids.length === 0) {
    return { kind: "empty", unreadable };
  }
  // The cap is applied here as well as on write, so a list written by an older build
  // cannot make the page render more entries than this one promises.
  return { kind: "kept", ids: ids.slice(0, MAX_KEPT_TICKETS), unreadable };
}

/**
 * Keeps one ticket id in this tab, at the front, and answers the list as it now stands.
 *
 * A blank id is never written. An id already kept moves to the front instead of being
 * recorded twice, so opening a ticket again reorders the list rather than growing it.
 * A refused write answers `unavailable` — never `kept` — because a screen that said
 * "saved" over a storage that refused would be claiming something that did not happen.
 */
export function rememberIssuedTicket(
  storage: IssuedTicketStorage | null,
  ticketId: string,
): KeptTickets {
  const id = ticketId.trim();
  if (storage === null) {
    return { kind: "unavailable", detail: UNAVAILABLE_DETAIL };
  }
  if (id === "") {
    // Nothing to remember, and nothing to write over: the store is left as it is.
    return readKeptTickets(storage);
  }

  const current = readKeptTickets(storage);
  // A store that answered `unavailable` holds nothing readable, so the write replaces
  // it: refusing to write would leave the tab permanently unable to keep any ticket.
  const kept = current.kind === "kept" ? current.ids : [];
  const ids = [id, ...kept.filter((existing) => existing !== id)].slice(
    0,
    MAX_KEPT_TICKETS,
  );

  try {
    storage.setItem(ISSUED_TICKETS_STORAGE_NAME, JSON.stringify(ids));
  } catch {
    return { kind: "unavailable", detail: UNWRITABLE_DETAIL };
  }
  return { kind: "kept", ids, unreadable: 0 };
}

/**
 * Forgets one ticket id, and answers the list as it now stands.
 *
 * Forgetting is the only way a person can clean out an id the box office no longer
 * knows, so it must work without touching the other records. An id this tab never kept
 * changes nothing, and nothing is written in that case — a pointless write is a write
 * that can fail and take the rest of the list with it.
 */
export function forgetIssuedTicket(
  storage: IssuedTicketStorage | null,
  ticketId: string,
): KeptTickets {
  const id = ticketId.trim();
  if (storage === null) {
    return { kind: "unavailable", detail: UNAVAILABLE_DETAIL };
  }
  if (id === "") {
    return readKeptTickets(storage);
  }

  const current = readKeptTickets(storage);
  if (current.kind === "unavailable") {
    // There is nothing to remove from a list that cannot be read, and rewriting it
    // here would destroy records this page never saw.
    return current;
  }
  const kept = current.kind === "kept" ? current.ids : [];
  const remaining = kept.filter((existing) => existing !== id);
  if (remaining.length === kept.length) {
    return current;
  }

  try {
    if (remaining.length === 0) {
      // An empty record and no record are the same fact, and the key is cheaper to
      // leave absent: a later read then answers `empty` without parsing anything.
      storage.removeItem(ISSUED_TICKETS_STORAGE_NAME);
    } else {
      storage.setItem(ISSUED_TICKETS_STORAGE_NAME, JSON.stringify(remaining));
    }
  } catch {
    return { kind: "unavailable", detail: UNWRITABLE_DETAIL };
  }
  if (remaining.length === 0) {
    return { kind: "empty", unreadable: current.unreadable };
  }
  return { kind: "kept", ids: remaining, unreadable: current.unreadable };
}

/** The part of `window` this module reads; kept narrow so a caller can fake it. */
export interface IssuedTicketWindowLike {
  sessionStorage?: IssuedTicketStorage;
}

/**
 * This tab's storage, or `null` when it cannot be used.
 *
 * Reading `window.sessionStorage` itself can throw — a browsing mode may deny the
 * property, not only its methods — so the access is guarded here, once, instead of at
 * every call site.
 */
export function keptTicketStorage(
  source: IssuedTicketWindowLike | null | undefined,
): IssuedTicketStorage | null {
  if (source === null || source === undefined) {
    return null;
  }
  try {
    return source.sessionStorage ?? null;
  } catch {
    return null;
  }
}

// ── The verification answer, in words (M3.5 · T7 · S3, S8) ──────────────────
//
// A door asks one question and the box office answers it: `POST /tickets/verify` with
// the served code. The page's job is nothing more than to say that answer out loud, and
// the answers are genuinely different from each other:
//
//   - the ticket admits its holder;
//   - it does not, because it was withdrawn (`voided`);
//   - it does not, because the screening was cancelled (`event_cancelled`);
//   - it does not, for a reason this build does not interpret — printed verbatim;
//   - the code is not a ticket of ours at all (`400 invalid_token`): a forgery and an
//     unknown ticket are deliberately one answer, and a retry cannot change it;
//   - the check itself failed (`503`, a network failure): **nothing about the ticket has
//     changed**, which is the one sentence that stops a buyer from believing their
//     purchase was undone by a bad minute.

/** What the page asked the box office, and what came back. */
export type TicketCheck =
  /** The API answered the question. */
  | { kind: "verified"; verification: PublicVerification }
  /** The question could not be answered, or the code was refused. */
  | { kind: "refused"; problem: PublicProblem };

/** How one verification answer reads. */
export type ValidityKind =
  /** The box office says this ticket admits its holder. */
  | "valid"
  /** The ticket itself was withdrawn. */
  | "voided"
  /** The screening was cancelled, so the issued ticket admits nobody. */
  | "event_cancelled"
  /** The box office says "not valid" and named a reason this build does not know. */
  | "reason_unrecognised"
  /** The code is not a ticket the box office signed, and no retry will change that. */
  | "invalid_token"
  /** The check failed. The ticket is unchanged; the answer is missing. */
  | "unavailable"
  /** A refusal this build does not interpret; its own words are printed. */
  | "refused";

/** One verification answer, as the ticket surface renders it. */
export interface ValidityView {
  kind: ValidityKind;
  /** The word that leads: the state itself, not a tone. */
  headline: string;
  /** What the box office's answer means. */
  message: string;
  /** What the person can do about it, if anything. */
  nextStep: string;
  /** The tone reinforces the words; it never carries them on its own. */
  tone: "success" | "warning" | "error" | "info";
  /** Whether this ticket admits its holder, as the box office answered. */
  admits: boolean;
  /** The machine-readable reason the box office reported, or null when none. */
  reason: string | null;
  /** Whether asking again is worth it. Never true for a refused code. */
  retryable: boolean;
}

/**
 * One verification answer turned into the sentence a person reads.
 *
 * `admits` is only ever `true` when the API said `valid: true`: nothing here reads the
 * ticket's stored status or the PDF, because neither is the door's answer (S8).
 */
export function describeTicketValidity(checked: TicketCheck): ValidityView {
  if (checked.kind === "refused") {
    return describeCheckFailure(checked.problem);
  }

  const { verification } = checked;
  if (verification.valid) {
    return {
      kind: "valid",
      headline: "Esta entrada es válida",
      message:
        "La taquilla ha comprobado esta entrada ahora mismo y ha respondido que admite a su titular en la sesión y en la butaca que se muestran aquí.",
      nextStep:
        "Enseña este código en el acceso. La validez se decide otra vez cada vez que se comprueba, así que esta respuesta describe esta comprobación y no una posterior.",
      tone: "success",
      admits: true,
      reason: null,
      retryable: false,
    };
  }

  const reason = verification.reason ?? null;
  if (reason === "voided") {
    return {
      kind: "voided",
      headline: "Esta entrada fue anulada",
      message:
        "La taquilla informa de que esta entrada está anulada: se retiró después de emitirse, así que ya no admite a nadie aunque se vendiera.",
      nextStep:
        "Esta página no puede deshacerlo. Contacta con la taquilla con el identificador de esta entrada — y no te fíes del PDF impreso, que se hizo antes de la anulación y no puede mostrarla.",
      tone: "warning",
      admits: false,
      reason,
      retryable: false,
    };
  }
  if (reason === "event_cancelled") {
    return {
      kind: "event_cancelled",
      headline: "La sesión se canceló",
      message:
        "La entrada sí se emitió, pero la sesión a la que pertenece se canceló, así que no admite a nadie. No es un fallo de la entrada.",
      nextStep:
        "Contacta con la taquilla por la sesión cancelada. Nada de esta página puede cambiar el estado de la entrada, y recargarla responderá lo mismo.",
      tone: "warning",
      admits: false,
      reason,
      retryable: false,
    };
  }

  return {
    kind: "reason_unrecognised",
    headline: "Esta entrada no admite a nadie",
    message:
      reason === null
        ? "La taquilla ha respondido que esta entrada no es válida y no ha indicado ningún motivo."
        : `La taquilla ha respondido que esta entrada no es válida y ha indicado un motivo que esta página no interpreta: «${reason}». Se imprime tal y como lo sirvió la taquilla, sin traducirlo.`,
    nextStep:
      "Contacta con la taquilla con el identificador de esta entrada. Esta página no decide por su cuenta qué significa un motivo que no interpreta.",
    tone: "warning",
    admits: false,
    reason,
    retryable: false,
  };
}

/** The failed-check states: a code that is not ours, or a check that did not happen. */
function describeCheckFailure(problem: PublicProblem): ValidityView {
  if (problem.code === "invalid_token") {
    return {
      kind: "invalid_token",
      headline: "Este código no se reconoce como una entrada",
      message:
        "La taquilla no firmó este código, así que no pertenece a ninguna entrada que haya emitido. Un código falsificado y uno desconocido se responden igual a propósito.",
      nextStep:
        "Si compraste una butaca, abre la entrada desde el enlace que devolvió el pago: ahí se sirve un código de verdad, y esta página nunca compone uno propio.",
      tone: "error",
      admits: false,
      reason: null,
      retryable: false,
    };
  }

  const temporary =
    problem.code === "network_error" ||
    problem.code === "service_unavailable" ||
    problem.code === "coordination_unavailable" ||
    problem.status >= 500;

  if (temporary) {
    return {
      kind: "unavailable",
      headline: "No se pudo comprobar la entrada",
      message:
        "No se pudo llegar a la taquilla para comprobar este código, así que esta página no tiene respuesta sobre él. Nada de la entrada ha cambiado: lo que falló fue la comprobación, no la compra.",
      nextStep:
        "Recarga esta página para volver a preguntar, y enseña esta página al personal si sigue fallando. Si la entrada admite a alguien lo decide la taquilla cuando se escanea, no esta página.",
      tone: "warning",
      admits: false,
      reason: null,
      retryable: true,
    };
  }

  return {
    kind: "refused",
    headline: "La taquilla se negó a comprobar este código",
    message: `La comprobación se rechazó con ${problem.code}: ${problem.detail}`,
    nextStep:
      "Recarga la página una vez para volver a preguntar. Esta página no interpreta el rechazo como un veredicto sobre la entrada.",
    tone: "error",
    admits: false,
    reason: null,
    retryable: true,
  };
}

// ── One ticket read, in words (M3.5 · T7 · S3, S8) ──────────────────────────
//
// The list screen reads each kept id from the box office, and the detail screen reads the
// id in its address. Both get the same answer type, and both must tell three states
// apart: still reading, read, and not readable. Within "not readable", a `404` is final —
// the box office does not know this ticket — while a `503` is worth asking again. One
// stale id therefore costs one row, never the list.

/** One ticket, as one surface renders it. */
export type KeptTicketRow =
  /** The read is in flight. */
  | { kind: "reading" }
  /** The box office answered with the ticket. */
  | { kind: "found"; ticket: PublicTicket }
  /** The box office does not know this identifier, and no retry will change that. */
  | {
      kind: "unknown";
      problem: PublicProblem;
      headline: string;
      message: string;
      nextStep: string;
      retryable: false;
    }
  /** The read failed; the ticket is not drawn at all. */
  | {
      kind: "unavailable";
      problem: PublicProblem;
      headline: string;
      message: string;
      nextStep: string;
      retryable: true;
    };

/**
 * One answer of a ticket read, in the terms a surface renders.
 *
 * `null` means the read has not answered yet, which is a state and not an error: a
 * screen that drew an unreadable ticket as a missing one would turn a slow box office
 * into a lost purchase.
 */
export function describeKeptTicket(answer: TicketResponse | null): KeptTicketRow {
  if (answer === null) {
    return { kind: "reading" };
  }
  if (answer.ok) {
    return { kind: "found", ticket: answer.ticket };
  }

  const problem = answer.problem;
  const missing = problem.code === "not_found" || problem.status === 404;
  if (missing) {
    return {
      kind: "unknown",
      problem,
      headline: "La taquilla no conoce esta entrada",
      message:
        "Este identificador no responde a ninguna entrada, así que no se puede mostrar nada de ella. Una entrada que existió y se borró y una dirección mal escrita se ven igual desde aquí.",
      nextStep:
        "Revisa el enlace que seguiste, u olvida este registro en este dispositivo para que deje de aparecer en la lista.",
      retryable: false,
    };
  }

  const temporary =
    problem.code === "network_error" ||
    problem.code === "service_unavailable" ||
    problem.code === "coordination_unavailable" ||
    problem.status >= 500;
  return {
    kind: "unavailable",
    problem,
    headline: temporary
      ? "No se pudo leer esta entrada"
      : "La taquilla se negó a leer esta entrada",
    message: temporary
      ? `No se pudo llegar a la taquilla, así que esta entrada no se ha leído. La API respondió ${problem.code}.`
      : `La lectura se rechazó con ${problem.code}: ${problem.detail}`,
    nextStep: temporary
      ? "Vuelve a preguntar: leer una entrada no cambia nada, así que un reintento no puede cobrar, retener ni liberar nada. Las demás entradas de la lista no se ven afectadas."
      : "Vuelve a preguntar una vez; si sigue igual, abre la entrada desde el enlace que devolvió el pago.",
    retryable: true,
  };
}

/**
 * The seat as the door paints it: the row label and seat number the venue uses.
 *
 * Never a reconstructed label, and never a guess: a value the API did not serve is said
 * to be unknown rather than filled in from the event seat id.
 */
export function ticketSeatLabel(seat: Pick<PublicTicketSeat, "rowLabel" | "seatNumber">): string {
  const row = seat.rowLabel.trim();
  const number = seat.seatNumber.trim();
  if (row !== "" && number !== "") {
    return `${row}${number}`;
  }
  if (row !== "") {
    return `fila ${row}`;
  }
  if (number !== "") {
    return number;
  }
  return "butaca desconocida";
}
