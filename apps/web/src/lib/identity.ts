// Signing in, as answers turned into states and words (identity-login T6 · S1, S2, S3).
//
// Two screens have a rule worth testing and no layout to test it with, so the rule lives here and
// the pages render it:
//
//   - **the request screen**. Its whole honesty is that one confirmation sentence comes back
//     whether a link was mailed, the hourly allowance refused it, or the counter that keeps that
//     allowance could not be read. `POST /identity/links` answers all three with the same `202`,
//     so a page that told them apart would be the oracle the route refuses to be (D1, S2, S6);
//   - **the page the mailed link opens**. Its one opaque `401` means spent, expired *or* never
//     valid, deliberately: asking whether a credential existed is not a question the box office
//     answers (S1, S2). That state therefore carries nothing of the refusal into its words, and an
//     outage stays a different state — a bad minute must never read as a broken link.
//
// Two sentences are worth stating because they are what the product promises and what the code
// must not drift from:
//
//   - a link expires in **minutes** and works **once**;
//   - anyone with access to that mailbox can sign in with it. A shared inbox is a shared account,
//     and hiding it would not make it false.
//
// Nothing here claims an address was verified or that a message was sent. The request screen
// cannot know either; the page behind the link does not need to. The only place verification
// becomes true is the box office, when the link is spent (S3).
import type {
  PublicProblem,
  PublicResult,
  PublicSignInLinkRequested,
  PublicSignInSession,
} from "./public-api.ts";

/** Where a sign-in link is asked for. One path, because two spellings would drift. */
export const SIGN_IN_PATH = "/sign-in";

/** Where a browser that has just signed in is sent. The list is the account's own (D3). */
export const TICKETS_PATH = "/tickets";

/**
 * The one confirmation the request screen shows, in the same words for all three outcomes.
 *
 * Conditional on purpose: "if that address can receive mail" is exactly as much as the box office
 * said. A page that wrote "we sent you a link" would be confirming that an account exists and that
 * a provider accepted a message, and neither is known here.
 */
export const SIGN_IN_CONFIRMATION =
  "If that address can receive mail, a link is on its way.";

/**
 * What the request screen says before anything is typed, in three sentences.
 *
 * Exported rather than written in JSX so the promise and the code that keeps it are in one place:
 * minutes and one use (S2), a shared mailbox is a shared account (D1's own limit), and one answer
 * whether or not an account exists (D1, S6).
 */
export const SIGN_IN_PROMISES: readonly string[] = [
  "We email a link that expires in minutes and works once.",
  "Anyone with access to that mailbox can sign in with it, so a shared inbox is a shared account.",
  "If the address has no account, nothing is revealed: the same answer comes back either way.",
];

// ── The request screen ──────────────────────────────────────────────────────

/** One answer of the link request, as the request screen shows it. */
export type SignInRequestState =
  /** Nothing asked yet: the form, and its standing sentences. */
  | { kind: "idle" }
  /** The request is in flight. The button is disabled and says so, and nothing is claimed yet. */
  | { kind: "sending" }
  /** The one acceptance. Identical for issued, throttled and unreadable counter. */
  | { kind: "confirmed"; headline: string; message: string; nextStep: string }
  /** The API's own rule refused the value, and its own sentence says why. */
  | { kind: "refused"; headline: string; message: string; nextStep: string }
  /** Ours: the request could not be answered, and it is retryable. */
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
 * One link request, turned into the state the screen shows and the words for it.
 *
 * `sending` wins over any answer already in hand, because a second attempt is a second request and
 * showing the previous answer for it would be showing a fact about a request nobody is making any
 * more. Nothing about the address travels into any of these states: the confirmation cannot name
 * it, and the refusals carry only what the box office said.
 */
