// Pure layout editor model.
//
// A layout is a list of rows, and each row owns its own list of seats. Rows
// routinely hold different seat counts, so no function here assumes a
// rectangular grid, a shared seat count, or contiguous numbering.
//
// Every function is pure: the input layout is never mutated and a new layout is
// returned. Validation mirrors the backend rules in apps/api/internal/venue,
// and the issue messages reuse the backend wording so the UI can surface the
// same detail the API would return.
import type {
  AdminLayout,
  LayoutStatus,
  RowPayload,
  SeatKind,
  SeatPayload,
} from "./admin-api.ts";
import { SEAT_KINDS } from "./admin-api.ts";

/** One editable seat. Identifiers are absent until the backend assigns them. */
export interface EditorSeat {
  id?: string;
  number: string;
  position: number;
  kind: SeatKind;
  enabled: boolean;
}

/** One editable row; its seats are independent from every other row. */
export interface EditorRow {
  id?: string;
  label: string;
  position: number;
  seats: EditorSeat[];
}

/** The editor state for one layout version. */
export interface EditorLayout {
  layoutId: string;
  version: number;
  status: LayoutStatus;
  rows: EditorRow[];
}

/** One rejected rule, addressed to the exact field that broke it. */
export interface LayoutIssue {
  /** Location inside the edited layout, for example `rows[1].seats[0].number`. */
  path: string;
  /** Backend field name, for example `seat.number`. */
  field: string;
  /** Backend-compatible message for that field. */
  message: string;
}

/**
 * Renders an issue the way the API renders a problem detail, so a caller can
 * compare local validation with the server's answer.
 */
export function layoutIssueDetail(issue: LayoutIssue): string {
  return `${issue.field}: ${issue.message}`;
}

/**
 * The word the editor prints for one stored seat kind.
 *
 * The value is the column's (`standard`, `accessible`, `companion`) and it is what the write
 * sends; this is what a person reads in the select. A kind this build does not know is printed
 * as served rather than blanked, for the same reason an unknown state is: a seat nobody can
 * name is a seat nobody dares edit.
 */
export function seatKindLabel(kind: SeatKind): string {
  switch (kind) {
    case "standard":
      return "estándar";
    case "accessible":
      return "accesible";
    case "companion":
      return "acompañante";
    default:
      return kind;
  }
}

/** Converts an API layout into editor state, preserving row seat counts. */
export function fromLayout(layout: AdminLayout): EditorLayout {
  const rows = Array.isArray(layout.rows) ? layout.rows : [];
  return {
    layoutId: layout.id,
    version: layout.version,
    status: layout.status,
    rows: rows.map((row) => ({
      ...optionalId(row.id),
      label: row.label,
      position: row.position,
      seats: row.seats.map((seat) => ({
        ...optionalId(seat.id),
        number: seat.number,
        position: seat.position,
        kind: seat.kind,
        enabled: seat.enabled,
      })),
    })),
  };
}

/** Converts editor state into the payload PUT /layouts/{id}/rows accepts. */
export function toRowPayloads(layout: EditorLayout): RowPayload[] {
  return layout.rows.map((row) => ({
    ...optionalId(row.id),
    label: row.label,
    position: row.position,
    seats: row.seats.map((seat): SeatPayload => ({
      ...optionalId(seat.id),
      number: seat.number,
      position: seat.position,
      kind: seat.kind,
      enabled: seat.enabled,
    })),
  }));
}

/** Counts seats that can still be sold, exactly like the backend does. */
export function enabledSeatCount(layout: EditorLayout): number {
  let total = 0;
  for (const row of layout.rows) {
    for (const seat of row.seats) {
      if (seat.enabled) {
        total += 1;
      }
    }
  }
  return total;
}

/** Appends a row with a free label, a free position, and one enabled seat. */
export function addRow(layout: EditorLayout): EditorLayout {
  const label = nextRowLabel(layout);
  const position =
    layout.rows.reduce((max, row) => Math.max(max, row.position), 0) + 1;
  return {
    ...layout,
    rows: [
      ...layout.rows,
      { label, position, seats: [newSeat([], 1)] },
    ],
  };
}

