// The ticket page's "send the link to another address", as one answer turned into words
// (T5 · S1, S2, S3).
//
// The API answers `POST /api/v1/tickets/{ticketId}/email` in five ways, and a person reads
// five different sentences because they mean five different things:
//
//   1. `202 {"sent": true}` — the provider took the message. It is the **only** state that
//      may say a message was sent, and it is reached only from the field the API writes
//      after that provider call;
//   2. `400 invalid_input` — the checkout's own address rule refused the value, and nothing
//      was mailed. The rule is restated in the panel's copy, never tightened here;
//   3. `429 rate_limited` — this order has been mailed three times in the last hour, because
//      a button that emails any address on request is a spam relay. The wait is the API's:
//      `Retry-After` in whole seconds, printed with a readable form beside it, and where the
//      API served no number this module says "in a while" instead of inventing one;
//   4. `503` — this deployment does not send mail. A deployment with no provider must never
//      look like a success (S5), so its own state says so and points at the PDF;
//   5. the opaque `404` that an unknown ticket and a ticket of an unpaid order share. The
//      API refuses both with one body on purpose, so nothing of it is carried into the view:
//      a page that explained which one it was would claim a knowledge the API withheld.
//
// What is *ours* stays apart from all of it: a network the request never reached, a `5xx`, a
// `202` whose body is not the answer. Those are retryable and say nothing about the address,
// the order or the ticket, which is why they are one state with the problem carried beside it.
//
// Two things this module owns because the copy has to stay honest. First, the address is not
// part of the answer at all: it arrives nowhere in this module's input, so no sentence can
// echo it back (the live region, a URL or a log would all be the same mistake). Second, S2's
// consequence is a sentence rather than a footnote — the address was never verified, it is
// stored nowhere on the order, and anyone holding the mailed link can open the tickets,
// because the link is the whole credential.
import type { PublicProblem, PublicResult, PublicTicketEmailSent } from "./public-api.ts";

/**
 * One send attempt, as the panel renders it.
 *
 * Every variant names its state in words, so colour only reinforces it: the headline is the
 * state, and the badge or tone beside it is decoration.
 */
export type TicketEmailView =
  /** The provider took the message. The one state that says a message was sent. */
  | {
      kind: "sent";
      headline: string;
      message: string;
      nextStep: string;
      tone: "success";
    }
  /** The API's own rule refused the address, so nothing left the building. */
  | {
      kind: "refused";
      headline: string;
      message: string;
      nextStep: string;
      tone: "warning";
    }
  /**
   * The order is over its hourly allowance and nothing was sent this time.
   *
   * `retryAfterSeconds` is the `Retry-After` the API served, or `null` when it served none: a
   * page may print a wait the server stated and must not compute one of its own.
   */
  | {
      kind: "limited";
      headline: string;
      message: string;
      nextStep: string;
      tone: "warning";
      retryAfterSeconds: number | null;
    }
  /**
   * This deployment does not send mail: no provider is configured, or the provider could not
   * be reached just now. Both are `503 service_unavailable`, and the sentences below cover
   * both without pretending to tell them apart.
   */
  | {
      kind: "no_mail";
      headline: string;
      message: string;
      nextStep: string;
      tone: "error";
    }
  /** The box office will not mail this ticket, and will not say why it means it. */
  | {
      kind: "unknown";
      headline: string;
      message: string;
      nextStep: string;
      tone: "warning";
    }
  /** The question could not be asked, so nothing about the send is known. */
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

/**
 * One send turned into the state a person sees and the words for it.
 *
 * The order of the checks is the rule, and it follows the API's own cases: a refusal of the
 * address is final and needs a correction, a limit needs the wait it stated, a deployment that
 * cannot send is its own honest state, the opaque `404` is a verdict that is deliberately
 * unreadable, and *everything else* is a failure to ask — including a `200`/`202` whose body is
 * not the answer — which is retryable and is never read as a send.
 */
