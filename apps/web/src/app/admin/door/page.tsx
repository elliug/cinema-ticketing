// /admin/door — the door screen a cashier uses to admit one person (M4.1 · T8 · S4, S5;
// Marquesina since visual-redesign).
//
// It takes the code a buyer shows, offers one action, and prints one answer. The route
// authenticates nobody new: it sits under the local administration exactly as the venue, layout
// and schedule screens do, and it asks the **public** admission route (`POST /tickets/redeem`)
// with no administrative token, because that route is public and a second endpoint for this
// screen would be a second place for the same rule to live.
//
// Two things it deliberately does not do:
//
//   - it does not re-verify anything on the client. The server verifies the code and admits the
//     ticket it names, in one answer, and that answer is the verdict (S3);
//   - it does not trust the printed page. The PDF is a record of the sale and never changes; the
//     door asks the box office (S5).
//
// The route is server-rendered per request — an admission is never prerendered and never cached —
// and it is a local-development surface, like the rest of `/admin`.
import Link from "next/link";

import { PageHeader } from "@/components";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { redeemCodeAction } from "./actions";
import { DoorForm } from "./door-form";

export const dynamic = "force-dynamic";

export default function DoorPage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Administración local · M4.1"
        title="Acceso"
        description="Un código, una respuesta. Una entrada admite a una persona exactamente una vez: un código ya usado, una entrada anulada — o cuya sesión se canceló — y un código que no es una entrada nuestra son cuatro respuestas distintas, y cada una lo dice con palabras. No se guarda nada en este navegador."
      />

      <DoorForm redeem={redeemCodeAction} />

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">De dónde sale el código</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Toda entrada que emitió un pago lleva su propio código en su página y en su PDF. El
            acceso no lee ninguno de los dos como veredicto: la misma ruta pública que llamaría
            un escáner responde qué admite el código, y dos accesos presentando un código en el
            mismo momento es el caso normal, no un accidente.
          </p>
        </CardHeader>
        <CardContent>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Compra una en <Link href="/events">la cartelera</Link> y ábrela después desde{" "}
            <Link href="/tickets">las entradas que compró este navegador</Link>.
          </p>
        </CardContent>
      </Card>
    </main>
  );
}
