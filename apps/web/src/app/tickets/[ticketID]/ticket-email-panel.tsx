"use client";

// "Send the link to another address": one field, one button, one action (T5 · S1, S2, S3).
//
// This is the ticket page's only write. It mails the address typed here the message a purchase
// email carries — the same subject, the same words and the same **signed, expiring** link to
// this order's tickets — and it does it through a server action, so the browser never learns
// where the API lives (`payment-panel.tsx` does the same for the payment).
//
// Five rules this panel owes a person, in the order they matter:
//
//   1. the address is for **this send only**: it is not stored on the order, it is not kept in
//      this browser, and the field is emptied once the box office confirms the message left;
//   2. nothing here has been sent until the API says so — the panel prints "sent" in exactly
//      one state, the one reached from `202 {"sent": true}`;
//   3. the link expires, so the mail is worth opening while it is around;
//   4. anyone with that link can open the tickets, because the link is the whole credential;
//   5. sending is bounded — three times per order per hour — because a button that emails any
//      address on request is a spam relay. The wait printed after a refusal is the API's own
//      `Retry-After`, never a number this page invented.
//
// The rule that turns an answer into words is lib/ticket-email.ts, so what a refusal means is
// tested without a browser and cannot drift from what this file renders. The refusal of the
// *address* is deliberately the API's call: `type="email"` and the browser's own validation
// are hints, and the one authority on what an address is is the box office's — the same rule
// the checkout applies.
//
// Accessibility: one labelled field, one focus-ringed button, and one polite live region that
// announces the outcome once. The address never appears in that region — or in a URL, or in a
// log — and the failure states leave the button usable, because a second attempt is the only
// thing a person can do about most of them.
import { useCallback, useState } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { PublicProblem, TicketEmailResponse } from "@/lib/public-api";
import type { TicketEmailView } from "@/lib/ticket-email";
import { describeTicketEmail } from "@/lib/ticket-email";

/**
 * The failure of a send this deployment never managed to ask the box office about.
 *
 * A server action can fail before the API is reached — the browser's own request to this
 * application did not complete — and that is the same retryable `network_error` an unreachable
 * API is: our failure, not a refusal by anybody. It is spelled here so the panel does not
 * `catch` into an empty string, and it says nothing about the address or the ticket.
 */
const UNREACHED_PROBLEM: PublicProblem = {
  type: "https://cinema.local/problems/network",
  title: "Error de red",
  status: 0,
  code: "network_error",
  detail: "la petición a la taquilla no llegó a completarse",
};

export interface TicketEmailPanelProps {
  /** The ticket whose order is mailed; the API reads the order through this id. */
  ticketId: string;
  /**
   * Server action: `POST /api/v1/tickets/{id}/email` with this address.
   *
   * It is a prop rather than an import for the same reason the list's `readTicket` is: the API
   * is reached in this application's process, and only plain data crosses back here.
   */
  sendTicketEmail: (ticketId: string, email: string) => Promise<TicketEmailResponse>;
}

/** The panel's own state: nothing asked yet, one send in flight, or the answer to it. */
type SendState =
  | { kind: "idle" }
  | { kind: "sending" }
  | { kind: "answer"; view: TicketEmailView };

