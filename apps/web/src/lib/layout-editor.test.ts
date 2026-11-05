// Pure layout editor model.
//
// The model must support rows that hold different seat counts and must mirror
// the backend validation rules, so every test below works through the public
// model functions only. No rectangular grid is assumed anywhere: a row with
// three seats and a row with one seat are both normal.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { AdminLayout, SeatKind } from "./admin-api.ts";
import type { EditorLayout } from "./layout-editor.ts";
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
  seatKindLabel,
  toRowPayloads,
  toggleSeatEnabled,
  validateForPublish,
  validateLayout,
} from "./layout-editor.ts";

/** A published layout whose rows deliberately have 3, 1, and 5 seats. */
const HETEROGENEOUS_LAYOUT: AdminLayout = {
  id: "layout-1",
  roomId: "room-1",
  version: 2,
  status: "draft",
  rows: [
    {
      id: "row-a",
      label: "A",
      position: 1,
      seats: [
        { id: "seat-a1", number: "1", position: 1, kind: "standard", enabled: true },
        { id: "seat-a2", number: "2", position: 2, kind: "accessible", enabled: true },
        { id: "seat-a3", number: "3", position: 3, kind: "companion", enabled: true },
      ],
    },
    {
      id: "row-b",
      label: "B",
      position: 2,
      seats: [
        { id: "seat-b1", number: "1", position: 1, kind: "standard", enabled: false },
      ],
    },
    {
      id: "row-c",
      label: "C",
      position: 3,
      seats: [
        { id: "seat-c1", number: "1", position: 1, kind: "standard", enabled: true },
        { id: "seat-c2", number: "2", position: 2, kind: "standard", enabled: true },
        { id: "seat-c3", number: "3", position: 3, kind: "standard", enabled: true },
        { id: "seat-c4", number: "4", position: 4, kind: "standard", enabled: true },
        { id: "seat-c5", number: "5", position: 5, kind: "standard", enabled: true },
      ],
    },
  ],
};

function editor(): EditorLayout {
  return fromLayout(HETEROGENEOUS_LAYOUT);
}

function seatCounts(layout: EditorLayout): number[] {
  return layout.rows.map((row) => row.seats.length);
}

function details(layout: EditorLayout): string[] {
  return validateLayout(layout).map(layoutIssueDetail);
}

function publishDetails(layout: EditorLayout): string[] {
  return validateForPublish(layout).map(layoutIssueDetail);
}

test("a seat kind is printed in the word an operator reads", () => {
  // The stored value is the column's and travels unchanged; what this adds is the word the
  // editor's select shows, in the same Castilian as every other word on the screen.
  assert.equal(seatKindLabel("standard"), "estándar");
  assert.equal(seatKindLabel("accessible"), "accesible");
  assert.equal(seatKindLabel("companion"), "acompañante");
  // A kind the column grew later is printed as served, never blanked.
  assert.equal(seatKindLabel("recliner" as SeatKind), "recliner");
});

test("fromLayout keeps rows with different seat counts without padding", () => {
  const layout = editor();

  assert.deepEqual(seatCounts(layout), [3, 1, 5]);
  assert.equal(layout.layoutId, "layout-1");
  assert.equal(layout.version, 2);
  assert.equal(layout.status, "draft");
  assert.deepEqual(
    layout.rows.map((row) => row.label),
    ["A", "B", "C"],
  );
  assert.deepEqual(
    layout.rows[1].seats.map((seat) => seat.enabled),
    [false],
  );
});

test("fromLayout tolerates a layout the API reports with no rows", () => {
  const layout = fromLayout({
    ...HETEROGENEOUS_LAYOUT,
    rows: undefined as unknown as AdminLayout["rows"],
  });

  assert.deepEqual(layout.rows, []);
});

test("toRowPayloads round-trips the API shape exactly", () => {
  assert.deepEqual(toRowPayloads(editor()), HETEROGENEOUS_LAYOUT.rows);
});

test("a heterogeneous layout that mirrors the backend rules is valid", () => {
  assert.deepEqual(details(editor()), []);
  assert.deepEqual(publishDetails(editor()), []);
});

test("addRow appends a new row without touching the other rows", () => {
  const updated = addRow(editor());

  assert.equal(updated.rows.length, 4);
  const added = updated.rows[3];
  assert.equal(added.label, "D");
  assert.equal(added.position, 4);
  assert.equal(added.seats.length, 1);
  assert.deepEqual(
    added.seats.map((seat) => [seat.number, seat.position, seat.kind, seat.enabled]),
    [["1", 1, "standard", true]],
  );
  assert.deepEqual(seatCounts({ ...updated, rows: updated.rows.slice(0, 3) }), [
    3, 1, 5,
  ]);
  assert.deepEqual(details(updated), []);
});

test("addRow skips row labels and positions already in use", () => {
  const withGap = setRowLabel(editor(), 1, "D");
  const updated = addRow(withGap);

  assert.deepEqual(
    updated.rows.map((row) => row.label),
    ["A", "D", "C", "E"],
  );
  assert.deepEqual(details(updated), []);
});

test("addSeat grows only the targeted row", () => {
  const updated = addSeat(editor(), 1);

  assert.deepEqual(seatCounts(updated), [3, 2, 5]);
  assert.deepEqual(
    updated.rows[1].seats.map((seat) => [seat.number, seat.position, seat.enabled]),
    [
      ["1", 1, false],
      ["2", 2, true],
    ],
  );
  assert.deepEqual(details(updated), []);
});

