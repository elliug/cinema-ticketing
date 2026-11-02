"use client";

// The three states of the page a sign-in link opens (identity-login T6 · S1, S2, S3).
//
// The page itself is a server component: it reads the request's cookie jar (a browser that
// already holds a session is sent straight to `/tickets`, so reloading a spent link does not read
// as a broken link) and it binds the token to the server action below. This component is where
// the outcome is drawn, and it exists only because a cookie can be *written* from an action and
// not from a render — the state it draws is decided in lib/identity.ts, tested there, and none of
// it is a claim this component invents.
//
// The form submits itself on arrival, which keeps the promised flow: follow the link, a session
// is written, the browser lands on the tickets. The button stays for a browser whose scripting is
// off — one click, the same action, the same outcome — and one pleasant consequence of doing it
// this way is worth keeping: a link preview or a mail scanner that fetches this address only ever
// receives the form. A `GET` cannot spend the link, so a robot reading the message cannot burn
// the sign-in of the person it was sent to.
//
// The states, and what each one refuses to say:
//
//   1. **checking** — nothing is known yet, and the page says that rather than a placeholder;
//   2. **unusable** — the opaque refusal, identical for a link that was spent, one that expired
//      and one that was never real, with the way forward: ask for a new link;
//   3. **unavailable** — ours, retryable, and never a verdict on the link.
//
// The session token never reaches this component: the action writes it into the cookie and
// redirects. What the view carries is the expiry, and not because this page prints it.
import Link from "next/link";
import { useActionState, useEffect, useRef } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { PublicResult, PublicSignInSession } from "@/lib/public-api";
import { SIGN_IN_PATH, TICKETS_PATH, describeSignInLink } from "@/lib/identity";

export interface SignInLinkSurfaceProps {
  /**
   * Server action: spends the link, writes the session cookie and redirects to `/tickets`.
   *
   * The token is bound to it on the server, and the bound argument is encrypted in the reference
   * this component receives — the browser never holds the token as a readable value.
   */
  redeem: () => Promise<PublicResult<PublicSignInSession>>;
  /** Whether the address carried a token at all. An empty one has nothing to spend. */
  hasToken: boolean;
}

export function SignInLinkSurface({ redeem, hasToken }: SignInLinkSurfaceProps) {
  const [result, submit, pending] = useActionState(
    async () => redeem(),
    null,
  );
  const form = useRef<HTMLFormElement | null>(null);
  const asked = useRef(false);

  useEffect(() => {
    if (!hasToken || asked.current) {
      return;
    }
    // One submission, even if React runs this effect twice: the link works once, so a second
    // automatic spend would answer `401` for a session the first one already opened.
    asked.current = true;
    form.current?.requestSubmit();
  }, [hasToken]);

  if (!hasToken) {
    return <MissingToken />;
  }

  const view = describeSignInLink(result);

  switch (view.kind) {
    case "checking":
      return (
        <Card>
          <CardHeader>
            <CardTitle>Checking this link</CardTitle>
            <CardDescription>
              The link is being spent at the box office. It expires in minutes and works once, so
              this takes one request.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form ref={form} action={submit} className="flex flex-col gap-3">
              <p role="status" className="m-0 text-sm text-muted-foreground">
                {pending ? "Signing you in…" : "Checking the link with the box office…"}
              </p>
              <div>
                <Button type="submit" disabled={pending}>
                  {pending ? "Signing in…" : "Continue"}
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>
      );
    case "opened":
      // The action redirects, so this state is what a redirect that did not happen would leave:
      // a session exists and this browser holds it.
      return (
        <Alert variant="success" role="status" aria-labelledby="sign-in-opened">
          <h2 id="sign-in-opened" className="m-0 text-lg leading-tight font-semibold">
            You are signed in
          </h2>
          <AlertDescription className="flex flex-col gap-2 text-sm">
            <p className="m-0">
              The link was spent and the session is in this browser now. It lasts until it
              expires, and then you ask for a new link — there is no silent renewal.
            </p>
            <div className="flex flex-wrap gap-2 pt-1">
              <Button asChild size="sm">
                <Link href={TICKETS_PATH}>My tickets</Link>
              </Button>
            </div>
          </AlertDescription>
        </Alert>
      );
    case "unusable":
      return (
        <Alert variant="warning" role="alert" aria-labelledby="sign-in-unusable">
          <h2 id="sign-in-unusable" className="m-0 text-lg leading-tight font-semibold">
            {view.headline}
          </h2>
          <AlertDescription className="flex flex-col gap-2 text-sm">
            <p className="m-0">{view.message}</p>
            <p className="m-0">{view.nextStep}</p>
            <div className="flex flex-wrap gap-2 pt-1">
              <Button asChild size="sm">
                <Link href={SIGN_IN_PATH}>Ask for a new link</Link>
              </Button>
              <Button asChild variant="outline" size="sm">
                <Link href={TICKETS_PATH}>My tickets</Link>
              </Button>
            </div>
          </AlertDescription>
        </Alert>
      );
    default:
      return (
        <Alert variant="destructive" role="alert" aria-labelledby="sign-in-unavailable">
          <h2 id="sign-in-unavailable" className="m-0 text-lg leading-tight font-semibold">
            {view.headline}
          </h2>
          <AlertDescription className="flex flex-col gap-2 text-sm">
            <p className="m-0">
              <code>{view.problem.code}</code> · HTTP {view.problem.status}
            </p>
            <p className="m-0">{view.message}</p>
            <p className="m-0">{view.nextStep}</p>
            <div className="flex flex-wrap gap-2 pt-1">
              <form action={submit}>
                <Button type="submit" size="sm" disabled={pending}>
                  {pending ? "Asking again…" : "Try again"}
                </Button>
              </form>
              <Button asChild variant="outline" size="sm">
                <Link href={SIGN_IN_PATH}>Ask for a new link</Link>
              </Button>
            </div>
          </AlertDescription>
        </Alert>
      );
  }
}

/** An address that carries no token at all: nothing to spend, and nothing to blame. */
function MissingToken() {
  return (
    <Alert variant="warning" role="alert" aria-labelledby="sign-in-missing">
      <h2 id="sign-in-missing" className="m-0 text-lg leading-tight font-semibold">
        This address carries no link
      </h2>
      <AlertDescription className="flex flex-col gap-2 text-sm">
        <p className="m-0">
          A sign-in link carries its own token in the address. Open it exactly as it arrived in
          the message, or ask for another one.
        </p>
        <div className="flex flex-wrap gap-2 pt-1">
          <Button asChild size="sm">
            <Link href={SIGN_IN_PATH}>Ask for a new link</Link>
          </Button>
        </div>
      </AlertDescription>
    </Alert>
  );
}
