// The session cookie (identity-login T6 · S1, D2).
//
// The API mints a session token and answers it exactly once; this application is what puts it in
// a cookie, because the API lives on another origin and sets no cookie of its own. Everything
// about that cookie is a rule, not a detail:
//
//   - `HttpOnly`, so no script can read the token and an XSS cannot walk away with a session;
//   - `SameSite=Lax`, so another site cannot spend it, while a link followed from a mail still
//     arrives with the cookie attached on the navigation that opens it;
//   - `path=/`, so every server action presents it, from `/tickets` as much as from `/in/…`;
//   - `Secure` **only when the request itself arrived over https**. The local demo is `next dev`
//     on plain http, and a `Secure` cookie on an http response is not an error any browser
//     reports: it is a cookie the browser *silently never stores*. The sign-in would appear to
//     succeed, and every later page would find nobody signed in — a bug with no error message
//     anywhere. So the flag follows the request's own protocol rather than a build-time guess,
//     and one build is right on both;
//   - `maxAge`, taken from the `expiresAt` the API answered and floored to whole seconds. The
//     row's lifetime is the box office's decision; a lifetime invented here would be a second
//     opinion, and both ways of being wrong are bad — a cookie that outlives its row is a session
//     the server quietly refuses, and one that dies early signs somebody out for no reason.
//
// The token is a credential, and this module is the one place that handles it: never logged,
// never placed in a URL, never put in browser storage (an `HttpOnly` cookie exists precisely so
// it cannot be read there) and never handed to a client component. [readSessionToken] is what a
// server component uses to learn that somebody is signed in, without learning who.
//
// `next/headers` is imported inside each function instead of at the top of the file for one
// reason: the unit test beside this file is plain Node, which cannot resolve that extensionless
// specifier, and the rules above are worth testing. Nothing here is called outside a request, so
// the import always resolves to this application's own copy of Next.

/** The cookie's name. Namespaced like the other keys this application writes. */
export const SESSION_COOKIE_NAME = "cinema.session";

/** The attributes every write of the cookie carries. */
export interface SessionCookieOptions {
  httpOnly: true;
  sameSite: "lax";
  path: "/";
  secure: boolean;
  maxAge: number;
}

/**
 * The attributes of the session cookie, for an expiry the API answered.
 *
 * `now` is a parameter so the derivation is testable against a fixed clock; a caller never
 * passes one.
 */
export function sessionCookieOptions(
  expiresAt: string,
  secure: boolean,
  now: number = Date.now(),
): SessionCookieOptions {
  return {
    httpOnly: true,
    sameSite: "lax",
    path: "/",
    secure,
    maxAge: sessionMaxAge(expiresAt, now),
  };
}

/**
 * How long the cookie may live, in whole seconds.
 *
 * Floored, so the rounding can only ever bring the cookie's death forward: it must never outlive
 * the row it names. An instant already behind the clock — or one this side cannot read, which
 * the client refuses before it reaches here — is `0`, a cookie that dies at once. The other
 * answer, a cookie that never expires, is a credential nobody can recall.
 */
export function sessionMaxAge(expiresAt: string, now: number = Date.now()): number {
  const deadline = Date.parse(expiresAt);
  if (Number.isNaN(deadline)) {
    return 0;
  }
  return Math.max(0, Math.floor((deadline - now) / 1000));
}

/**
 * Whether the request this process is answering arrived over https.
 *
 * `x-forwarded-proto` is what a TLS-terminating proxy sets and what Next's own request pipeline
 * preserves; its first value is the protocol the browser actually used, the rest are the hops.
 * No header at all means no proxy, which is `next dev` and `next start` on plain http — the local
 * demo — and there `Secure` would lose the cookie, so it is false.
 */
export function isSecureRequest(headers: Headers): boolean {
  const forwarded = headers.get("x-forwarded-proto") ?? "";
  const protocol = (forwarded.split(",")[0] ?? "").trim().toLowerCase();
  return protocol === "https";
}

/**
 * Writes the session cookie after a spent sign-in link answered a token.
 *
 * The token arrives from `POST /identity/sessions` and from nowhere else, and its expiry is the
 * instant that answer carried: this function invents neither.
 */
export async function setSessionCookie(
  token: string,
  expiresAt: string,
): Promise<void> {
  const { cookies, headers } = await import("next/headers");
  const [jar, requestHeaders] = await Promise.all([cookies(), headers()]);
  jar.set(
    SESSION_COOKIE_NAME,
    token,
    sessionCookieOptions(expiresAt, isSecureRequest(requestHeaders)),
  );
}

/**
 * This request's session token, or `null` when this browser holds none.
 *
 * Absent and blank are the same answer: a cookie with an empty value names no session, and a page
 * that treated it as one would ask the API about a credential that cannot exist. The token is
 * returned to the caller that presents it to the API, and to nothing else.
 */
export async function readSessionToken(): Promise<string | null> {
  const { cookies } = await import("next/headers");
  const jar = await cookies();
  const token = (jar.get(SESSION_COOKIE_NAME)?.value ?? "").trim();
  return token === "" ? null : token;
}

/**
 * Removes the cookie from this browser.
 *
 * It expires the cookie rather than deleting it by name: a `Set-Cookie` with `Max-Age=0` and the
 * same path is the form every browser honours, and it targets exactly the cookie [setSessionCookie]
 * wrote. This closes **this browser's** copy of the session; closing the row at the box office is
 * `DELETE /api/v1/identity/sessions` (`SessionService.Close`), which the sign-out action calls
 * *before* it gets here. The order matters and is the opposite of convenience: revoking first means
 * a failure leaves the cookie in place to be retried, while clearing first would strand a live row
 * behind a browser that no longer remembers it.
 */
export async function clearSessionCookie(): Promise<void> {
  const { cookies, headers } = await import("next/headers");
  const [jar, requestHeaders] = await Promise.all([cookies(), headers()]);
  jar.set(SESSION_COOKIE_NAME, "", {
    ...sessionCookieOptions("", isSecureRequest(requestHeaders)),
  });
}