/** Removes one row; the remaining rows keep their own seat collections. */
export function removeRow(
  layout: EditorLayout,
  rowIndex: number,
): EditorLayout {
  if (!hasIndex(layout.rows, rowIndex)) {
    return layout;
  }
  return { ...layout, rows: layout.rows.filter((_, index) => index !== rowIndex) };
}

/** Renames one row. The label may be invalid; validation reports it. */
export function setRowLabel(
  layout: EditorLayout,
  rowIndex: number,
  label: string,
): EditorLayout {
  return updateRow(layout, rowIndex, (row) => ({ ...row, label }));
}

/** Changes one row's ordering position; uniqueness is validated separately. */
export function setRowPosition(
  layout: EditorLayout,
  rowIndex: number,
  position: number,
): EditorLayout {
  return updateRow(layout, rowIndex, (row) => ({ ...row, position }));
}

/** Appends one seat to a single row, leaving every other row untouched. */
export function addSeat(
  layout: EditorLayout,
  rowIndex: number,
): EditorLayout {
  return updateRow(layout, rowIndex, (row) => {
    const position =
      row.seats.reduce((max, seat) => Math.max(max, seat.position), 0) + 1;
    return { ...row, seats: [...row.seats, newSeat(row.seats, position)] };
  });
}

/** Removes one seat from one row without renumbering the seats that remain. */
export function removeSeat(
  layout: EditorLayout,
  rowIndex: number,
  seatIndex: number,
): EditorLayout {
  return updateRow(layout, rowIndex, (row) => {
    if (!hasIndex(row.seats, seatIndex)) {
      return row;
    }
    return {
      ...row,
      seats: row.seats.filter((_, index) => index !== seatIndex),
    };
  });
}

/** Changes one seat's number; duplicates inside the row are validated. */
export function setSeatNumber(
  layout: EditorLayout,
  rowIndex: number,
  seatIndex: number,
  number: string,
): EditorLayout {
  return updateSeat(layout, rowIndex, seatIndex, (seat) => ({ ...seat, number }));
}

/** Changes one seat's position; duplicates inside the row are validated. */
export function setSeatPosition(
  layout: EditorLayout,
  rowIndex: number,
  seatIndex: number,
  position: number,
): EditorLayout {
  return updateSeat(layout, rowIndex, seatIndex, (seat) => ({
    ...seat,
    position,
  }));
}

/** Changes one seat's kind. The supported kinds mirror the backend. */
export function setSeatKind(
  layout: EditorLayout,
  rowIndex: number,
  seatIndex: number,
  kind: SeatKind,
): EditorLayout {
  return updateSeat(layout, rowIndex, seatIndex, (seat) => ({ ...seat, kind }));
}

/** Enables or disables one seat, which decides whether it is materialized. */
export function toggleSeatEnabled(
  layout: EditorLayout,
  rowIndex: number,
  seatIndex: number,
): EditorLayout {
  return updateSeat(layout, rowIndex, seatIndex, (seat) => ({
    ...seat,
    enabled: !seat.enabled,
  }));
}

/**
 * Validates the layout geometry against the same rules the API enforces.
 *
 * An empty result means the rows can be saved. Row seat counts are never
 * compared with each other: only per-row uniqueness and row-level uniqueness
 * are required.
 */