export function TicketEmailPanel({ ticketId, sendTicketEmail }: TicketEmailPanelProps) {
  // The address as typed. It is read (trimmed) when the button is pressed, never while a
  // person is still typing in it, and it is dropped the moment the send is confirmed.
  const [address, setAddress] = useState("");
  const [state, setState] = useState<SendState>({ kind: "idle" });

  const sending = state.kind === "sending";
  const answer = state.kind === "answer" ? state.view : null;
  const refusedAddress = answer !== null && answer.kind === "refused";

  const handleSend = useCallback(async () => {
    const to = address.trim();
    if (to === "") {
      // The button is disabled in this state; the guard is here because a control that can
      // only fail is not an action, and this panel will not ask the box office nothing.
      return;
    }

    setState({ kind: "sending" });

    let response: TicketEmailResponse;
    try {
      response = await sendTicketEmail(ticketId, to);
    } catch {
      // The action itself did not complete. It is the retryable failure above, and it must
      // leave the button usable: a panel stuck on "sending" would claim a send it cannot see.
      setState({
        kind: "answer",
        view: describeTicketEmail({ ok: false, error: UNREACHED_PROBLEM }),
      });
      return;
    }

    const view = describeTicketEmail(
      response.ok
        ? { ok: true, value: response.email }
        : { ok: false, error: response.problem },
    );
    setState({ kind: "answer", view });

    // The address was for this one send: once the box office confirms the message left, the
    // field is emptied so this browser keeps no copy of it in a form value. A refusal keeps
    // what was typed, because correcting it is the next step.
    if (view.kind === "sent") {
      setAddress("");
    }
  }, [address, sendTicketEmail, ticketId]);

  const announcement =
    state.kind === "sending"
      ? "Pidiendo a la taquilla que envíe el enlace…"
      : answer === null
        ? "Todavía no se ha enviado nada desde esta página."
        : `${answer.headline}. ${answer.message}`;

  return (
    <Card aria-labelledby="ticket-email">
      <CardHeader>
        <h2 id="ticket-email" className="m-0 text-lg leading-tight font-semibold">
          Enviar el enlace a otra dirección
        </h2>
        <CardDescription>
          Esto envía el mensaje que lleva un correo de compra: el mismo asunto, las mismas
          palabras y el mismo enlace firmado a las entradas de este pedido. La dirección de
          abajo se usa solo para este envío: nada la guarda en el pedido, y este navegador no
          la conserva.
        </CardDescription>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="ticket-email-address">
            Correo <span className="text-muted-foreground">(una dirección, esta vez)</span>
          </Label>
          <Input
            id="ticket-email-address"
            name="email"
            type="email"
            autoComplete="email"
            value={address}
            onChange={(event) => setAddress(event.target.value)}
            disabled={sending}
            aria-invalid={refusedAddress ? true : undefined}
            aria-describedby="ticket-email-help"
            className="max-w-md"
          />
          {/* What the action actually does, in the repo's voice: where the mail goes, that the
              link expires, that the link is the credential, that nothing verified or stored
              the address, and that the sending is bounded. Nothing here promises a message
              already sent, because nothing has been sent until the answer below says so. */}
          <ul
            id="ticket-email-help"
            className="m-0 flex max-w-prose list-disc flex-col gap-1 pl-5 text-sm text-muted-foreground"
          >
            <li>
              El mensaje lleva el mismo enlace firmado que un correo de compra, y abre las
              entradas de este pedido.
            </li>
            <li>El enlace caduca, así que ábrelo mientras el correo siga ahí.</li>
            <li>
              Cualquiera que tenga ese enlace puede abrir las entradas: no está atado a ti, a
              este navegador ni a esta página.
            </li>
            <li>
              La dirección no se verifica — nada aquí comprueba que sea de quien la escribió — y
              no se guarda en el pedido.
            </li>
            <li>
              El envío está limitado a tres veces por pedido y por hora, porque un botón que
              manda correos a cualquier dirección es un relé de spam.
            </li>
            <li>
              Un despliegue sin proveedor de correo responde que no puede enviar, y esta página
              lo dice en lugar de afirmar que salió un mensaje.
            </li>
          </ul>
        </div>

        {/* The one polite live region of this panel. It announces the outcome once per
            attempt, and it never carries the address: a live region is page text, and the
            address was given for one send. */}
        <p role="status" className="m-0 max-w-prose text-sm font-medium">
          {announcement}
        </p>

        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            onClick={handleSend}
            disabled={sending || address.trim() === ""}
            aria-busy={sending}
          >
            {sending ? "Enviando…" : "Enviar el enlace"}
          </Button>
        </div>

        {answer === null ? null : <SendOutcome view={answer} />}
      </CardContent>
    </Card>
  );
}

/**
 * One answer, with the words the module decided and nothing of its own.
 *
 * The block is not a live region: the sentence above already announced it, and a second region
 * would say everything twice. The tone reinforces the state; the headline is the state.
 */
function SendOutcome({ view }: { view: TicketEmailView }) {
  return (
    <Alert variant={alertVariant(view)} aria-labelledby="ticket-email-outcome">
      <h3 id="ticket-email-outcome" className="m-0 text-base font-semibold">
        {view.headline}
      </h3>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">{view.message}</p>
        <p className="m-0">{view.nextStep}</p>
      </AlertDescription>
    </Alert>
  );
}

/** The `Alert` tone for one answer; the words inside carry the state. */
function alertVariant(view: TicketEmailView) {
  switch (view.tone) {
    case "success":
      return "success" as const;
    case "warning":
      return "warning" as const;
    default:
      return "destructive" as const;
  }
}
