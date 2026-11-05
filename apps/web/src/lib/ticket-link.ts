// The page behind the link a purchase email carries, as one answer turned into words
// (T4 · D3, S3).
//
// The token in `/t/<token>` names a paid order and the instant it stops verifying, signed
// with a key of its own (S3). The web route redeems it in this process — the browser never
// learns where the API lives — and hands the answer here. This module is the whole of the
// decision, for the reason lib/issued-tickets.ts is: a rule that only exists inside JSX
// cannot be tested, and this page's honesty is a rule, not a layout.
//
// The API deliberately answers every way of *not* opening with one refusal: forged,
// tampered, expired, an order this database never sold, an order that was never paid. So
// the closed state is reached by all of them and is **identical** — [describeTicketLink]
// carries nothing of the refusal into it, because a view that printed the refusal's detail
// would say "expired" for an answer that has never known the difference.
//
// What is *ours* stays apart from what is the link's: a `503`, an unreachable API or a body
// that is not the answer is the retry state, and it says so as our failure. A page that
// turned a bad minute into "this link no longer opens" would tell a buyer their purchase
// was undone by an outage.
//
// D3's consequence is a sentence this module owns: links expire, and an email read a month
// later does not open. That is deliberate, and it is said instead of hidden, because the
// alternative was a permanent key sitting in a mailbox forever. Nothing here claims the
// address the email went to was verified (it never is, S2) or that anything was mailed to
// *this* reader: the link arrived, and that is all the page knows.
import type { PublicProblem, PublicResult, PublicTicketLink } from "./public-api.ts";

/** One answer of the link read, as the page renders it. */
export type TicketLinkView =
  /** The signature verified a paid sale, and these are its tickets. */
  | {
      kind: "opened";
      headline: string;
      message: string;
      nextStep: string;
      tone: "success";
      /** Whether asking again could change anything. Nothing is missing here. */
      retryable: false;
      /** The order the signature named, as the read confirmed it. */
      reservationId: string;
      /** The instant the link stops opening, printed verbatim with the answer. */
      expiresAt: string;
      /** The order's tickets, ascending as served. */
      ticketIds: string[];
    }
  /**
   * The link does not open. It carries **no** `problem`, and that is the point: an
   * aged-out email and a token this deployment never signed are one state, verbatim.
   */
  | {
      kind: "closed";
      headline: string;
      message: string;
      nextStep: string;
      tone: "warning";
      retryable: false;
    }
  /** The question could not be asked, so nothing about the link is known. */
  | {
      kind: "unavailable";
      headline: string;
      message: string;
      nextStep: string;
      tone: "error";
      retryable: true;
      /** The failure itself, so a surface can print which one it was. */
      problem: PublicProblem;
    };

/** The opened view alone, which is what the client component that keeps the ids receives. */
export type OpenedTicketLink = Extract<TicketLinkView, { kind: "opened" }>;

/**
 * One link read turned into the state a person sees and the words for it.
 *
 * The order of the checks is the rule: an unknown identifier is final and closed, while
 * *anything else* — including a `503`, an unreachable API and a `200` whose body is not
 * the answer — is a failure to ask and therefore retryable.
 */
export function describeTicketLink(
  result: PublicResult<PublicTicketLink>,
): TicketLinkView {
  if (result.ok) {
    return {
      kind: "opened",
      headline: "Este enlace abrió tus entradas",
      message:
        "La firma del enlace era buena, nombra un pedido pagado y la taquilla respondió con las entradas de ese pedido. La lista de abajo es lo que abrió: nada aquí comprueba quién eres, porque el enlace es la única credencial que tiene esta compra.",
      nextStep:
        "Abre cada entrada desde su propio enlace. El enlace de este correo deja de funcionar en el instante que se imprime abajo; los de las entradas no, y este navegador guarda nota de ellas en Mis entradas.",
      tone: "success",
      retryable: false,
      reservationId: result.value.reservationId,
      expiresAt: result.value.expiresAt,
      ticketIds: result.value.ticketIds,
    };
  }

  // The one refusal: expired, forged, tampered, unknown or never paid all arrive here, so
  // nothing of the problem travels into the view. It is the same state, verbatim, which is
  // exactly what the API promised and what S3 costs.
  const problem = result.error;
  if (problem.code === "not_found" || problem.status === 404) {
    return {
      kind: "closed",
      headline: "Este enlace ya no abre",
      message:
        "Un enlace como este va firmado y fechado, y pasada su fecha deja de abrir. La taquilla no dirá si esta dirección funcionó alguna vez — un correo que caducó y un enlace que nunca existió reciben una sola respuesta a propósito — así que esta página no puede decirte cuál de las dos cosas pasó. Los enlaces caducan; es a propósito, porque la alternativa es una llave permanente a las entradas de alguien en un buzón para siempre.",
      nextStep:
        "Abre Mis entradas en este navegador: una compra hecha en esta pestaña está en la lista, y cada entrada se abre por su propio enlace. Si la compra se hizo aquí, la confirmación que devolvió el pago abre el pedido otra vez.",
      tone: "warning",
      retryable: false,
    };
  }

  return describeLinkFailure(problem);
}

