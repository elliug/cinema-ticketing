"use client";

// The one form of the sign-in screen (identity-login T6 · S1, S2).
//
// Two things are the whole of it. The address goes to a **server action**, so the browser never
// learns where the API lives (the same rule `/t/[token]` follows), and what the form says comes
// from lib/identity.ts, where the rules are tested:
//
//   - one confirmation sentence for a link that was mailed, a request the hourly allowance
//     refused, and a counter that could not be read — because the route answers all three the
//     same way, and a screen that told them apart would be the oracle the route refuses to be;
//   - the API's own sentence for an address its rule refused, printed verbatim;
//   - our own failure, retryable, for a deployment that could not answer at all.
//
// Every failure leaves the button usable, so "send it again" is always one click away, and the
// pending state is the only time it is disabled.
//
// Nothing here reads the address back: it stays in the field a person typed it into, it is never
// put in the URL, never stored, and never echoed in a sentence — the confirmation cannot name it
// and a refusal carries only what the box office said.
import { useActionState } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { PublicResult, PublicSignInLinkRequested } from "@/lib/public-api";
import type { SignInRequestState } from "@/lib/identity";
import { describeSignInRequest } from "@/lib/identity";

export interface SignInFormProps {
  /** Server action: `POST /identity/links` with the address typed here. */
  requestSignInLink: (
    email: string,
  ) => Promise<PublicResult<PublicSignInLinkRequested>>;
}

export function SignInForm({ requestSignInLink }: SignInFormProps) {
  const [result, submit, pending] = useActionState(
    async (
      _previous: PublicResult<PublicSignInLinkRequested> | null,
      form: FormData,
    ) => requestSignInLink(String(form.get("email") ?? "")),
    null,
  );

  const state = describeSignInRequest(result, pending);

  return (
    <form action={submit} className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <Label htmlFor="sign-in-email">Email address</Label>
        <Input
          id="sign-in-email"
          name="email"
          type="email"
          required
          autoComplete="email"
          inputMode="email"
          spellCheck={false}
          aria-describedby="sign-in-email-hint"
        />
        <p id="sign-in-email-hint" className="m-0 text-sm text-muted-foreground">
          The address the link is mailed to. This page keeps no copy of it.
        </p>
      </div>

      <div>
        <Button type="submit" disabled={pending}>
          {pending ? "Sending…" : "Email me a link"}
        </Button>
      </div>

      <SignInRequestPanel state={state} />
    </form>
  );
}

/** The four states that follow a submission, in words from lib/identity.ts. */
function SignInRequestPanel({ state }: { state: SignInRequestState }) {
  switch (state.kind) {
    case "idle":
      return null;
    case "sending":
      return (
        <p role="status" className="m-0 text-sm text-muted-foreground">
          Asking the box office to mail the link…
        </p>
      );
    case "confirmed":
      return (
        <Alert variant="success" role="status" aria-labelledby="sign-in-confirmed">
          <h2 id="sign-in-confirmed" className="m-0 text-lg leading-tight font-semibold">
            {state.headline}
          </h2>
          <AlertDescription className="flex flex-col gap-2 text-sm">
            <p className="m-0">{state.message}</p>
            <p className="m-0">{state.nextStep}</p>
          </AlertDescription>
        </Alert>
      );
    case "refused":
      return (
        <Alert variant="warning" role="alert" aria-labelledby="sign-in-refused">
          <h2 id="sign-in-refused" className="m-0 text-lg leading-tight font-semibold">
            {state.headline}
          </h2>
          <AlertDescription className="flex flex-col gap-2 text-sm">
            <p className="m-0">{state.message}</p>
            <p className="m-0">{state.nextStep}</p>
          </AlertDescription>
        </Alert>
      );
    default:
      return (
        <Alert variant="destructive" role="alert" aria-labelledby="sign-in-unavailable">
          <h2 id="sign-in-unavailable" className="m-0 text-lg leading-tight font-semibold">
            {state.headline}
          </h2>
          <AlertDescription className="flex flex-col gap-2 text-sm">
            <p className="m-0">
              <code>{state.problem.code}</code> · HTTP {state.problem.status}
            </p>
            <p className="m-0">{state.message}</p>
            <p className="m-0">{state.nextStep}</p>
          </AlertDescription>
        </Alert>
      );
  }
}
