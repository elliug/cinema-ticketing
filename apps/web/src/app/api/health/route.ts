// GET /api/health — deterministic, self-contained health contract for the M0
// web foundation.
//
// M0 foundation only: it reports that the web service process is serving and
// that the M0 baseline is in place. It performs no I/O and consults no external
// dependency, so it is safe for local and CI health checks. No product behavior
// (venue, layout, reservation, payment, ticketing, or identity) is represented
// here; that arrives in later milestones.
//
// The response payload and its status mapping are owned by lib/health.ts and
// pinned by lib/health.test.ts, so the wire contract is tested in isolation.
import { NextResponse } from "next/server";
import {
  buildHealthPayload,
  HEALTH_CONTENT_TYPE,
  healthStatusCode,
} from "@/lib/health";

// Prerender the endpoint as a static response so it is cacheable and stable
// for CI. It has no per-request data dependencies.
export const dynamic = "force-static";

export function GET(): NextResponse {
  const payload = buildHealthPayload();
  return NextResponse.json(payload, {
    status: healthStatusCode(payload),
    headers: { "Content-Type": HEALTH_CONTENT_TYPE },
  });
}