export function describeSignInRequest(
  result: PublicResult<PublicSignInLinkRequested> | null,
  sending = false,
): SignInRequestState {
  if (sending) {
    return { kind: "sending" };
  }
  if (result === null) {
    return { kind: "idle" };
  }

  if (result.ok) {
    return {
      kind: "confirmed",
      headline: SIGN_IN_CONFIRMATION,
      message:
        "The box office took the request. It answers the same way whether that address has an account, whether it had already used up its links for this hour, and whether the counter that keeps track could be read at all — so this page cannot say which of those happened, and it does not guess.",
      nextStep:
        "Open the message in the next few minutes and follow the link: it expires quickly and works once. If nothing arrives, ask again — and check the spam folder before assuming anything.",
    };
  }

  const problem = result.error;
  if (problem.code === "invalid_input" || problem.status === 400) {
    return {
      kind: "refused",
      headline: "That address was not accepted",
      // The API's own sentence, verbatim. Its rule refused the value; a second wording of that
      // rule here could only disagree with the rule that actually decides.
      message: problem.detail,
      nextStep: "Correct the address and send it again.",
    };
  }

  return {
    kind: "unavailable",
    headline: "The link could not be sent",
    // Our failure, said as ours. Nothing here is a verdict on the address, and nothing is a claim
    // that no account exists — the box office never got as far as looking.
    message:
      "The box office could not take the request, so no link was sent and nothing was decided about that address. This is our failure, not an answer about your mailbox or about whether an account exists.",
    nextStep:
      "Send it again. If it keeps failing, this deployment's mail side is down, and signing in will not work until it is back.",
    retryable: true,
    problem,
  };
}

// ── The page the mailed link opens ──────────────────────────────────────────

/** One answer of the link redemption, as the page behind the link shows it. */
export type SignInLinkView =
  /** The token has not been spent yet. */
  | { kind: "checking" }
  /**
   * The link was spent and a session exists. It carries only when that session ends: the token
   * itself is a credential and belongs to the cookie, never to a view a surface renders.
   */
  | { kind: "opened"; expiresAt: string }
  /**
   * The box office's one opaque refusal. It carries **nothing** of the problem, and that is the
   * point: spent, expired and never valid are one state, verbatim, because the API made them one.
   */
  | { kind: "unusable"; headline: string; message: string; nextStep: string }
  /** The link could not be checked at all. Ours, retryable, and no verdict on the link. */
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
 * One redemption, turned into the state the page shows and the words for it.
 *
 * The order is the rule. An answer that stands is the session; a `401` is the opaque refusal; and
 * *everything else* — a `503`, an unreachable API, a body that is not the answer — is a failure to
 * ask, which is retryable and says nothing about the link.
 */
export function describeSignInLink(
  result: PublicResult<PublicSignInSession> | null,
): SignInLinkView {
  if (result === null) {
    return { kind: "checking" };
  }
  if (result.ok) {
    return { kind: "opened", expiresAt: result.value.expiresAt };
  }

  const problem = result.error;
  if (problem.code === "unauthorized" || problem.status === 401) {
    return {
      kind: "unusable",
      headline: "This link cannot be used",
      message:
        "A sign-in link expires in minutes and works once. The box office gives one answer for a link that was already used, one that aged out, and one that was never real — so this page cannot tell you which of those this is, and it will not guess. That is deliberate: the answer to \"did this link ever exist\" is not one this system gives out.",
      nextStep:
        "Ask for a new link at Sign in. If you signed in a moment ago, your list is under My tickets — and if you reloaded this page after signing in, this is the spent link answering, not a problem with your account.",
    };
  }

  return {
    kind: "unavailable",
    headline: "This link could not be checked",
    // Nothing about the link is known, and the page says exactly that instead of borrowing a
    // verdict: an outage must never look like an expired mail.
    message:
      "The box office could not spend this link, so nothing about it is known — not whether it is spent, not whether it expired. This is our failure and not a verdict on the link.",
    nextStep:
      "Open the link from your message again in a moment. If it keeps failing, this deployment is having a bad minute; ask for a new link once it is back.",
    retryable: true,
    problem,
  };
}
