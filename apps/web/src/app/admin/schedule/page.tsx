// /admin/schedule — create a film, schedule an event, inspect its seats.
//
// The page is intentionally thin: the two forms are client components that call
// the server actions in ./actions.ts, and when an `eventId` query parameter is
// present the page reads that event's materialized seats through the typed
// administrative client and hands them to the event seat view.
//
// The route is server-rendered per request because it depends on local
// administrative configuration and live data; it is never prerendered at build
// time. Like the API itself, it is a local-development surface only.
import { ADMIN_TOKEN_ENV } from "@/lib/admin-api";
import type { AdminProblem } from "@/lib/admin-api";
import { summarizeEventSeats } from "@/lib/event-seats";
import { ADMIN_SCHEDULE_PATH } from "@/lib/scheduling";
import { readEventSeatsAction } from "./actions";
import { EventSeatsView } from "./event-seats-view";
import { EventForm, FilmForm } from "./schedule-forms";

export const dynamic = "force-dynamic";

interface SchedulePageProps {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}

export default async function SchedulePage({ searchParams }: SchedulePageProps) {
  const params = await searchParams;
  const posted = params.eventId;
  const eventId = (Array.isArray(posted) ? posted[0] : posted)?.trim() ?? "";

  return (
    <main id="main-content" className="foundation admin">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>

      <section className="card" aria-labelledby="schedule-title">
        <p className="eyebrow">Local administration · M1 venue and layout</p>
        <h1 id="schedule-title">Films and events</h1>
        <p className="lead">
          Create a film, schedule it in a room against a published layout
          version, then read the event seats that were materialized from that
          layout.
        </p>
      </section>

      <section className="card" aria-labelledby="create-film">
        <h2 id="create-film">Create a film</h2>
        <FilmForm />
      </section>

      <section className="card" aria-labelledby="schedule-event">
        <h2 id="schedule-event">Schedule an event</h2>
        <p className="notice">
          The layout version must be published: a draft version is rejected with
          a <code>conflict</code> problem and no event is created. Start times are
          entered in UTC.
        </p>
        <EventForm />
      </section>

      {eventId === "" ? (
        <section className="card">
          <p className="notice">
            Schedule an event, or open{" "}
            <code>{ADMIN_SCHEDULE_PATH}?eventId=…</code>, to see its materialized
            seats.
          </p>
        </section>
      ) : (
        await renderEventSeats(eventId)
      )}
    </main>
  );
}

async function renderEventSeats(eventId: string) {
  // The read goes through the same server action the rest of the surface uses,
  // so the administrative client and its token stay inside the server boundary.
  const result = await readEventSeatsAction(eventId);
  if (!result.ok) {
    return <ProblemCard problem={result.error} eventId={eventId} />;
  }

  return <EventSeatsView summary={summarizeEventSeats(result.value)} />;
}

function ProblemCard({
  problem,
  eventId,
}: {
  problem: AdminProblem;
  eventId?: string;
}) {
  return (
    <section className="card problems" aria-labelledby="event-seats-problem">
      <h2 id="event-seats-problem">The event seats could not be loaded</h2>
      {eventId === undefined ? null : (
        <p>
          Event <code>{eventId}</code>
        </p>
      )}
      <p>
        <code>{problem.code}</code> · HTTP {problem.status}
      </p>
      <p>{problem.detail}</p>
      <p className="notice">
        This surface works only in local development: the API must run with local
        administration enabled, and this page needs the same{" "}
        <code>{ADMIN_TOKEN_ENV}</code> token as the API.
      </p>
    </section>
  );
}
