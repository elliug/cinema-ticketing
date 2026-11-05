// Home page (M3.5 · T3 · S1, S2, S8; redesigned in Marquesina).
//
// The foyer: two doors a stranger can take — buy a ticket, or open the ones already bought —
// and the operator's door into the box office, named as what it is.
//
// What it deliberately does not do is pretend to be the catalogue. There is no list of
// screenings, no film title, no price and no seat count here: those live in `/events`, over a
// read that answers for them, and a row of plausible films invented on this page would be
// exactly the claim S8 forbids. It also no longer says a screen is missing: the catalogue, the
// seat map, the checkout, the ticket and the box office all exist now, and each one is one
// link away.
import Link from "next/link";

import { Alert, Card, PageHeader } from "@/components";
import { SIGN_IN_PATH } from "@/lib/identity";

export default function HomePage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Cinema Ticketing"
        title="Una sesión, una butaca, una entrada"
        description="Cartelera, plano de butacas, pago y entrada. Del otro lado, los mismos datos leídos como taquilla: cines, salas, planos y programación."
      />

      <section aria-labelledby="buyer-path" className="grid items-start gap-4 md:grid-cols-2">
        <Card
          id="buyer-path"
          title="Comprar una entrada"
          description="Lo que hace alguien que llega por primera vez, en orden."
        >
          <ol className="list">
            <li>
              <h3>Mira la cartelera</h3>
              <p className="notice">
                Las sesiones a la venta, en el orden en que las programa el cine, con la hora
                del propio local y las butacas que quedan.
              </p>
              <p>
                <Link href="/events">Ver la cartelera</Link>
              </p>
            </li>
            <li>
              <h3>Elige las butacas</h3>
              <p className="notice">
                El plano sale del plano real de la sala: cada fila con sus butacas, cada estado
                dicho con palabras además de con color, y el teclado funciona igual que el ratón.
              </p>
            </li>
            <li>
              <h3>Paga y guarda la entrada</h3>
              <p className="notice">
                Reservar arranca una cuenta atrás. Pagar dentro de ese plazo emite una entrada
                por butaca, con su código y su PDF.
              </p>
            </li>
          </ol>
        </Card>

        <Card
          id="tickets-path"
          title="Mis entradas"
          description="Las que ya compraste, en esta pestaña o en tu cuenta."
        >
          <p className="notice">
            Esta pestaña guarda las entradas que abriste, y las olvida cuando la taquilla ya no
            las conoce. Si inicias sesión, la lista la responde la taquilla en vez del navegador:
            es la única diferencia, y ninguna de las dos rutas pide cuenta para comprar.
          </p>
          <p>
            <Link href="/tickets">Ver mis entradas</Link>
          </p>
          <p>
            <Link href={SIGN_IN_PATH}>Iniciar sesión con un enlace</Link>
          </p>
        </Card>
      </section>

      <Card
        id="box-office"
        title="Taquilla"
        description="Las pantallas de quien opera el cine. En este despliegue son de desarrollo local: la API tiene que arrancar con la administración local activada y la web necesita el mismo token."
      >
        <ul className="list">
          <li>
            <Link href="/admin/venues">Cines, salas y planos</Link> — crear un cine, añadir
            salas y abrir los planos de una sala.
          </li>
          <li>
            <Link href="/admin/layouts">Editor de planos</Link> — filas y butacas de una versión
            del plano, guardada como borrador y publicada cuando está bien.
          </li>
          <li>
            <Link href="/admin/schedule">Películas y sesiones</Link> — programar una película en
            una sala, con su precio y su estado.
          </li>
          <li>
            <Link href="/admin/door">Acceso</Link> — un código, una respuesta, una persona.
          </li>
        </ul>
      </Card>

      <Alert tone="info" title="Sobre iniciar sesión">
        <p>
          Iniciar sesión es real y es opcional: un enlace enviado a tu dirección te da sesión
          durante un rato, y esa sesión es lo que permite que la taquilla conteste &quot;estas
          son tus entradas&quot; en vez de guardarlas este navegador. Nada del proceso de compra
          pide cuenta, y nada comprueba quién eres antes de venderte una butaca.
        </p>
        <p>
          El enlace de tu correo aterriza en <code>/in/&#123;token&#125;</code>, y esa página es
          la que sabe quién eres. La cabecera solo ofrece la entrada al acceso y no dice nada
          sobre si este navegador tiene sesión: tendría que leer la cookie para saberlo, y una
          suposición en el navegador no coincidiría con la página que sirvió el servidor.
        </p>
      </Alert>
    </main>
  );
}
