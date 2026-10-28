// M0 health contract for the cinema ticketing web service.
//
// M0 foundation only: this module owns the exact JSON that GET /api/health
// returns. It is deliberately self-contained and deterministic — it reports
// that the web service process is serving and that the M0 baseline is in
// place. No product behavior (venue, layout, reservation, payment,
// ticketing, or identity) is represented here; those arrive in later
// milestones. The Go API owns its own /health endpoints; this web health
// report is an independent readiness marker for front-end deployment checks.
//
// Consumers:
//   - src/app/api/health/route.ts renders this payload for GET /api/health.
//   - src/lib/health.test.ts pins the exact serialization for CI.

export type HealthStatus = "ok";

export interface HealthChecks {
  // The Node.js process that serves this route is running.
  runtime: HealthStatus;
}

export interface HealthPayload {
  status: HealthStatus;
  service: "cinema-web";
  milestone: "M0";
  checks: HealthChecks;
}

/**
 * Builds the canonical M0 health payload.
 *
 * The returned object is deeply frozen so its shape cannot drift at runtime,
 * which keeps /api/health byte-stable across requests and processes.
 */
export function buildHealthPayload(): HealthPayload {
  const payload: HealthPayload = {
    status: "ok",
    service: "cinema-web",
    milestone: "M0",
    checks: {
      runtime: "ok",
    },
  };

  return deepFreeze(payload);
}

/**
 * Maps a health payload to its HTTP status code. Healthy returns 200.
 *
 * At M0 the only state is "ok", but exposing the mapping keeps the contract
 * explicit and ready for a degradation path without changing the shape.
 */
export function healthStatusCode(payload: HealthPayload): number {
  return payload.status === "ok" ? 200 : 503;
}

/**
 * Content-Type for the /api/health response.
 */
export const HEALTH_CONTENT_TYPE = "application/json";

function deepFreeze<T extends object>(value: T): T {
  for (const nested of Object.values(value)) {
    if (nested && typeof nested === "object" && !Object.isFrozen(nested)) {
      deepFreeze(nested);
    }
  }
  return Object.freeze(value);
}
