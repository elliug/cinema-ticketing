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
      eyebrow: "Tickets · M3.5",
      headline: "My tickets",
      description:
        "The tickets this account bought, read from the box office when this screen loads. The list is the box office's answer, so it is the same in another browser and on another device, and this browser keeps no ticket ids of its own. Each entry is one ticket of an order: open it by its own link, which admits whoever holds it — signing in or not.",
    };
  }

  return {
    kind: "guest",
    eyebrow: "Tickets · M3.5",
    headline: "My tickets",
    description:
      "The ticket ids this browser tab kept from the purchases it made in it. No account is signed in on this browser, so this is not a list the box office keeps for you: each entry is read from the box office when this screen loads, and a ticket is opened by its own link. An account would keep this list across devices and browsers instead of leaving it in this tab, and signing in is optional — buying a ticket and opening one need no account. If this deployment sends mail and the purchase left an address, a link to these tickets was mailed to it as well — that link expires, so the PDF is still downloaded from each ticket.",
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
        headline: "This account has bought no ticket",
        message:
          "The box office answered this account's list and it is empty. That is an answer, not a failed read: nothing here is unknown, and no purchase is missing because something could not be asked.",
        nextStep:
          "Buy a ticket while signed in and it is added to this list. A ticket is still opened by its own link, and that needs no account.",
      };
    }

    return {
      kind: "loaded",
      tickets: result.tickets,
      headline: "Tickets on this account",
      message:
        result.tickets.length === 1
          ? "1 ticket, in the box office's own order — newest first. It was read from the box office when this page loaded, so nothing here is remembered detail."
          : `${result.tickets.length} tickets, in the box office's own order — newest first. Each one was read from the box office when this page loaded, so nothing here is remembered detail.`,
    };
  }

  const problem = result.problem;
  if (problem.code === "unauthorized" || problem.status === 401) {
    return {
      kind: "ended",
      headline: "This session has ended",
      // Nothing of the refusal: a status and a code are not what a person reads here, and the API
      // deliberately gives one answer for a session that expired, one that was revoked and one
      // that never existed.
      message:
        "The box office refused the session this browser holds, so it cannot say which tickets are yours. A session expires, and it can end at any moment — including between this page and its read, which is what happened here. Nothing about a ticket you bought has changed: a ticket is opened by its own link, and that does not need a session.",
      nextStep:
        "Sign in again to read this list. This page removes the dead cookie from this browser on the way, because a cookie that no longer works is worse than none.",
    };
  }

  return {
    kind: "unavailable",
    headline: "The account's tickets could not be read",
    // Nothing about the account is known, and the screen says exactly that instead of printing an
    // empty list: "you bought nothing" is a claim this read did not earn.
    message:
      "The box office could not answer, so this page does not know what is on the list — and it will not show an empty list in its place. This is our failure, not an answer about your account.",
    nextStep:
      "Ask again in a moment. Nothing about a ticket changes when its list cannot be read, and a ticket is opened by its own link either way.",
    retryable: true,
    problem,
  };
}
