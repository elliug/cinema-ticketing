"use client";

// Local administrative layout editor view.
//
// This component is presentation only. Every geometry change is delegated to
// the pure model in lib/layout-editor.ts, and every write is delegated to a
// server action in ./actions.ts. Rows are rendered as independent groups, so a
// row with one seat and a row with twelve seats render side by side without any
// grid assumption.
import { useRouter } from "next/navigation";
import { useState, useTransition } from "react";
import { SEAT_KINDS } from "@/lib/admin-api";
import type { SeatKind } from "@/lib/admin-api";
import type { EditorLayout } from "@/lib/layout-editor";
import {
  addRow,
  addSeat,
  enabledSeatCount,
  fromLayout,
  layoutIssueDetail,
  removeRow,
  removeSeat,
  setRowLabel,
  setRowPosition,
  setSeatKind,
  setSeatNumber,
  setSeatPosition,
  toRowPayloads,
  toggleSeatEnabled,
  validateForPublish,
  validateLayout,
} from "@/lib/layout-editor";
import {
  cloneLayoutAction,
  publishLayoutAction,
  saveRowsAction,
} from "./actions";

interface LayoutEditorViewProps {
  initialLayout: EditorLayout;
}

interface Feedback {
  kind: "success" | "error";
  message: string;
}