export function describeTicketEmail(
  result: PublicResult<PublicTicketEmailSent>,
): TicketEmailView {
  if (result.ok) {
    return {
      kind: "sent",
      headline: "The message was sent",
      message:
        "The box office handed the message to its mail provider, and it is the message a purchase email carries: the same subject, the same words and the same signed link to this order's tickets. That link expires, so open it while the mail is still around, and anyone with that link can open these tickets — it is not tied to you, to this browser or to this page. The address you typed was not verified, and it is not stored on the order: it is used for this send and nothing else remembers it.",
      nextStep:
        "If the mail does not arrive, send it again, or keep the PDF from this page.",
      tone: "success",
    };
  }

  const problem = result.error;

  // `400`: the checkout's own rule, refusing the value before anything was mailed.
  if (problem.status === 400 || problem.code === "invalid_input") {
    return {
      kind: "refused",
      headline: "That address was not sent anything",
      message:
        "The box office refused the address, so nothing was mailed. It applies the rule the checkout applies: an address needs a name, an @, and a domain with a dot in it, and it cannot contain spaces.",
      nextStep:
        "Correct the address and send it again. Nothing about this ticket changed, and the address was stored nowhere.",
      tone: "warning",
    };
  }

  // `429`: the anti-relay bound. The wait is the header's number or nothing at all.
  if (problem.status === 429 || problem.code === "rate_limited") {
    const seconds = problem.retryAfterSeconds ?? null;
    return {
      kind: "limited",
      headline: "This order has been mailed as often as it can be for now",
      message:
        "The box office mails one order at most three times an hour, because a button that emails any address on request is a spam relay. Nothing was sent this time, and the ticket itself is unaffected.",
      nextStep:
        seconds === null
          ? "The box office did not say how long the wait is, so this page will not invent a time: try again in a while."
          : `The box office asked to wait ${describeWait(seconds)} before this order can be mailed again. Try again then.`,
      tone: "warning",
      retryAfterSeconds: seconds,
    };
  }

  // `503`: a deployment that cannot send. S5's honest half, and never a success.
  if (problem.status === 503 || problem.code === "service_unavailable") {
    return {
      kind: "no_mail",
      headline: "This deployment does not send mail",
      message:
        "The box office answered that it cannot send mail: either this deployment has no mail provider configured, or the provider could not be reached just now. Nothing was sent, and this page will not say it was. The link is still the one a purchase email carries, and the PDF still downloads from this page.",
      nextStep:
        "If no provider is configured, asking again here will keep failing and the PDF is the way to keep a copy. If the provider was the one that failed, trying again in a moment may work.",
      tone: "error",
    };
  }

  // The opaque `404`: an unknown ticket and an unpaid order, indistinguishable on purpose.
  if (problem.status === 404 || problem.code === "not_found") {
    return {
      kind: "unknown",
      headline: "The box office will not mail this ticket",
      message:
        "It answers an unknown ticket and a ticket of an order that was never paid with the same refusal on purpose, so this page cannot tell you which one this is. Nothing was sent.",
      nextStep:
        "Nothing about the ticket on this page changed: it still opens here, and it is still what the door checks. The PDF still downloads.",
      tone: "warning",
    };
  }

  return {
    kind: "unavailable",
    headline: "The message could not be sent right now",
    message:
      "The box office could not be asked to send it, so this page has no answer about your message. This is our failure, not a refusal: nothing about the ticket or the order changed, and the address was not stored.",
    nextStep:
      "Try again. If it keeps failing, the PDF on this page is the copy to keep.",
    tone: "error",
    retryable: true,
    problem,
  };
}

/**
 * A wait in the two forms a person can use: the server's exact seconds, and a readable size
 * beside them once the number stops being readable.
 *
 * The exact number is never rounded away — it is what the next attempt is answered against —
 * and the readable form is never printed when it would only repeat it: a wait under a minute
 * is its own seconds, not "0 minutes".
 */
function describeWait(seconds: number): string {
  const whole = `${seconds} seconds`;
  if (seconds < 90) {
    return whole;
  }
  if (seconds < 5400) {
    return `${whole} (about ${Math.round(seconds / 60)} minutes)`;
  }
  return `${whole} (about ${Math.round(seconds / 3600)} hours)`;
}
