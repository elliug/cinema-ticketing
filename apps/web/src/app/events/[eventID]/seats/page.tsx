// /events/[eventID]/seats — guest seat selection for one event.
//
// The page is a thin server component: it reads the public availability snapshot
// for the event through lib/public-api.ts, prepares the map geometry, and hands
// both to the client panel. Everything a customer does after the first render
// (selecting, holding, refetching, counting down) lives in ./seat-map.tsx and
// ./hold-form.tsx, and every rule they apply is tested in
// lib/seat-selection.test.ts.
//
// Guest use is deliberate (S9, S11): the public reservation routes are mounted
// outside the administrative guard, so this page reads and writes without a
// token and without creating an account. The two server actions below are the
// only way the browser reaches the API, because the Go API is same-origin only
// for the web server.
//
// The route is server-rendered per request: availability is live data and must
// never be prerendered or cached.
import type {
  AvailabilityResponse,
  HoldResponse,
} from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";
import { layoutFromAvailability } from "@/lib/seat-selection";
import { SeatSelectionPanel } from "./seat-map";

export const dynamic = "force-dynamic";

interface SeatsPageProps {
  params: Promise<{ eventID: string }>;
}

export default async function EventSeatsPage({ params }: SeatsPageProps) {
  const { eventID } = await params;
  const eventId = eventID.trim();
  const availability = await loadAvailability(eventId);

  return (
    <main id="main-content" className="foundation">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>

      <section className="card" aria-labelledby="choose-seats">
        <p className="eyebrow">Guest seat selection · M2 reservation core</p>
        <h1 id="choose-seats">Choose your seats</h1>
        <p className="lead">
          Event <code>{eventId}</code>. No account is needed: pick as many seats
          as you like, then hold them. A hold is time-boxed and released
          automatically when its window ends.
        </p>
      </section>

      {availability.ok ? (
        <SeatSelectionPanel
          eventId={eventId}
          initialSeats={availability.seats}
          layout={layoutFromAvailability(availability.seats)}
          loadAvailability={loadAvailability}
          requestHold={requestHold}
        />
      ) : (
        <AvailabilityProblemCard availability={availability} />
      )}
    </main>
  );
}

/** Reads the public availability snapshot of one event. */
async function loadAvailability(eventId: string): Promise<AvailabilityResponse> {
  "use server";

  if (eventId === "") {
    return {
      ok: false,
      problem: {
        type: "https://cinema.local/problems/invalid-input",
        title: "Invalid input",
        status: 0,
        code: "invalid_input",
        detail: "this page needs an event id",
      },
    };
  }

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const client = createPublicClient(config.value);
  const result = await client.listEventSeats(eventId);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, seats: result.value.seats };
}

/**
 * Asks the reservation API to hold a selection.
 *
 * The key comes from the caller because it belongs to one attempt: the panel
 * mints a fresh one per attempt, and this action must never invent or reuse one.
 */
async function requestHold(input: {
  eventId: string;
  seatIds: number[];
  idempotencyKey: string;
}): Promise<HoldResponse> {
  "use server";

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const client = createPublicClient(config.value);
  const result = await client.createHold(input);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, hold: result.value };
}

function AvailabilityProblemCard({
  availability,
}: {
  availability: Extract<AvailabilityResponse, { ok: false }>;
}) {
  const problem = availability.problem;
  return (
    <section className="card problems" aria-labelledby="availability-problem">
      <h2 id="availability-problem">Seats could not be loaded</h2>
      <p>
        <code>{problem.code}</code> · HTTP {problem.status}
      </p>
      <p>{problem.detail}</p>
      <p className="notice">
        {problem.code === "network_error" ||
        problem.code === "coordination_unavailable"
          ? "The reservation service is temporarily unreachable. Reload the page to try again."
          : "Check the event id, then reload the page."}
      </p>
    </section>
  );
}