export function validateLayout(layout: EditorLayout): LayoutIssue[] {
  const issues: LayoutIssue[] = [];
  if (layout.rows.length === 0) {
    issues.push({
      path: "rows",
      field: "rows",
      message: "must contain at least one row",
    });
    return issues;
  }

  const rowLabels = new Set<string>();
  const rowPositions = new Set<number>();

  layout.rows.forEach((row, rowIndex) => {
    const rowPath = `rows[${rowIndex}]`;

    const label = row.label.trim();
    if (label === "") {
      issues.push({
        path: `${rowPath}.label`,
        field: "row.label",
        message: "must not be empty",
      });
    } else if (rowLabels.has(label)) {
      issues.push({
        path: `${rowPath}.label`,
        field: "row.label",
        message: "must be unique within its layout",
      });
    } else {
      rowLabels.add(label);
    }

    if (row.position <= 0) {
      issues.push({
        path: `${rowPath}.position`,
        field: "row.position",
        message: "must be greater than zero",
      });
    } else if (rowPositions.has(row.position)) {
      issues.push({
        path: `${rowPath}.position`,
        field: "row.position",
        message: "must be unique within its layout",
      });
    } else {
      rowPositions.add(row.position);
    }

    if (row.seats.length === 0) {
      issues.push({
        path: `${rowPath}.seats`,
        field: "row.seats",
        message: "must contain at least one seat",
      });
    }

    const seatNumbers = new Set<string>();
    const seatPositions = new Set<number>();

    row.seats.forEach((seat, seatIndex) => {
      const seatPath = `${rowPath}.seats[${seatIndex}]`;

      const number = seat.number.trim();
      if (number === "") {
        issues.push({
          path: `${seatPath}.number`,
          field: "seat.number",
          message: "must not be empty",
        });
      } else if (seatNumbers.has(number)) {
        issues.push({
          path: `${seatPath}.number`,
          field: "seat.number",
          message: "must be unique within its row",
        });
      } else {
        seatNumbers.add(number);
      }

      if (seat.position <= 0) {
        issues.push({
          path: `${seatPath}.position`,
          field: "seat.position",
          message: "must be greater than zero",
        });
      } else if (seatPositions.has(seat.position)) {
        issues.push({
          path: `${seatPath}.position`,
          field: "seat.position",
          message: "must be unique within its row",
        });
      } else {
        seatPositions.add(seat.position);
      }

      if (!(SEAT_KINDS as readonly string[]).includes(seat.kind)) {
        issues.push({
          path: `${seatPath}.kind`,
          field: "seat.kind",
          message: "must be standard, accessible, or companion",
        });
      }
    });
  });

  return issues;
}

/**
 * Validates a layout for publishing: the geometry must be saveable and at least
 * one seat must be enabled, because publishing freezes the version.
 */
export function validateForPublish(layout: EditorLayout): LayoutIssue[] {
  const issues = validateLayout(layout);
  if (issues.length > 0) {
    return issues;
  }
  if (enabledSeatCount(layout) === 0) {
    return [
      ...issues,
      {
        path: "rows",
        field: "layout",
        message: "must contain at least one enabled seat before publishing",
      },
    ];
  }
  return issues;
}

function updateRow(
  layout: EditorLayout,
  rowIndex: number,
  update: (row: EditorRow) => EditorRow,
): EditorLayout {
  if (!hasIndex(layout.rows, rowIndex)) {
    return layout;
  }
  return {
    ...layout,
    rows: layout.rows.map((row, index) =>
      index === rowIndex ? update(row) : row,
    ),
  };
}

function updateSeat(
  layout: EditorLayout,
  rowIndex: number,
  seatIndex: number,
  update: (seat: EditorSeat) => EditorSeat,
): EditorLayout {
  return updateRow(layout, rowIndex, (row) => {
    if (!hasIndex(row.seats, seatIndex)) {
      return row;
    }
    return {
      ...row,
      seats: row.seats.map((seat, index) =>
        index === seatIndex ? update(seat) : seat,
      ),
    };
  });
}

function newSeat(existing: readonly EditorSeat[], position: number): EditorSeat {
  const numbers = new Set(existing.map((seat) => seat.number.trim()));
  let candidate = 1;
  while (numbers.has(String(candidate))) {
    candidate += 1;
  }
  return {
    number: String(candidate),
    position,
    kind: "standard",
    enabled: true,
  };
}

/** Picks the first unused label at or after the current row count. */
function nextRowLabel(layout: EditorLayout): string {
  const used = new Set(layout.rows.map((row) => row.label.trim()));
  let index = layout.rows.length;
  while (used.has(rowLabel(index))) {
    index += 1;
  }
  return rowLabel(index);
}

/** Spreadsheet-style labels: A..Z, then AA, AB, and so on. */
function rowLabel(index: number): string {
  let remaining = index;
  let label = "";
  do {
    label = String.fromCharCode(65 + (remaining % 26)) + label;
    remaining = Math.floor(remaining / 26) - 1;
  } while (remaining >= 0);
  return label;
}

function hasIndex(values: readonly unknown[], index: number): boolean {
  return Number.isInteger(index) && index >= 0 && index < values.length;
}

function optionalId(id: string | undefined): { id?: string } {
  return id === undefined ? {} : { id };
}
