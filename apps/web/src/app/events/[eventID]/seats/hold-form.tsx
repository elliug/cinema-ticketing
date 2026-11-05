"use client";

// The receipt of the selection: the running amount, the deadline and every outcome of a
// hold.
//
// Presentation only. The count, the labels and the amount come from
// lib/seat-selection.ts (`selectionSummary`, `priceSummary`), the rows and the money text
// from lib/receipt.ts (`seatReceipt`, `describeHoldDeadline`), and the countdown and the
// failure wording come from the same modules, so what this component prints is exactly what
// the tested rules decided. Three rules are structural:
//
//   - the amount is a summary, and the sentence that says so travels with it: the order
//     the API creates is what gets paid, and nothing here may read as a promise about
//     the charge (S8);
//   - the countdown text comes from the `countdown` prop, which the panel derives from
//     the server's `expiresAt`, never from a client-side guess. It is deliberately not
//     a live region: it changes every second, and a screen reader announcing a clock
//     would make the page unusable. Only the expiry is announced, once;
//   - a failure is rendered from its classification: a taken seat asks for new
//     availability and names the lost seats, an expired window says the window closed,
//     and a temporary outage invites a retry.
//
// The receipt itself is the shared `SummaryPanel`, so the seat plan and the checkout print
// money with one vocabulary (visual-redesign · T5). The action stays a real `Button` with its
// own focus ring; the panel only decides where it sits.
import { SummaryPanel } from "@/components/summary-panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { describeHoldDeadline, seatReceipt } from "@/lib/receipt";
import type {
  HoldAttempt,
  HoldCountdown,
  PriceSummary,
  SelectionSummary,
} from "@/lib/seat-selection";

export interface HoldFormProps {
  summary: SelectionSummary;
  /** The same seats priced at the screening's price per seat. */
  priced: PriceSummary;
  countdown: HoldCountdown | null;
  attempt: HoldAttempt;
  /** Seats this attempt lost, already named by the pure module. */
  lostLabels: string[];
  canSubmit: boolean;
  onSubmit: () => void;
  onReset: () => void;
}

export function HoldForm({
  summary,
  priced,
  countdown,
  attempt,
  lostLabels,
  canSubmit,
  onSubmit,
  onReset,
}: HoldFormProps) {
  const expired = countdown?.expired === true;
  const failure = attempt.failure;
  const receipt = seatReceipt({
    seatLabels: summary.labels,
    count: summary.count,
    amountCents: priced.amountCents,
    amountDisplay: null,
  });
  const deadline = describeHoldDeadline(countdown);

  return (
    <div className="flex flex-col gap-3">
      <SummaryPanel
        title="Tu selección"
        lines={receipt.lines}
        total={receipt.total}
        {...(deadline === null ? {} : { deadline })}
        note={priced.note}
      >
        {priced.limitNote === null ? null : (
          <p className="m-0 max-w-prose text-sm font-medium text-[var(--color-warning-fg)]">
            {priced.limitNote}
          </p>
        )}

        <Button type="button" onClick={onSubmit} disabled={!canSubmit}>
          {attempt.status === "submitting"
            ? "Reservando…"
            : `Reservar ${summary.count === 1 ? "1 butaca" : `${summary.count} butacas`}`}
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={onReset}
          disabled={summary.count === 0}
        >
          Quitar la selección
        </Button>

        {attempt.status === "idle" ? (
          <p className="m-0 max-w-prose text-xs text-muted-foreground">
            Elegir butacas no las reserva. La reserva es tuya cuando el servidor responde, y
            se libera sola al terminar su plazo.
          </p>
        ) : null}
      </SummaryPanel>

      {countdown === null ? null : expired ? (
        <div className="flex flex-col gap-2">
          {/* Mounted only when the window closes, so it is announced once instead of every
              time the clock ticks. */}
          <p role="status" className="m-0 max-w-prose text-sm">
            {countdown.notice}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" onClick={onReset}>
              Elegir butacas otra vez
            </Button>
          </div>
        </div>
      ) : (
        <p className="m-0 max-w-prose text-xs text-muted-foreground">
          El plazo lo marca el servidor. Cuando termina, las butacas se liberan.
        </p>
      )}

      {attempt.status === "held" && attempt.hold !== null && !expired ? (
        <Alert variant="success" role="status">
          <AlertDescription>
            Butacas reservadas. Reserva <code>{attempt.hold.id}</code>.
          </AlertDescription>
        </Alert>
      ) : null}

      {failure === null ? null : (
        <Alert
          variant={failure.tone === "error" ? "destructive" : "warning"}
          role="alert"
          aria-labelledby="hold-failure"
          className="flex flex-col gap-1"
        >
          <h3 id="hold-failure" className="m-0 text-base font-semibold">
            {failure.headline}
          </h3>
          <AlertDescription className="flex flex-col gap-1">
            <p className="m-0">{failure.message}</p>
            {lostLabels.length > 0 ? (
              <p className="m-0">
                Ya no están seleccionadas: <code>{lostLabels.join(", ")}</code>
              </p>
            ) : null}
            <p className="m-0">
              {failure.retryable
                ? "Puedes ajustar la selección y volver a intentarlo."
                : "No se reservó nada en este intento."}
            </p>
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}
