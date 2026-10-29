"use client";

// Hold form: the running total, the countdown and every outcome of an attempt.
//
// Presentation only. The running total, the countdown text and the failure
// wording all come from lib/seat-selection.ts, so what this component prints is
// exactly what the tested pure module decided. Two rules are structural:
//
//   - the countdown text and its expired state come from the `countdown` prop,
//     which the panel derives from the server's `expiresAt`, never from a
//     client-side guess;
//   - a failure is rendered from its classification: a taken seat asks for new
//     availability and names the lost seats, an expired window says the window
//     closed, and a temporary outage invites a retry.
import type { HoldAttempt, HoldCountdown, SelectionSummary } from "@/lib/seat-selection";

export interface HoldFormProps {
  summary: SelectionSummary;
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
    <section className="card" aria-labelledby="hold-form">
      <h2 id="hold-form">Your selection</h2>

      <p className="meta" aria-live="polite">
        <strong>{summary.totalLabel}</strong>
        {summary.labels.length > 0 ? ` · ${summary.labels.join(", ")}` : ""}
      </p>

      <div className="actions">
        <button type="button" onClick={onReset} disabled={summary.count === 0}>
          Clear selection
        </button>
        <button type="button" onClick={onSubmit} disabled={!canSubmit}>
          {attempt.status === "submitting" ? "Holding seats…" : "Hold these seats"}
        </button>
      </div>

      {attempt.status === "idle" ? (
        <p className="notice">
          Selecting seats does not reserve them. A hold is only yours once the
          server answers, and it is released automatically when its window ends.
        </p>
      ) : null}

      {countdown !== null ? (
        <div className="feedback" role="status" aria-live="polite">
          <p className="meta">Hold window</p>
          <p>
            <strong>{countdown.displayLabel}</strong>
            {countdown.expired ? "" : ` left of your hold`}
          </p>
          {countdown.notice !== null ? (
            <p className="notice">{countdown.notice}</p>
          ) : null}
          {expired ? (
            <div className="actions">
              <button type="button" onClick={onReset}>
                Select seats again
              </button>
            </div>
          ) : null}
        </div>
      ) : null}

      {attempt.status === "held" && attempt.hold !== null ? (
        <p className="feedback" role="status">
          Seats held. Reservation <code>{attempt.hold.id}</code>.
        </p>
      ) : null}

      {failure !== null ? (
        <section
          className={`problems feedback ${failure.tone === "error" ? "error" : ""}`}
          role="alert"
          aria-labelledby="hold-failure"
        >
          <h2 id="hold-failure">{failure.headline}</h2>
          <p>{failure.message}</p>
          {lostLabels.length > 0 ? (
            <p>
              No longer selected: <code>{lostLabels.join(", ")}</code>
            </p>
          ) : null}
          <p className="notice">
            {failure.retryable
              ? "You can adjust the selection and try again."
              : "Nothing was reserved for this attempt."}
          </p>
        </section>
      ) : null}
    </section>
  );
}
