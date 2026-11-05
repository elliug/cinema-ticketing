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
      headline: "El mensaje se ha enviado",
      message:
        "La taquilla ha entregado el mensaje a su proveedor de correo, y es el mensaje que lleva un correo de compra: el mismo asunto, las mismas palabras y el mismo enlace firmado a las entradas de este pedido. Ese enlace caduca, así que ábrelo mientras el correo siga ahí, y cualquiera que tenga ese enlace puede abrir estas entradas: no está atado a ti, a este navegador ni a esta página. La dirección que escribiste no se ha verificado y no se guarda en el pedido: se usa para este envío y nada más la recuerda.",
      nextStep:
        "Si el correo no llega, envíalo otra vez o guarda el PDF de esta página.",
      tone: "success",
    };
  }

  const problem = result.error;

  // `400`: the checkout's own rule, refusing the value before anything was mailed.
  if (problem.status === 400 || problem.code === "invalid_input") {
    return {
      kind: "refused",
      headline: "A esa dirección no se le ha enviado nada",
      message:
        "La taquilla rechazó la dirección, así que no se envió nada. Aplica la misma regla que el cobro: una dirección necesita un nombre, una @ y un dominio con un punto, y no puede llevar espacios.",
      nextStep:
        "Corrige la dirección y envíala otra vez. Nada de esta entrada ha cambiado, y la dirección no se ha guardado en ninguna parte.",
      tone: "warning",
    };
  }

  // `429`: the anti-relay bound. The wait is the header's number or nothing at all.
  if (problem.status === 429 || problem.code === "rate_limited") {
    const seconds = problem.retryAfterSeconds ?? null;
    return {
      kind: "limited",
      headline: "Este pedido ya se ha enviado tantas veces como se puede por ahora",
      message:
        "La taquilla envía un pedido como mucho tres veces por hora, porque un botón que manda correos a cualquier dirección es un relé de spam. Esta vez no se ha enviado nada, y la entrada no se ve afectada.",
      nextStep:
        seconds === null
          ? "La taquilla no ha dicho cuánto dura la espera, así que esta página no va a inventar un tiempo: vuelve a intentarlo dentro de un rato."
          : `La taquilla pide esperar ${describeWait(seconds)} antes de que este pedido se pueda enviar otra vez. Vuelve a intentarlo entonces.`,
      tone: "warning",
      retryAfterSeconds: seconds,
    };
  }

  // `503`: a deployment that cannot send. S5's honest half, and never a success.
  if (problem.status === 503 || problem.code === "service_unavailable") {
    return {
      kind: "no_mail",
      headline: "Este despliegue no envía correo",
      message:
        "La taquilla ha respondido que no puede enviar correo: o a este despliegue le falta un proveedor de correo configurado, o no se ha podido llegar al proveedor ahora mismo. No se ha enviado nada, y esta página no va a decir que sí. El enlace sigue siendo el que lleva un correo de compra, y el PDF se sigue descargando desde esta página.",
      nextStep:
        "Si no hay proveedor configurado, volver a preguntar aquí seguirá fallando y el PDF es la forma de conservar una copia. Si el que falló fue el proveedor, intentarlo otra vez dentro de un momento puede funcionar.",
      tone: "error",
    };
  }

  // The opaque `404`: an unknown ticket and an unpaid order, indistinguishable on purpose.
  if (problem.status === 404 || problem.code === "not_found") {
    return {
      kind: "unknown",
      headline: "La taquilla no enviará esta entrada",
      message:
        "Responde igual, a propósito, a una entrada desconocida y a una entrada de un pedido que nunca se pagó, así que esta página no puede decirte cuál de las dos es. No se ha enviado nada.",
      nextStep:
        "Nada de la entrada de esta página ha cambiado: se sigue abriendo aquí, y sigue siendo lo que mira el acceso. El PDF se sigue descargando.",
      tone: "warning",
    };
  }

  return {
    kind: "unavailable",
    headline: "No se ha podido enviar el mensaje ahora mismo",
    message:
      "No se pudo pedir a la taquilla que lo enviara, así que esta página no tiene respuesta sobre tu mensaje. Es un fallo nuestro, no un rechazo: nada de la entrada ni del pedido ha cambiado, y la dirección no se ha guardado.",
    nextStep:
      "Inténtalo otra vez. Si sigue fallando, el PDF de esta página es la copia que hay que guardar.",
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
  const whole = `${seconds} segundos`;
  if (seconds < 90) {
    return whole;
  }
  if (seconds < 5400) {
    return `${whole} (unos ${Math.round(seconds / 60)} minutos)`;
  }
  return `${whole} (unas ${Math.round(seconds / 3600)} horas)`;
}
