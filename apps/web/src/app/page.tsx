// Home page for the M0 web foundation.
//
// M0 foundation only: it states that the platform is operational and that M1+
// product behavior is intentionally absent. It renders no venue, layout,
// reservation, payment, or ticketing UI; those arrive in later milestones.
// The view is static and self-contained (no remote fonts or images).

export default function HomePage() {
  return (
    <main id="main-content" className="foundation">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>

      <section aria-labelledby="foundation-status" className="card">
        <p className="eyebrow">Cinema ticketing platform</p>
        <h1 id="foundation-status">M0 foundation is operational</h1>
        <p className="lead">
          The service and web application run locally so the repository
          baseline can be verified. This milestone is operational only and does
          not add product behavior.
        </p>
      </section>

      <section aria-labelledby="foundation-scope" className="card">
        <h2 id="foundation-scope">What M0 provides</h2>
        <ul className="list">
          <li>A minimal Next.js App Router foundation with TypeScript.</li>
          <li>
            A deterministic health endpoint:{" "}
            <code>GET /api/health</code> returns a fixed JSON contract for
            local and CI checks.
          </li>
          <li>
            A Go API exposing{" "}
            <code>GET /health/live</code> and <code>GET /health/ready</code>.
          </li>
        </ul>
      </section>

      <section aria-labelledby="foundation-next" className="card">
        <h2 id="foundation-next">What M0 does not provide yet</h2>
        <p>
          Venue and layout management, reservations and payments, ticket
          purchase and issuance, identity and roles, and reporting are out of
          scope for M0 and land in later milestones.
        </p>
      </section>
    </main>
  );
}
