# M3 — Checkout, tickets and PDF delivery

Simulated checkout that turns a hold into a paid ticket, with a confirmation
surface where the buyer sees the ticket detail and downloads a PDF carrying the
same detail and a QR, stored in S3 (Floci in development). After M3 the
application must be demonstrable end to end to a real user.
Builds on the integrated M2 reservation core.

## Specs

1. S1 — "Quiero que tras M3 tengamos una aplicación end to end funcional y que ya se pueda mostrar al usuario."
2. S2 — "como en M3 se va a relizar la proma de pago (simulada)".
3. S3 — "me gustaría que al pagar y que el usuairio tenga la confirmación de la entrada en la web se pueda descargar un PDF con la entrada".
4. S4 — "que la entrada tanto en web como en pdf tenga el detalle y disponga de un QR con el detalle".
5. S5 — "Este pdf me gustaría que se almacenara en S3 (para ello quiero que utilices como infraestructura floci en forma de docker como redis y postgre)".
6. S6 — from the approved design: "Start with simulated payments behind a replaceable integration boundary." (design S8), so the payment provider is an adapter and no real credentials exist in v1.
7. S7 — from the approved architecture checklist: "Payment requests and callbacks are idempotent." and "Guest checkout works without creating a permanent account."
8. S8 — from the approved architecture checklist: "No real payment credentials or live payment flow exist in v1."

## Constraints inherited from the approved architecture

- PostgreSQL stays the source of truth; Redis remains the coordination layer for holds; the ticket is a durable row, not a Redis key.
- The payment boundary and the object-storage boundary are both replaceable adapters, so a real provider later replaces only the adapter (design S8).
- The QR must be verifiable by a later milestone (M4 cashier workflows): the ticket detail it carries has to be checkable server side, never trusted from the client.
- Floci runs as a compose service next to postgres and redis (`floci/floci:latest`, port 4566), and the end-to-end verification of M3 must run against it, exactly as M1/M2 verification runs against compose.

## Tasks

- T1 — S6, S7, S8 — simulated payment adapter behind the payment boundary, idempotent charge and callback — pending definition
- T2 — S7 — checkout that converts a held reservation into a paid order and issues tickets — pending definition
- T3 — S4, S8 — ticket detail projection shared by the web surface and the PDF — pending definition
- T4 — S3, S5 — PDF generation, storage in S3 (Floci) and download — pending definition
- T5 — S4 — QR encoding the ticket detail and its server-side verification — pending definition
- T6 — S1 — web confirmation and ticket surfaces, guest and signed-in — pending definition
- T7 — S1 — `make verify-m3` end-to-end proof against compose including Floci — pending definition

## Decided

These were answered before implementation, so M3 starts with them fixed.

1. **QR content and verification model**: a *signed opaque token*. The server signs the ticket identifier with an HMAC over the ticket, event and seats, and the scan verifies it against the database. The client never carries the full detail, so a photographed QR exposes no personal data, and M4 can distinguish valid, already used and forged tickets.
2. **PDF lifecycle**: generated once when the payment is confirmed, stored in S3 (Floci in development), and served through a short-lived pre-signed URL (`GET /api/v1/tickets/{id}/pdf` → redirect). The stored PDF is an immutable artefact that matches what was sold.
3. **Pricing for v1**: one price per event (`events.price_cents`), so the order amount is price × number of seats. This closes the architecture's open pricing decision for v1 and requires a migration that adds the column to `cinema.events`.
4. **Ticket detail fields**: film, room, date and time, row and seat labels, amount, order reference and the signed QR. One `TicketDetail` projection feeds both the web view and the PDF, so the two cannot drift.
5. **Delivery channel**: download from the web confirmation page only. No email delivery in M3; adding it is a later decision that needs a vendor.

## Log

- L1 — "Mientras acabamos M2 me gustaría iterar y dar más contexto a M3 para cuando tengamos que implementarla. Quiero que tras M3 tengamos una aplicación end to end funcional y que ya se pueda mostrar al usuario. PAra ello, como en M3 se va a relizar la proma de pago (simulada) me gustaría que al pagar y que el usuairio tenga la confirmación de la entrada en la web se pueda descargar un PDF con la entrada y que la entrada tanto en web como en pdf tenga el detalle y disponga de un QR con el detalle. Este pdf me gustaría que se almacenara en S3 (para ello quiero que utilices como infraestructura floci en forma de docker como redis y postgre)"
- L2 — Floci confirmed as the S3 emulator: `floci/floci:latest`, port 4566, S3-compatible with pre-signed URLs, no real credentials.
- L3 — Decided with the user before implementation: signed opaque QR token verified server side; PDF generated at purchase, stored in S3 and served by pre-signed URL; one price per event (`events.price_cents`) with amount = price × seats; the agreed ticket detail shared by web and PDF; web-only download, no email.
