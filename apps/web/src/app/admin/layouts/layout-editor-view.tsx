"use client";

// Local administrative layout editor view (M3.5 · T8 · S3).
//
// This component is presentation only. Every geometry change is delegated to the pure model
// in lib/layout-editor.ts, and every write is delegated to a server action in ./actions.ts.
// Rows are rendered as independent groups, so a row with one seat and a row with twelve
// seats render side by side without any grid assumption.
//
// Accessibility: every control is labelled, the version's state is a word in a badge as
// well as a tone, and the two issue lists are printed in full rather than reduced to "fix
// these" — the whole point of the editor is that saving and publishing refuse for reasons
// the operator can read.
import { useRouter } from "next/navigation";
import { useId, useState, useTransition } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
        message: `Saved the rows of draft version ${result.value.version}.`,
      });
      router.refresh();
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
        message: `Published version ${result.value.version}. It is immutable now: clone it to change it. A screening can be scheduled against it.`,
      });
      router.refresh();
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

  const disabled = !isDraft || pending;

  return (
    <section aria-labelledby="layout-version" className="flex flex-col gap-5">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center gap-3">
            <h2 id="layout-version" className="m-0 text-lg leading-tight font-semibold">
              Version {layout.version}
            </h2>
            {/* The word is the state; the tone only reinforces it, so the header reads the
                same in greyscale. */}
            <Badge variant={isDraft ? "secondary" : "success"}>{layout.status}</Badge>
          </div>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Layout <code>{layout.layoutId}</code> · {layout.rows.length} row(s) ·{" "}
            {enabledSeats} enabled seat(s)
          </p>
          {isDraft ? null : (
            <p className="m-0 max-w-prose text-sm">
              Version {layout.version} is published and immutable. Clone it to edit a new
              draft version.
            </p>
          )}
        </CardHeader>
      </Card>

      {issues.length > 0 ? (
        <Alert
          variant="warning"
          role="alert"
          aria-labelledby="layout-problems"
          className="flex flex-col gap-2"
        >
          <h2 id="layout-problems" className="m-0 text-lg leading-tight font-semibold">
            Fix these before saving
          </h2>
          <AlertDescription>
            <ul role="list" className="m-0 flex list-disc flex-col gap-1 pl-5">
              {issues.map((issue) => (
                <li key={issue.path}>
                  <code>{layoutIssueDetail(issue)}</code>{" "}
                  <span className="text-muted-foreground">({issue.path})</span>
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      ) : null}

      {layout.rows.length === 0 ? (
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          This version has no rows yet. Add a row to describe the room.
        </p>
      ) : null}

      <ol role="list" className="m-0 flex list-none flex-col gap-4 p-0">
        {layout.rows.map((row, rowIndex) => (
          <li key={rowIndex} className="min-w-0">
            <Card className="flex flex-col gap-4 p-4">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <h3 className="m-0 text-base leading-tight font-semibold">
                  Row {rowIndex + 1}
                </h3>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setLayout(removeRow(layout, rowIndex))}
                  disabled={disabled}
                >
                  Remove row {rowIndex + 1}
                </Button>
              </div>

              <RowFields
                rowLabel={row.label}
                rowPosition={row.position}
                seatCount={row.seats.length}
                disabled={disabled}
                onLabel={(value) => setLayout(setRowLabel(layout, rowIndex, value))}
                onPosition={(value) =>
                  setLayout(setRowPosition(layout, rowIndex, value))
                }
              />

              <ol role="list" className="m-0 flex list-none flex-col gap-3 p-0">
                {row.seats.map((seat, seatIndex) => (
                  <li key={seatIndex} className="min-w-0">
                    <div className="flex flex-wrap items-end gap-3 rounded-md border border-border p-3">
                      <div className="flex w-28 flex-col gap-1.5">
                        <Label htmlFor={`seat-${rowIndex}-${seatIndex}-number`}>
                          Number
                        </Label>
                        <Input
                          id={`seat-${rowIndex}-${seatIndex}-number`}
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
                          disabled={disabled}
                        />
                      </div>
                      <div className="flex w-28 flex-col gap-1.5">
                        <Label htmlFor={`seat-${rowIndex}-${seatIndex}-position`}>
                          Position
                        </Label>
                        <Input
                          id={`seat-${rowIndex}-${seatIndex}-position`}
                          type="number"
                          inputMode="numeric"
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
                          disabled={disabled}
                        />
                      </div>
                      <div className="flex w-40 flex-col gap-1.5">
                        <Label htmlFor={`seat-${rowIndex}-${seatIndex}-kind`}>Kind</Label>
                        <Select
                          value={seat.kind}
                          onValueChange={(value) =>
                            setLayout(
                              setSeatKind(
                                layout,
                                rowIndex,
                                seatIndex,
                                value as SeatKind,
                              ),
                            )
                          }
                          disabled={disabled}
                        >
                          <SelectTrigger id={`seat-${rowIndex}-${seatIndex}-kind`}>
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {SEAT_KINDS.map((kind) => (
                              <SelectItem key={kind} value={kind}>
                                {kind}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </div>
                      <Label className="flex h-10 items-center gap-2 text-sm font-medium">
                        <input
                          type="checkbox"
                          className="size-4"
                          checked={seat.enabled}
                          onChange={() =>
                            setLayout(
                              toggleSeatEnabled(layout, rowIndex, seatIndex),
                            )
                          }
                          disabled={disabled}
                        />
                        Enabled
                      </Label>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() =>
                          setLayout(removeSeat(layout, rowIndex, seatIndex))
                        }
                        disabled={disabled}
                      >
                        Remove seat {seat.number.trim() === "" ? "" : seat.number}
                      </Button>
                    </div>
                  </li>
                ))}
              </ol>

              <div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setLayout(addSeat(layout, rowIndex))}
                  disabled={disabled}
                >
                  Add a seat to row {rowIndex + 1}
                </Button>
              </div>
            </Card>
          </li>
        ))}
      </ol>

      <div className="flex flex-wrap gap-3">
        <Button
          type="button"
          variant="outline"
          onClick={() => setLayout(addRow(layout))}
          disabled={disabled}
        >
          Add row
        </Button>
        <Button type="button" onClick={handleSave} disabled={!canSave}>
          {pending ? "Saving…" : "Save rows"}
        </Button>
        <Button type="button" onClick={handlePublish} disabled={!canPublish}>
          Publish version
        </Button>
        <Button type="button" variant="secondary" onClick={handleClone} disabled={!canClone}>
          Clone into new draft
        </Button>
      </div>

      {publishIssues.length > 0 ? (
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Publishing is blocked: <code>{layoutIssueDetail(publishIssues[0])}</code>
        </p>
      ) : null}

      {feedback ? <EditorFeedback feedback={feedback} /> : null}
    </section>
  );
}

/** One row's label and position, in the same block as its seat count. */
function RowFields({
  rowLabel,
  rowPosition,
  seatCount,
  disabled,
  onLabel,
  onPosition,
}: {
  rowLabel: string;
  rowPosition: number;
  seatCount: number;
  disabled: boolean;
  onLabel: (value: string) => void;
  onPosition: (value: number) => void;
}) {
  const ids = useRowIds();
  return (
    <div className="flex flex-wrap items-end gap-3">
      <div className="flex w-40 flex-col gap-1.5">
        <Label htmlFor={ids.label}>Label</Label>
        <Input
          id={ids.label}
          value={rowLabel}
          onChange={(event) => onLabel(event.target.value)}
          disabled={disabled}
        />
      </div>
      <div className="flex w-28 flex-col gap-1.5">
        <Label htmlFor={ids.position}>Position</Label>
        <Input
          id={ids.position}
          type="number"
          inputMode="numeric"
          value={rowPosition}
          onChange={(event) => onPosition(Number(event.target.value))}
          disabled={disabled}
        />
      </div>
      <p className="m-0 text-sm text-muted-foreground">{seatCount} seat(s)</p>
    </div>
  );
}

function EditorFeedback({ feedback }: { feedback: Feedback }) {
  const error = feedback.kind === "error";
  return (
    <Alert variant={error ? "destructive" : "success"} role={error ? "alert" : "status"}>
      <AlertDescription className="m-0">{feedback.message}</AlertDescription>
    </Alert>
  );
}

/** A stable id set for one row's controls. */
function useRowIds() {
  const base = useId();
  return {
    label: `${base}-row-label`,
    position: `${base}-row-position`,
  };
}
