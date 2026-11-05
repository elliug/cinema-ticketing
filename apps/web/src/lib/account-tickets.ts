// /tickets, as answers turned into states and words (identity-login T7 · S5, D3).
//
// This screen has one rule and two framings, and both belong here rather than inside JSX:
//
//   - **which list it is** follows from one fact the server knows and the browser does not: does
//     this browser hold a session cookie? With one, the list is the account's and the box office
//     answers it, so the ids are no longer this tab's to keep. Without one, the screen is exactly
//     what it was — this tab's own note — and it says why an account would be better and that one
//     is optional (D4). What it may never do is call the tab's note an account's list, or the
//     account's list the tab's;
//   - **what one read means** has four answers that must not collapse into each other. An account
//     that bought nothing is an empty list, and that is an answer. A refused session is a session
//     that ended, said plainly and without the opaque refusal's detail — the API gives one `401`
//     for expired, revoked and never-issued alike, and a page that distinguished them would leak
//     the distinction the route refuses to make. A store that could not answer is our failure,
//     retryable, and never an empty list: "you bought nothing" is a claim a broken read has not
//     earned.
//
// Two prohibitions travel with this module, and its tests hold them: no sentence claims an address
// was verified (that happens at the box office, when a link is spent — S3), and no sentence
// carries the session token, which is a credential that belongs to the cookie and nowhere else.
// The component that draws these states has no runner in this application, so what is pinned here
// is the rule and the words, not the markup.
import type {
  AccountTicketsResponse,
  PublicProblem,
  PublicTicket,
} from "./public-api.ts";

/**
 * The heading block `/tickets` renders, and which list is under it.
 *
 * The headline is the same either way — this is one screen with two answers — and what changes is
 * who the list belongs to, in words a person can check against the list beneath them.
 */
export type TicketsScreen =
  | {
      kind: "account";
      eyebrow: string;
      headline: string;
      description: string;
    }
  | {
      kind: "guest";
      eyebrow: string;
      headline: string;
      description: string;
    };

/**
 * The screen this browser gets, from the one fact that decides it.
 *
 * `hasSession` is whether this request carried a session cookie (lib/session.ts). The guest copy
 * is the older screen's honesty, kept: the list is this tab's, a ticket is opened by its own link,
 * and an account is an offer rather than a requirement. The account copy drops every claim about
 * the tab keeping ids, because on that screen the box office is what remembers them.
 */
export function describeTicketsScreen(hasSession: boolean): TicketsScreen {
  if (hasSession) {
    return {
      kind: "account",
      eyebrow: "Entradas",
      headline: "Mis entradas",
      description:
        "Las entradas que compró esta cuenta, leídas de la taquilla al cargar esta pantalla. La lista es la respuesta de la taquilla, así que es la misma en otro navegador y en otro dispositivo, y este navegador no guarda identificadores de entradas propios. Cada entrada es una butaca de un pedido: ábrela por su propio enlace, que admite a quien lo tenga, con sesión o sin ella.",
    };
  }

  return {
    kind: "guest",
    eyebrow: "Entradas",
    headline: "Mis entradas",
    description:
      "Los identificadores de entrada que esta pestaña guardó de las compras que hizo en ella. No hay ninguna cuenta abierta en este navegador, así que no es una lista que la taquilla guarde para ti: cada entrada se lee de la taquilla al cargar esta pantalla, y una entrada se abre por su propio enlace. Una cuenta guardaría esta lista entre dispositivos y navegadores en lugar de dejarla en esta pestaña, y abrir sesión es opcional: comprar una entrada y abrirla no necesitan cuenta. Si este despliegue envía correo y la compra dejó una dirección, a esa dirección se le envió también un enlace a estas entradas; ese enlace caduca, así que el PDF se sigue descargando desde cada entrada.",
  };
}

