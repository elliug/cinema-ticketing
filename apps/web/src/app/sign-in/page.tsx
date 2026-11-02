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
  title: "Sign in",
};

export default function SignInPage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Identity · T6"
        title="Sign in"
        description="A sign-in link, mailed to your address. No password is stored, and buying a ticket or opening one does not need an account at all — signing in only makes the list of your tickets something the box office answers instead of something this browser remembers."
      />

      <section aria-labelledby="sign-in-request" className="flex flex-col gap-4">
        <Card>
          <CardHeader>
            <CardTitle id="sign-in-request">Email me a link</CardTitle>
            <CardDescription>
              Type the address you bought with. We mail one link to it, and the link is what
              signs you in.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <SignInForm requestSignInLink={requestSignInLinkAction} />
          </CardContent>
        </Card>

        <Alert variant="info" aria-labelledby="sign-in-promises">
          <h2 id="sign-in-promises" className="m-0 text-lg leading-tight font-semibold">
            What this link is, and what it is not
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
            <CardTitle id="sign-out">Signed in on this browser?</CardTitle>
            <CardDescription>
              Signing out removes the session cookie from this browser. The session itself stays
              live at the box office until it expires — closing it there is the next piece of
              work, so this button says what it does and no more.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form action={signOutAction}>
              <Button type="submit" variant="outline">
                Sign out of this browser
              </Button>
            </form>
          </CardContent>
        </Card>
      </section>
    </main>
  );
}
