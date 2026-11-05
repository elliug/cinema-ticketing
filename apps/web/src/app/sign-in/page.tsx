// /sign-in — asking for a sign-in link (identity-login T6 · S1, S2, S7).
//
// An account in this product is one thing: a list of the tickets bought with an address, read
// from the box office instead of kept by this browser (D3). Nobody needs one to buy a ticket or
// to open one — that is D4, and it is why this screen is a way in rather than a gate in front of
// the checkout.
//
// The page is a server component rendered per request. It holds no state: the form does, in one
// client component, and what that form says is decided by lib/identity.ts. The API is reached
// only from server actions (./actions.ts), so the browser never learns where it lives.
//
// What the screen promises, in three sentences, and only these (D1, S2, S6):
//
//   1. the link expires in **minutes** and works **once** — it is a step, not a key;
//   2. anyone with access to that mailbox can sign in with it, so a shared inbox is a shared
//      account. That is the definition of a magic link and also its limit, and hiding it would
//      not make it untrue;
//   3. if the address has no account, nothing is revealed: the same answer comes back either way.
//
// It never claims an address was verified (that happens at the box office, when the link is spent
// — S3), and it never claims a message was sent. The confirmation is conditional for exactly that
// reason: "if that address can receive mail, a link is on its way."
//
// Signing out lives here too, because this is the only screen that can carry it today: the header
// has no idea whether this browser holds a session, and a guess rendered on the client would
// disagree with the server's render (S7 retires the demonstration session precisely because a
// fake one beside a real one is a claim that the fake one is authentication).
//
// The sign-out button now ends the session at both ends — the cookie here (./actions.ts) and the
// row at the box office (T3) — and the copy below says so. It also says what happens when the
// box office cannot be reached, because the button still signs this browser out then: a session
// that could not be closed is one that expires on its own, and holding somebody inside a session
// they asked to leave is the one outcome worth refusing.
import type { Metadata } from "next";

import { PageHeader } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { SIGN_IN_PROMISES } from "@/lib/identity";
import { requestSignInLinkAction, signOutAction } from "./actions";
import { SignInForm } from "./sign-in-form";

export const dynamic = "force-dynamic";

export const metadata: Metadata = {
  title: "Iniciar sesión",
};

export default function SignInPage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Identidad"
        title="Iniciar sesión"
        description="Un enlace de acceso, enviado a tu dirección. No se guarda ninguna contraseña, y comprar una entrada o abrirla no necesita cuenta: iniciar sesión solo hace que la lista de tus entradas la responda la taquilla en vez de guardarla este navegador."
      />

      <section aria-labelledby="sign-in-request" className="flex flex-col gap-4">
        <Card>
          <CardHeader>
            <CardTitle id="sign-in-request">Envíame un enlace</CardTitle>
            <CardDescription>
              Escribe la dirección con la que compraste. Enviamos un enlace a ella, y ese enlace
              es lo que te hace entrar.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <SignInForm requestSignInLink={requestSignInLinkAction} />
          </CardContent>
        </Card>

        <Alert variant="info" aria-labelledby="sign-in-promises">
          <h2 id="sign-in-promises" className="m-0 text-lg leading-tight font-semibold">
            Qué es este enlace y qué no es
          </h2>
          <AlertDescription className="text-sm">
            <ul className="m-0 flex list-disc flex-col gap-2 pl-5">
              {SIGN_IN_PROMISES.map((promise) => (
                <li key={promise}>{promise}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>

        <Card>
          <CardHeader>
            <CardTitle id="sign-out">¿Tienes sesión en este navegador?</CardTitle>
            <CardDescription>
              Cerrar sesión la termina en los dos extremos: este navegador suelta la cookie y la
              taquilla cierra la sesión, así que la credencial deja de valer en todas partes y no
              solo aquí. Si no se puede llegar a la taquilla, este navegador cierra la sesión
              igualmente, y terminará sola cuando caduque.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form action={signOutAction}>
              <Button type="submit" variant="outline">
                Cerrar sesión
              </Button>
            </form>
          </CardContent>
        </Card>
      </section>
    </main>
  );
}
