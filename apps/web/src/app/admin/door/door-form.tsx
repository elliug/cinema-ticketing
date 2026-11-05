"use client";

// The door's whole interaction: one field, one action, one answer (M4.1 · T8 · S4, S5;
// Marquesina since visual-redesign).
//
// A cashier has a buyer in front of them and a queue behind them, so the screen is a text field
// and a button. There is no list of tickets, no identifier to copy, and no second form: the code
// is pasted, the button is pressed, and the answer appears.
//
// Three rules are structural:
//
//   - **the answer cannot be misread.** It is announced through a live region that exists before
//     the first answer, so a screen reader reads the verdict without the cashier hunting for it;
//     and the verdict is *words* — an admitted ticket, a ticket already used, a code that is not
//     ours, a ticket that cannot be admitted — with the tone only reinforcing them, never
//     carrying the meaning alone;
//   - **nothing is kept.** The code lives in this component's state for the length of one
//     request, is cleared from the field once it has been answered, and is written to no storage
//     of any kind: a door that remembers codes is a copy of every ticket that walks past it. The
//     component holds the last answer and nothing else, so a reload is a blank screen;
//   - **focus is never moved.** Arriving answers appear in place; typing does not re-render the
//     field's identity, and nothing here calls `focus()`.
//
// The verdict itself is not decided here: `describeRedemption` in ./door-outcome.ts owns it, so
// what this component prints is exactly what its tests proved.
//
// The station reads like the artboard's: the code in a tall mono field, one primary action, and
// the answer as a doorman's stamp — a doubled edge, spaced capitals and the word, so the four
// verdicts differ by their letters before they differ by their colour.
import { useState, useTransition } from "react";

import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { RedemptionResponse } from "@/lib/public-api";
import type { DoorAnswer, DoorOutcomeKind, DoorTone } from "./door-outcome";
import { describeRedemption } from "./door-outcome";

/** The one alert tone each verdict is drawn with; the headline is what says which it is. */
const TONE_VARIANT = {
  success: "success",
  warning: "warning",
  error: "destructive",
  info: "default",
} as const;

/** The stamp's tone. It reinforces the word on the stamp; it never stands in for it. */
const STAMP_CLASS: Record<DoorTone, string> = {
  success: "stamp stamp--success",
  warning: "stamp stamp--warning",
  error: "stamp stamp--danger",
  info: "stamp stamp--info",
};

/** The stamp's glyph, decoration only (`aria-hidden` where it is printed). */
const STAMP_GLYPH: Record<DoorOutcomeKind, string> = {
  admitted: "✓",
  already_used: "!",
  not_ours: "✕",
  not_admissible: "✕",
  unavailable: "?",
};

export interface DoorFormProps {
  /** The server action that presents a code; it runs on the server, never here. */
  redeem: (token: string) => Promise<RedemptionResponse>;
}

export function DoorForm({ redeem }: DoorFormProps) {
  const [code, setCode] = useState("");
  const [answer, setAnswer] = useState<DoorAnswer | null>(null);
  const [asking, startAsking] = useTransition();

  const blank = code.trim() === "";

  function present(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const presented = code.trim();
    if (presented === "") {
      return;
    }
    startAsking(async () => {
      const result = await redeem(presented);
      setAnswer(describeRedemption(result));
      // Answered and forgotten: the code is not left in the field for the next person to read.
      setCode("");
    });
  }

  return (
    <Card className="flex flex-col gap-4 p-5">
      <CardHeader className="p-0">
        <h2 id="door-check" className="m-0 text-lg leading-tight font-semibold">
          Comprobar una entrada
        </h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Pega o escribe el código que enseña el comprador y pulsa el botón una sola vez. La
          respuesta sustituye a la anterior, y el código no se guarda.
        </p>
      </CardHeader>

      <form
        className="flex flex-wrap items-end gap-3"
        onSubmit={present}
        aria-labelledby="door-check"
      >
        <div className="flex min-w-72 flex-1 flex-col gap-1.5">
          <Label htmlFor="door-code">Código de la entrada</Label>
          <Input
            id="door-code"
            name="code"
            value={code}
            onChange={(event) => setCode(event.target.value)}
            autoComplete="off"
            spellCheck={false}
            aria-describedby="door-code-hint"
            placeholder="Pega el código de la entrada del comprador"
            // Tall, mono and spaced: a code is read character by character, and `uppercase`
            // only changes how it looks — the value travels exactly as it was typed.
            className="h-14 font-mono text-xl tracking-[0.1em] uppercase"
          />
        </div>
        <Button type="submit" disabled={blank || asking}>
          {asking ? "Comprobando…" : "Comprobar el código"}
        </Button>
      </form>
      <p id="door-code-hint" className="m-0 max-w-prose text-sm text-muted-foreground">
        El servidor comprueba el código y no se guarda nada en este navegador. Una entrada
        impresa es un registro de la venta, nunca el veredicto: lo es la respuesta del servidor.
      </p>

      {/* The region exists before the first answer, so the first answer is announced too. */}
      <div role="status" aria-live="polite" aria-atomic="true" className="flex flex-col gap-2">
        {answer === null ? (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Ningún código se ha comprobado todavía.
          </p>
        ) : (
          <Alert variant={TONE_VARIANT[answer.tone]} className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-3">
              {/* The stamp is the verdict in one or two words, and the headline beside it is
                  the same verdict in a sentence: two words, never a colour, carry it. */}
              <p className={STAMP_CLASS[answer.tone]}>
                <span aria-hidden="true">{STAMP_GLYPH[answer.kind]}</span>
                {answer.stamp}
              </p>
              <h3 className="m-0 text-base leading-tight font-semibold">{answer.headline}</h3>
            </div>
            <p className="m-0 max-w-prose">{answer.message}</p>

            {answer.screening === null ? null : (
              <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
                <dt className="text-muted-foreground">Sesión</dt>
                <dd className="m-0 font-medium break-words">
                  {answer.screening.filmTitle}
                </dd>
                <dt className="text-muted-foreground">Dónde</dt>
                <dd className="m-0 break-words">{answer.screening.venue}</dd>
                <dt className="text-muted-foreground">Empieza</dt>
                <dd className="m-0">
                  <time dateTime={answer.screening.startsAt}>
                    {answer.screening.startsAtLocal}
                  </time>
                </dd>
                {answer.seatLabels.length === 0 ? null : (
                  <>
                    <dt className="text-muted-foreground">Butaca</dt>
                    <dd className="m-0 font-medium">{answer.seatLabels.join(", ")}</dd>
                  </>
                )}
              </dl>
            )}
          </Alert>
        )}
      </div>

      {answer !== null && answer.retryable ? (
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Esta respuesta no es un veredicto sobre la entrada. Vuelve a presentar el mismo
          código dentro de un momento.
        </p>
      ) : null}
    </Card>
  );
}
