// The session cookie (identity-login T6 · S1).
//
// The cookie this module writes carries a credential, so its shape is a rule and not a
// detail: `HttpOnly` so no script can read it, `SameSite=Lax` so another site cannot spend
// it, `path=/` so every server action presents it, and a lifetime taken from the instant the
// API answered rather than one invented here. `Secure` follows the request's protocol for a
// reason worth stating: the local demo runs on plain http, and a `Secure` cookie there is a
// cookie the browser silently never stores — a login that appears to work and signs nobody in.
//
// The rules are pure and are tested here. The three functions that reach `next/headers`
// ([setSessionCookie], [readSessionToken], [clearSessionCookie]) can only run inside a request,
// so they are not covered by a unit test and no test claims they are; what is pinned is the
// module's text, which is where the prohibitions live: no console call and no browser storage,
// because a token that reaches either has stopped being a credential.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  SESSION_COOKIE_NAME,
  isSecureRequest,
  sessionCookieOptions,
} from "./session.ts";

/** A fixed clock, so "the API's instant" is compared with a known now. */
const NOW = Date.parse("2026-03-01T10:00:00Z");

/** 30 days after NOW, the default `API_SESSION_TTL` the API hands out. */
const EXPIRES = "2026-03-31T10:00:00Z";

test("the cookie is named, HttpOnly, SameSite=Lax and scoped to the whole site", () => {
  const options = sessionCookieOptions(EXPIRES, true, NOW);

  assert.equal(SESSION_COOKIE_NAME, "cinema.session");
  assert.equal(options.httpOnly, true, "a token no script can read");
  assert.equal(options.sameSite, "lax", "another site must not be able to spend the session");
  assert.equal(options.path, "/", "every server action presents it, not only this folder");
});

test("Secure follows the request's protocol, so the plain-http demo still stores a cookie", () => {
  assert.equal(sessionCookieOptions(EXPIRES, true, NOW).secure, true);
  assert.equal(
    sessionCookieOptions(EXPIRES, false, NOW).secure,
    false,
    "a Secure cookie on plain http is never set, and the login would silently sign nobody in",
  );
});

test("maxAge is the API's expiry in whole seconds, never a lifetime invented here", () => {
  assert.equal(sessionCookieOptions(EXPIRES, true, NOW).maxAge, 30 * 24 * 60 * 60);
  // Seconds are floored: a cookie must never outlive the row that backs it, so the rounding
  // may only ever bring the death forward.
  assert.equal(sessionCookieOptions("2026-03-01T10:00:10.900Z", true, NOW).maxAge, 10);
});

test("an expiry at or behind now is a cookie that dies at once", () => {
  for (const expiresAt of ["2026-03-01T10:00:00Z", "2026-02-01T00:00:00Z"]) {
    assert.equal(sessionCookieOptions(expiresAt, true, NOW).maxAge, 0);
  }
});

test("an instant this side cannot read is a cookie that dies at once, never one that never ends", () => {
  // The client refuses a 201 whose `expiresAt` it cannot parse (lib/public-api.ts), so this
  // path is a caller's mistake. Both answers here would be a claim, and the safe one is the
  // one that ends the session: a cookie that never expires is a credential nobody can recall.
  assert.equal(sessionCookieOptions("not an instant", true, NOW).maxAge, 0);
  assert.equal(sessionCookieOptions("", true, NOW).maxAge, 0);
});

test("https is the only protocol that turns Secure on", () => {
  const headers = (proto?: string): Headers => {
    const carried = new Headers();
    if (proto !== undefined) {
      carried.set("x-forwarded-proto", proto);
    }
    return carried;
  };

  assert.equal(isSecureRequest(headers("https")), true);
  assert.equal(isSecureRequest(headers("HTTPS")), true);
  // A chain of proxies names the client's protocol first, which is the one the browser used.
  assert.equal(isSecureRequest(headers("https,http")), true);
  assert.equal(isSecureRequest(headers("http")), false);
  // The local demo: `next dev` on plain http and no proxy, so there is no header at all.
  assert.equal(isSecureRequest(headers()), false);
  assert.equal(isSecureRequest(headers("")), false);
});

test("the module never logs and never hands the token to browser storage", () => {
  const source = readFileSync(new URL("./session.ts", import.meta.url), "utf8");

  assert.equal(
    /console\s*\./.test(source),
    false,
    "a session token must never reach a console call, not even a debug one",
  );
  assert.equal(
    /localStorage|sessionStorage|document\.cookie/.test(source),
    false,
    "the token belongs in an HttpOnly cookie, not in anything a script can read",
  );
  // The cookie is written and read through `next/headers`, which is the one place in this
  // application that can set an HttpOnly cookie at all.
  assert.equal(source.includes("next/headers"), true);
});
