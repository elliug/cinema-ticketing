"use client";

// The selection summary: the running amount, the countdown and every outcome of a hold.
//
// Presentation only. The count, the labels and the amount come from
// lib/seat-selection.ts (`selectionSummary`, `priceSummary`), and the countdown and the
// failure wording come from the same module, so what this component prints is exactly
// what the tested rules decided. Three rules are structural:
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
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardHeader } from "@/components/ui/card";
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

  return (
    <Card aria-labelledby="hold-form" className="flex flex-col gap-3 p-5">
      <CardHeader className="p-0">
        {/* An `h2`: the page's outline is the film (h1), then the screening, the map,
            the selection and the way to payment. */}
        <h2 id="hold-form" className="m-0 text-lg leading-tight font-semibold">
          Your selection
        </h2>
      </CardHeader>

      <div className="flex flex-col gap-1">
        <p className="m-0 text-base font-medium">{summary.totalLabel}</p>
        {summary.labels.length > 0 ? (
          <p className="m-0 text-sm text-muted-foreground">
            {summary.labels.join(", ")}
          </p>
        ) : null}
        <p className="m-0 text-sm">
          {priced.lineLabel}
          {priced.count > 0 ? (
            <strong className="ml-1">{priced.amountLabel}</strong>
          ) : null}
        </p>
        <p className="m-0 max-w-prose text-xs text-muted-foreground">
          {priced.note}
        </p>
        {priced.limitNote === null ? null : (
          <p className="m-0 max-w-prose text-sm font-medium text-[var(--color-warning-fg)]">
            {priced.limitNote}
          </p>
        )}
      </div>

      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          variant="outline"
          onClick={onReset}
          disabled={summary.count === 0}
        >
          Clear selection
        </Button>
        <Button type="button" onClick={onSubmit} disabled={!canSubmit}>
          {attempt.status === "submitting" ? "Holding seats…" : "Hold these seats"}
        </Button>
      </div>

      {attempt.status === "idle" ? (
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Selecting seats does not reserve them. A hold is only yours once the server
          answers, and it is released automatically when its window ends.
        </p>
      ) : null}

      {countdown === null ? null : (
        <div className="flex flex-col gap-1">
          <p className="m-0 text-sm text-muted-foreground">Hold window</p>
          <p className="m-0">
            <strong>{countdown.displayLabel}</strong>
            {countdown.expired ? "" : " left of your hold"}
          </p>
          {expired ? (
            // Mounted only when the window closes, so it is announced once instead of
            // every time the clock ticks.
            <p role="status" className="m-0 max-w-prose text-sm">
              {countdown.notice}
            </p>
          ) : (
            <p className="m-0 max-w-prose text-sm text-muted-foreground">
              The window comes from the server. When it ends, the seats are released.
            </p>
          )}
          {expired ? (
            <div className="flex flex-wrap gap-2">
              <Button type="button" variant="outline" onClick={onReset}>
                Select seats again
              </Button>
            </div>
          ) : null}
        </div>
      )}

      {attempt.status === "held" && attempt.hold !== null && !expired ? (
        <Alert variant="success" role="status">
          <AlertDescription>
            Seats held. Reservation <code>{attempt.hold.id}</code>.
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
                No longer selected: <code>{lostLabels.join(", ")}</code>
              </p>
            ) : null}
            <p className="m-0">
              {failure.retryable
                ? "You can adjust the selection and try again."
                : "Nothing was reserved for this attempt."}
            </p>
          </AlertDescription>
        </Alert>
      )}
    </Card>
  );
}