/**
 * The failures of ours, kept apart from the answer about the link.
 *
 * A refusal this build does not interpret is not a closed link either: the page says the
 * check failed and offers to ask again, rather than borrowing a verdict the box office did
 * not give.
 */
function describeLinkFailure(problem: PublicProblem): TicketLinkView {
  const temporary =
    problem.code === "network_error" ||
    problem.code === "service_unavailable" ||
    problem.code === "coordination_unavailable" ||
    problem.status >= 500;

  if (temporary) {
    return {
      kind: "unavailable",
      headline: "Este enlace no se ha podido abrir ahora mismo",
      message:
        "No se pudo preguntar a la taquilla qué abre este enlace, así que esta página no tiene respuesta sobre él. Es un fallo nuestro y no un juicio sobre el enlace: nada del enlace, del pedido que nombra ni de sus entradas ha cambiado.",
      nextStep:
        "Recarga esta página para preguntar otra vez. Si sigue fallando, abre Mis entradas en este navegador o usa la confirmación que devolvió el pago: nada de eso depende de este enlace.",
      tone: "error",
      retryable: true,
      problem,
    };
  }

  return {
    kind: "unavailable",
    headline: "La taquilla se negó a abrir este enlace",
    message: `No se comprobó el enlace: la respuesta fue ${problem.code}, que esta página no interpreta como un juicio sobre el enlace de tu correo.`,
    nextStep:
      "Recarga la página una vez para volver a preguntar. Esta página no decide por su cuenta que un enlace no valga.",
    tone: "error",
    retryable: true,
    problem,
  };
}

// ── What this tab did with the tickets it just opened ───────────────────────
//
// The link opens the tickets once, and the tab is what keeps them: opening a ticket is
// what adds it to "my tickets", and there is no identity for the box office to ask instead
// (milestone log, L3). So the page has to say two things and only two things — how many
// tickets this tab kept, and that the list is this browser's own note rather than an
// account. A browser that refused the write gets a third: the tickets are still openable
// from the links the page prints, because a refused write is a fact about the list and
// never about the purchase (S8).

/** How this tab's storage answered, as the page reports it. */
export type LinkRetention = "writing" | "kept" | "refused";

/**
 * One sentence about the tickets this tab kept, in words rather than in a count alone.
 *
 * `count` is how many tickets the link opened; the caller never passes zero, because a
 * link that opens nothing is a refused read and never reaches a surface.
 */
export function describeLinkRetention(retention: LinkRetention, count: number): string {
  const tickets = count === 1 ? "1 entrada" : `${count} entradas`;
  switch (retention) {
    case "writing":
      return count === 1
        ? "Guardando esta entrada en la lista de esta pestaña…"
        : `Guardando ${tickets} en la lista de esta pestaña…`;
    case "kept":
      return count === 1
        ? "Esa entrada ya está en Mis entradas de esta pestaña. Esa lista es la nota de este navegador sobre lo que compró: no es una cuenta, y no es un registro que guarde la taquilla."
        : `Esas ${tickets} ya están en Mis entradas de esta pestaña. Esa lista es la nota de este navegador sobre lo que compró: no es una cuenta, y no es un registro que guarde la taquilla.`;
    default:
      return `Esta pestaña se negó a guardar un registro de ${tickets} que abrió el enlace, así que no aparecerán en Mis entradas. Las entradas no se ven afectadas: cada una se abre desde su propio enlace de abajo, en cualquier momento.`;
  }
}