/** One account read, as the screen under that heading renders it. */
export type AccountTicketsView =
  /** The account's tickets, in the box office's order. */
  | {
      kind: "loaded";
      tickets: PublicTicket[];
      headline: string;
      message: string;
    }
  /** The account exists and has bought nothing. An answer, not a failure. */
  | { kind: "empty"; headline: string; message: string; nextStep: string }
  /**
   * The box office refused the session this browser holds. One page for every way that happens,
   * carrying nothing of the refusal: the way forward is signing in again.
   */
  | { kind: "ended"; headline: string; message: string; nextStep: string }
  /** Ours: the list could not be read at all. Retryable, and no verdict on the account. */
  | {
      kind: "unavailable";
      headline: string;
      message: string;
      nextStep: string;
      retryable: true;
      /** Which failure it was, so a surface can print a code an operator can act on. */
      problem: PublicProblem;
    };

/**
 * One answer of the account read, turned into the state the screen shows and the words for it.
 *
 * The order is the rule. A list is a list, empty or not — an empty one says the account bought
 * nothing, which is exactly what the route answered instead of a `404`. A `401` is the session
 * that ended. *Everything else* — a `503`, an unreachable API, a body that is not a list — is a
 * failure to ask, which is retryable and says nothing about the account.
 */
export function describeAccountTickets(
  result: AccountTicketsResponse,
): AccountTicketsView {
  if (result.ok) {
    if (result.tickets.length === 0) {
      return {
        kind: "empty",
        headline: "Esta cuenta no ha comprado ninguna entrada",
        message:
          "La taquilla respondió la lista de esta cuenta y está vacía. Eso es una respuesta y no una lectura fallida: aquí no falta nada por saber, y no falta ninguna compra porque algo no se pudiera preguntar.",
        nextStep:
          "Compra una entrada con la sesión abierta y se añadirá a esta lista. Una entrada se sigue abriendo por su propio enlace, y eso no necesita cuenta.",
      };
    }

    return {
      kind: "loaded",
      tickets: result.tickets,
      headline: "Entradas en esta cuenta",
      message:
        result.tickets.length === 1
          ? "1 entrada, en el orden de la taquilla: la más nueva primero. Se leyó de la taquilla al cargar esta página, así que nada aquí es un dato recordado."
          : `${result.tickets.length} entradas, en el orden de la taquilla: la más nueva primero. Cada una se leyó de la taquilla al cargar esta página, así que nada aquí es un dato recordado.`,
    };
  }

  const problem = result.problem;
  if (problem.code === "unauthorized" || problem.status === 401) {
    return {
      kind: "ended",
      headline: "Esta sesión ha terminado",
      // Nothing of the refusal: a status and a code are not what a person reads here, and the API
      // deliberately gives one answer for a session that expired, one that was revoked and one
      // that never existed.
      message:
        "La taquilla rechazó la sesión que guarda este navegador, así que no puede decir qué entradas son tuyas. Una sesión caduca y puede terminar en cualquier momento — también entre esta página y su lectura, que es lo que ha pasado aquí. Nada de una entrada que compraste ha cambiado: una entrada se abre por su propio enlace, y eso no necesita sesión.",
      nextStep:
        "Vuelve a abrir sesión para leer esta lista. Esta página quita de paso la cookie muerta de este navegador, porque una cookie que ya no sirve es peor que ninguna.",
    };
  }

  return {
    kind: "unavailable",
    headline: "No se pudieron leer las entradas de la cuenta",
    // Nothing about the account is known, and the screen says exactly that instead of printing an
    // empty list: "you bought nothing" is a claim this read did not earn.
    message:
      "La taquilla no pudo responder, así que esta página no sabe qué hay en la lista, y no va a mostrar una lista vacía en su lugar. Es un fallo nuestro, no una respuesta sobre tu cuenta.",
    nextStep:
      "Vuelve a preguntar dentro de un momento. Nada de una entrada cambia cuando su lista no se puede leer, y una entrada se abre por su propio enlace de todos modos.",
    retryable: true,
    problem,
  };
}
