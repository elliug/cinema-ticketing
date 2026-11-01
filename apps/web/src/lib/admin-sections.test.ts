// The administration's navigation contract (issue #92).
//
// What a type checker cannot see is the *content* of this list: that the four screens are
// the four the section actually has, that each appears once, and above all that they are in
// the order the API enforces rather than the order they were written in. A reordering would
// compile, would pass the browser gate, and would teach an operator the wrong sequence, so
// the order is pinned here as a fact rather than left to whoever edits the array next.
//
// The predicate is tested where the repository has already been bitten: `/admin` is a prefix
// of `/administrator`, and a screen outside the section must never be marked current.
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  ADMIN_DOOR_PATH,
  ADMIN_INDEX_PATH,
  ADMIN_LAYOUTS_PATH,
  ADMIN_SCHEDULE_PATH,
  ADMIN_SECTIONS,
  ADMIN_VENUES_PATH,
  isCurrentSection,
} from "./admin-sections.ts";

test("the four screens are the four the administration has, each exactly once", () => {
  const hrefs = ADMIN_SECTIONS.map((section) => section.href);

  assert.deepEqual(hrefs, [
    ADMIN_VENUES_PATH,
    ADMIN_LAYOUTS_PATH,
    ADMIN_SCHEDULE_PATH,
    ADMIN_DOOR_PATH,
  ]);
  assert.equal(new Set(hrefs).size, hrefs.length, "two sections share an address");

  const labels = ADMIN_SECTIONS.map((section) => section.label);
  assert.equal(new Set(labels).size, labels.length, "two sections share a label");
  for (const section of ADMIN_SECTIONS) {
    assert.notEqual(section.label.trim(), "", "a section has no name");
    assert.notEqual(section.purpose.trim(), "", `${section.href} says nothing about what it is for`);
    assert.notEqual(
      section.prerequisite.trim(),
      "",
      `${section.href} does not say what has to come before it`,
    );
    assert.ok(
      section.href.startsWith("/admin/"),
      `${section.href} is not inside the administration`,
    );
  }
});

test("the index is a map, not one of the screens it maps", () => {
  assert.equal(ADMIN_INDEX_PATH, "/admin");
  const hrefs = ADMIN_SECTIONS.map((section) => section.href);
  assert.equal(
    hrefs.includes(ADMIN_INDEX_PATH),
    false,
    "the index must not list itself as a screen",
  );
});

test("a section is current only on its own screen", () => {
  assert.equal(isCurrentSection(ADMIN_SCHEDULE_PATH, ADMIN_SCHEDULE_PATH), true);
  assert.equal(isCurrentSection(`${ADMIN_SCHEDULE_PATH}/`, ADMIN_SCHEDULE_PATH), true);

  // The trap: a page outside the administration that merely starts with the same letters.
  assert.equal(isCurrentSection("/administrator", ADMIN_INDEX_PATH), false);
  assert.equal(isCurrentSection("/admin/schedule/extra", ADMIN_SCHEDULE_PATH), false);
  assert.equal(isCurrentSection(ADMIN_VENUES_PATH, ADMIN_SCHEDULE_PATH), false);
  assert.equal(isCurrentSection("", ADMIN_SCHEDULE_PATH), false);
});
