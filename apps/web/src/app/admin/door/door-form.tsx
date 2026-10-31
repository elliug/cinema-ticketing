"use client";

// The door's whole interaction: one field, one action, one answer (M4.1 · T8 · S4, S5).
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
import { useState, useTransition } from "react";

import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { RedemptionResponse } from "@/lib/public-api";
import type { DoorAnswer } from "./door-outcome";
import { describeRedemption } from "./door-outcome";

/** The one alert tone each verdict is drawn with; the headline is what says which it is. */
const TONE_VARIANT = {
  success: "success",
  warning: "warning",
  error: "destructive",
  info: "default",
} as const;

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
          Check a ticket
        </h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Paste or type the code the buyer shows, then press the button once. The answer
          replaces the last one, and the code is not kept.
        </p>
      </CardHeader>

      <form className="flex flex-wrap items-end gap-3" onSubmit={present} aria-labelledby="door-check">
        <div className="flex min-w-72 flex-1 flex-col gap-1.5">
          <Label htmlFor="door-code">Ticket code</Label>
          <Input
            id="door-code"
            name="code"
            value={code}
            onChange={(event) => setCode(event.target.value)}
            autoComplete="off"
            spellCheck={false}
            aria-describedby="door-code-hint"
            placeholder="Paste the code from the buyer's ticket"
          />
        </div>
        <Button type="submit" disabled={blank || asking}>
          {asking ? "Checking…" : "Admit or refuse"}
        </Button>
      </form>
      <p id="door-code-hint" className="m-0 max-w-prose text-sm text-muted-foreground">
        The code is checked by the server and nothing is stored in this browser. A printed
        ticket is a record of the sale, never the verdict — the server&#39;s answer is.
      </p>

      {/* The region exists before the first answer, so the first answer is announced too. */}
      <div role="status" aria-live="polite" aria-atomic="true" className="flex flex-col gap-2">
        {answer === null ? (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            No code has been checked yet.
          </p>
        ) : (
          <Alert variant={TONE_VARIANT[answer.tone]} className="flex flex-col gap-2">
            <h3 className="m-0 text-base leading-tight font-semibold">{answer.headline}</h3>
            <p className="m-0 max-w-prose">{answer.message}</p>

            {answer.screening === null ? null : (
              <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
                <dt className="text-muted-foreground">Screening</dt>
                <dd className="m-0 font-medium break-words">
                  {answer.screening.filmTitle}
                </dd>
                <dt className="text-muted-foreground">Where</dt>
                <dd className="m-0 break-words">{answer.screening.venue}</dd>
                <dt className="text-muted-foreground">Starts</dt>
                <dd className="m-0">
                  <time dateTime={answer.screening.startsAt}>
                    {answer.screening.startsAtLocal}
                  </time>
                </dd>
                {answer.seatLabels.length === 0 ? null : (
                  <>
                    <dt className="text-muted-foreground">Seat</dt>
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
          This answer is not a verdict on the ticket. Present the same code again in a moment.
        </p>
      ) : null}
    </Card>
  );
}