test("removeSeat removes one seat and leaves the row's other seats intact", () => {
  const updated = removeSeat(editor(), 2, 1);

  assert.deepEqual(seatCounts(updated), [3, 1, 4]);
  assert.deepEqual(
    updated.rows[2].seats.map((seat) => seat.number),
    ["1", "3", "4", "5"],
  );
  assert.deepEqual(details(updated), []);
});

test("removeRow drops one row and preserves the remaining seat counts", () => {
  const updated = removeRow(editor(), 1);

  assert.deepEqual(
    updated.rows.map((row) => row.label),
    ["A", "C"],
  );
  assert.deepEqual(seatCounts(updated), [3, 5]);
  assert.deepEqual(details(updated), []);
});

test("toggleSeatEnabled flips only the targeted seat", () => {
  const updated = toggleSeatEnabled(editor(), 1, 0);

  assert.equal(updated.rows[1].seats[0].enabled, true);
  assert.deepEqual(
    updated.rows[0].seats.map((seat) => seat.enabled),
    [true, true, true],
  );
  assert.equal(enabledSeatCount(updated), 9);
});

test("setSeatKind changes only the targeted seat kind", () => {
  const updated = setSeatKind(editor(), 2, 0, "accessible");

  assert.equal(updated.rows[2].seats[0].kind, "accessible");
  assert.equal(updated.rows[0].seats[0].kind, "standard");
  assert.deepEqual(details(updated), []);
});

test("model operations never mutate the layout they were given", () => {
  const original = editor();
  const snapshot = structuredClone(original);

  addRow(original);
  addSeat(original, 0);
  removeSeat(original, 2, 0);
  removeRow(original, 1);
  toggleSeatEnabled(original, 0, 0);
  setSeatKind(original, 0, 0, "companion");
  setSeatNumber(original, 0, 0, "9");
  setSeatPosition(original, 0, 0, 9);
  setRowLabel(original, 0, "Z");
  setRowPosition(original, 0, 9);

  assert.deepEqual(original, snapshot);
});

test("a duplicate seat number inside one row is rejected", () => {
  const updated = setSeatNumber(editor(), 0, 1, "1");

  assert.deepEqual(details(updated), ["seat.number: must be unique within its row"]);
});

test("a duplicate seat number is compared after trimming, like the backend", () => {
  const updated = setSeatNumber(editor(), 0, 1, "  3  ");

  assert.deepEqual(details(updated), ["seat.number: must be unique within its row"]);
});

test("the same seat number in a different row is allowed", () => {
  const updated = setSeatNumber(editor(), 1, 0, "1");

  assert.deepEqual(details(updated), []);
});

test("a duplicate seat position inside one row is rejected", () => {
  const updated = setSeatPosition(editor(), 0, 2, 2);

  assert.deepEqual(details(updated), [
    "seat.position: must be unique within its row",
  ]);
});

test("a duplicate row label inside the layout is rejected", () => {
  const updated = setRowLabel(editor(), 1, " A ");

  assert.deepEqual(details(updated), [
    "row.label: must be unique within its layout",
  ]);
});

test("a duplicate row position inside the layout is rejected", () => {
  const updated = setRowPosition(editor(), 2, 2);

  assert.deepEqual(details(updated), [
    "row.position: must be unique within its layout",
  ]);
});

test("a row with no seats is rejected", () => {
  const updated = removeSeat(editor(), 1, 0);

  assert.deepEqual(seatCounts(updated), [3, 0, 5]);
  assert.deepEqual(details(updated), ["row.seats: must contain at least one seat"]);
});

test("an empty row label and a non-positive position are rejected", () => {
  const blankLabel = setRowLabel(editor(), 0, "   ");
  assert.deepEqual(details(blankLabel), ["row.label: must not be empty"]);

  const zeroRowPosition = setRowPosition(editor(), 0, 0);
  assert.deepEqual(details(zeroRowPosition), [
    "row.position: must be greater than zero",
  ]);

  const zeroSeatPosition = setSeatPosition(editor(), 0, 0, 0);
  assert.deepEqual(details(zeroSeatPosition), [
    "seat.position: must be greater than zero",
  ]);
});

test("an empty layout is rejected", () => {
  const empty: EditorLayout = {
    layoutId: "layout-1",
    version: 1,
    status: "draft",
    rows: [],
  };

  assert.deepEqual(details(empty), ["rows: must contain at least one row"]);
});

test("publishing is blocked until at least one seat is enabled", () => {
  let layout = editor();
  for (const [rowIndex, row] of layout.rows.entries()) {
    for (let seatIndex = 0; seatIndex < row.seats.length; seatIndex += 1) {
      if (layout.rows[rowIndex].seats[seatIndex].enabled) {
        layout = toggleSeatEnabled(layout, rowIndex, seatIndex);
      }
    }
  }

  assert.equal(enabledSeatCount(layout), 0);
  assert.deepEqual(details(layout), []);
  assert.deepEqual(publishDetails(layout), [
    "layout: must contain at least one enabled seat before publishing",
  ]);

  const enabled = toggleSeatEnabled(layout, 1, 0);
  assert.deepEqual(publishDetails(enabled), []);
});

test("publish validation still reports structural problems first", () => {
  const broken = setRowLabel(editor(), 1, "A");

  assert.deepEqual(publishDetails(broken), [
    "row.label: must be unique within its layout",
  ]);
});
