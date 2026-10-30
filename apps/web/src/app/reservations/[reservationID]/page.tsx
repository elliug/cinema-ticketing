// /reservations/[reservationID] — the guest confirmation page (M3 · T7 · S1, S3).
//
// The page a buyer reaches after holding seats: it names the order and puts the
// Pay action on it. Everything that happens after the click (the attempt, its
// key, the answer) lives in ./payment-panel.tsx, and the key rules it relies on
// are the tested ones in lib/payment-key.ts.
//
// Three things this page deliberately does not do:
//
//   - it does not read the reservation first. There is no
//     `GET /api/v1/reservations/{id}` route yet, so the amount is shown once the
//     payment answer carries it instead of being invented here;
//   - it does not mint the payment key. The browser does, once per reservation,
//     and keeps it in `sessionStorage` so a reload resumes the open attempt rather
//     than being refused with `409 payment_in_flight`;
//   - it does not accept a "pay" that already happened. A ticket list is only
//     known from the paying session's answer; nothing lists tickets per order, so
//     a reload of a paid reservation says so instead of guessing.
//
// The route is server-rendered per request: a hold window is live data.
import { resolvePublicConfig } from "@/lib/public-api";
import { payReservationAction } from "./actions";
import { PaymentPanel } from "./payment-panel";

export const dynamic = "force-dynamic";

interface ReservationPageProps {
  params: Promise<{ reservationID: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}

export default async function ReservationPage({
  params,
  searchParams,
}: ReservationPageProps) {
  const { reservationID } = await params;
  const reservationId = reservationID.trim();
  const query = await searchParams;
  const posted = query.eventId;
  // The event id is how this page offers a way back to the seats; it arrives from
  // the seat page that created the hold, and its absence only removes that link.
  const eventId = (Array.isArray(posted) ? posted[0] : posted)?.trim() ?? "";

  const config = resolvePublicConfig(process.env);
  const apiBaseUrl = config.ok ? config.value.baseUrl : "";

  return (
    <main id="main-content" className="foundation">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>

      <section className="card" aria-labelledby="confirm-order">
        <p className="eyebrow">Guest checkout · M3 checkout and tickets</p>
        <h1 id="confirm-order">Confirm your order</h1>
        <p className="lead">
          Your seats are held for a short window. Paying inside that window turns
          the hold into one ticket per seat; letting it close releases them.
        </p>
        <p className="meta">
          Order <code>{reservationId === "" ? "unknown" : reservationId}</code>
          {eventId === "" ? null : (
            <>
              {" · "}
              <a href={`/events/${encodeURIComponent(eventId)}/seats`}>
                back to the seat map
              </a>
            </>
          )}
        </p>
      </section>

      {reservationId === "" ? (
        <ProblemCard
          title="This address names no reservation"
          detail="Open this page from the seat map after holding seats."
        />
      ) : config.ok ? (
        <PaymentPanel
          reservationId={reservationId}
          eventId={eventId}
          apiBaseUrl={apiBaseUrl}
          payReservation={payReservationAction}
        />
      ) : (
        <ProblemCard
          title="The payment page is not configured"
          detail={config.error.detail}
        />
      )}
    </main>
  );
}

function ProblemCard({ title, detail }: { title: string; detail: string }) {
  return (
    <section className="card problems" aria-labelledby="page-problem">
      <h2 id="page-problem">{title}</h2>
      <p>{detail}</p>
    </section>
  );
}
