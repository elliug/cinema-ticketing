// Deterministic health contract for GET /api/health.
//
// M0 foundation only: this pins the exact JSON the web service exposes for
// local and CI health checks. It depends only on the public `./health`
// interface (not on Next.js internals or the Go API), and it runs with Node's
// native test runner so it is deterministic and needs no install.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { HealthPayload } from "./health.ts";
import {
  buildHealthPayload,
  healthStatusCode,
  HEALTH_CONTENT_TYPE,
} from "./health.ts";

// The exact contract: keys and values are order- and byte-stable.
const EXPECTED_PAYLOAD: HealthPayload = {
  status: "ok",
  service: "cinema-web",
  milestone: "M0",
  checks: {
    runtime: "ok",
  },
};

const EXPECTED_JSON = JSON.stringify(EXPECTED_PAYLOAD);

test("buildHealthPayload returns the exact M0 health contract", () => {
  assert.deepEqual(buildHealthPayload(), EXPECTED_PAYLOAD);
});

test("buildHealthPayload is byte-stable when serialized", () => {
  assert.equal(JSON.stringify(buildHealthPayload()), EXPECTED_JSON);
  // A fresh build must reproduce the identical serialization each time.
  assert.equal(
    JSON.stringify(buildHealthPayload()),
    JSON.stringify(buildHealthPayload()),
  );
});

test("buildHealthPayload is deeply frozen and immutable", () => {
  const payload = buildHealthPayload();
  assert.equal(Object.isFrozen(payload), true);
  assert.equal(Object.isFrozen(payload.checks), true);

  assert.throws(() => {
    // @ts-expect-error mutate to prove the contract cannot drift.
    payload.status = "degraded";
  });
});

test("healthStatusCode reports HTTP 200 for a healthy payload", () => {
  assert.equal(healthStatusCode(buildHealthPayload()), 200);
});

test("HEALTH_CONTENT_TYPE is application/json", () => {
  assert.equal(HEALTH_CONTENT_TYPE, "application/json");
});