export function LayoutEditorView({ initialLayout }: LayoutEditorViewProps) {
  const router = useRouter();
  const [layout, setLayout] = useState<EditorLayout>(initialLayout);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [pending, startTransition] = useTransition();

  const issues = validateLayout(layout);
  const publishIssues = validateForPublish(layout);
  const enabledSeats = enabledSeatCount(layout);
  const isDraft = layout.status === "draft";
  const canSave = isDraft && issues.length === 0 && !pending;
  const canPublish = isDraft && publishIssues.length === 0 && !pending;
  const canClone = layout.status === "published" && !pending;

  function handleSave() {
    setFeedback(null);
    startTransition(async () => {
      const result = await saveRowsAction(
        layout.layoutId,
        toRowPayloads(layout),
      );
      if (!result.ok) {
        setFeedback({
          kind: "error",
          message: `${result.error.code}: ${result.error.detail}`,
        });
        return;
      }
      setLayout(fromLayout(result.value));
      setFeedback({
        kind: "success",
        message: `Saved rows for draft version ${result.value.version}.`,
      });
    });
  }

  function handlePublish() {
    setFeedback(null);
    startTransition(async () => {
      const result = await publishLayoutAction(layout.layoutId);
      if (!result.ok) {
        setFeedback({
          kind: "error",
          message: `${result.error.code}: ${result.error.detail}`,
        });
        return;
      }
      setLayout(fromLayout(result.value));
      setFeedback({
        kind: "success",
        message: `Published version ${result.value.version}.`,
      });
    });
  }

  function handleClone() {
    setFeedback(null);
    startTransition(async () => {
      const result = await cloneLayoutAction(layout.layoutId);
      if (!result.ok) {
        setFeedback({
          kind: "error",
          message: `${result.error.code}: ${result.error.detail}`,
        });
        return;
      }
      setFeedback({
        kind: "success",
        message: `Opened draft version ${result.value.version}.`,
      });
      router.push(`/admin/layouts?layoutId=${result.value.id}`);
      router.refresh();
    });
  }

  return (
    <section className="editor" aria-labelledby="layout-title">
      <header className="editor-header">
        <h1 id="layout-title">Layout editor</h1>
        <p className="meta">
          Layout <code>{layout.layoutId}</code> · version{" "}
          <strong>{layout.version}</strong> · status{" "}
          <strong>{layout.status}</strong> · {layout.rows.length} row(s) ·{" "}
          {enabledSeats} enabled seat(s)
        </p>
        {layout.status === "published" ? (
          <p className="notice">
            Version {layout.version} is published and immutable. Clone it to
            edit a new draft version.
          </p>
        ) : null}
      </header>

      {issues.length > 0 ? (
        <section className="problems" aria-labelledby="layout-problems">
          <h2 id="layout-problems">Fix these before saving</h2>
          <ul>
            {issues.map((issue) => (
              <li key={issue.path}>
                <code>{layoutIssueDetail(issue)}</code>{" "}
                <span className="path">({issue.path})</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {layout.rows.length === 0 ? (
        <p className="notice">
          This version has no rows yet. Add a row to describe the room.
        </p>
      ) : null}

      <ol className="rows">
        {layout.rows.map((row, rowIndex) => (
          <li key={rowIndex} className="row">
            <div className="row-header">
              <h2>Row {rowIndex + 1}</h2>
              <button
                type="button"
                onClick={() => setLayout(removeRow(layout, rowIndex))}
                disabled={!isDraft || pending}
              >
                Remove row
              </button>
            </div>

            <div className="fields">
              <label>
                Label
                <input
                  type="text"
                  value={row.label}
                  onChange={(event) =>
                    setLayout(setRowLabel(layout, rowIndex, event.target.value))
                  }
                  disabled={!isDraft || pending}
                />
              </label>
              <label>
                Position
                <input
                  type="number"
                  value={row.position}
                  onChange={(event) =>
                    setLayout(
                      setRowPosition(
                        layout,
                        rowIndex,
                        Number(event.target.value),
                      ),
                    )
                  }
                  disabled={!isDraft || pending}
                />
              </label>
              <span className="meta">{row.seats.length} seat(s)</span>
            </div>

            <ol className="seats">
              {row.seats.map((seat, seatIndex) => (
                <li key={seatIndex} className="seat">
                  <label>
                    Number
                    <input
                      type="text"
                      value={seat.number}
                      onChange={(event) =>
                        setLayout(
                          setSeatNumber(
                            layout,
                            rowIndex,
                            seatIndex,
                            event.target.value,
                          ),
                        )
                      }
                      disabled={!isDraft || pending}
                    />
                  </label>
                  <label>
                    Position
                    <input
                      type="number"
                      value={seat.position}
                      onChange={(event) =>
                        setLayout(
                          setSeatPosition(
                            layout,
                            rowIndex,
                            seatIndex,
                            Number(event.target.value),
                          ),
                        )
                      }
                      disabled={!isDraft || pending}
                    />
                  </label>
                  <label>
                    Kind
                    <select
                      value={seat.kind}
                      onChange={(event) =>
                        setLayout(
                          setSeatKind(
                            layout,
                            rowIndex,
                            seatIndex,
                            event.target.value as SeatKind,
                          ),
                        )
                      }
                      disabled={!isDraft || pending}
                    >
                      {SEAT_KINDS.map((kind) => (
                        <option key={kind} value={kind}>
                          {kind}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="checkbox">
                    <input
                      type="checkbox"
                      checked={seat.enabled}
                      onChange={() =>
                        setLayout(
                          toggleSeatEnabled(layout, rowIndex, seatIndex),
                        )
                      }
                      disabled={!isDraft || pending}
                    />
                    Enabled
                  </label>
                  <button
                    type="button"
                    onClick={() =>
                      setLayout(removeSeat(layout, rowIndex, seatIndex))
                    }
                    disabled={!isDraft || pending}
                  >
                    Remove seat
                  </button>
                </li>
              ))}
            </ol>

            <button
              type="button"
              onClick={() => setLayout(addSeat(layout, rowIndex))}
              disabled={!isDraft || pending}
            >
              Add seat to row {rowIndex + 1}
            </button>
          </li>
        ))}
      </ol>

      <div className="actions">
        <button
          type="button"
          onClick={() => setLayout(addRow(layout))}
          disabled={!isDraft || pending}
        >
          Add row
        </button>
        <button type="button" onClick={handleSave} disabled={!canSave}>
          Save rows
        </button>
        <button type="button" onClick={handlePublish} disabled={!canPublish}>
          Publish version
        </button>
        <button type="button" onClick={handleClone} disabled={!canClone}>
          Clone into new draft
        </button>
      </div>

      {publishIssues.length > 0 ? (
        <p className="notice">
          Publishing is blocked:{" "}
          <code>{layoutIssueDetail(publishIssues[0])}</code>
        </p>
      ) : null}

      {feedback ? (
        <p
          className={feedback.kind === "error" ? "feedback error" : "feedback"}
          role={feedback.kind === "error" ? "alert" : "status"}
        >
          {feedback.message}
        </p>
      ) : null}
    </section>
  );
}
