// The origin the gate drives (M4.1 · T1 · S6).
//
// The M3.5 gate shipped blind in one direction: it drove `http://127.0.0.1:3000`, and
// loopback is a **secure context** in every browser, so everything the browser decides by
// origin behaved as it does behind TLS. Two real defects walked through that blind spot —
// `crypto.randomUUID` (undefined outside a secure context) and the ticket media linked at
// an absolute API address — and neither could have failed here.
//
// `scripts/verify_m35.sh` now serves the web on the machine's primary non-loopback IPv4
// address and refuses to fall back to loopback, so the gate drives an origin the browser
// does not consider secure. This spec is what makes that promise checkable from inside the
// browser the gate drives: if the gate is ever pointed at a secure origin again, the run
// fails here, in words, instead of quietly proving nothing.
//
// It is a normal spec on purpose. It runs with the rest of the suite, in every configured
// engine, against whatever `WEB_BASE_URL` names — no separate command, no opt-in flag that
// could be forgotten.
import { expect, test } from "@playwright/test";

/**
 * The half of every failure message that says what a secure origin costs, so a red run
 * explains itself without anybody reading this file: the insecure case — the only reason
 * this gate exists — is not being exercised.
 */
function insecureCaseNotExercised(origin: string): string {
  return (
    `the insecure case is no longer being exercised at ${origin}: a secure origin gates ` +
    "exactly what TLS does, so nothing this run does proves anything about the browser " +
    "behaviour the gate was made to catch"
  );
}

test("the gate drives an origin the browser does not treat as secure", async ({ page }) => {
  const response = await page.goto("/events");
  expect(response, "GET /events answered no response").not.toBeNull();
  expect(response?.status(), "GET /events status").toBe(200);

  const origin = await page.evaluate(() => window.location.origin);
  const host = await page.evaluate(() => window.location.hostname);

  // Checked before the browser is asked, because loopback is a secure context by
  // definition: naming the address that caused it is more useful than the property alone.
  expect(
    host,
    `the gate is pointed at a secure origin, loopback (${origin}) — ${insecureCaseNotExercised(origin)}`,
  ).not.toMatch(/^(localhost|127\.\d{1,3}\.\d{1,3}\.\d{1,3}|\[::1\]|::1)$/i);

  const isSecureContext = await page.evaluate(() => window.isSecureContext);
  expect(
    isSecureContext,
    `the gate is pointed at a secure origin (${origin}) — ${insecureCaseNotExercised(origin)}`,
  ).toBe(false);

  // The concrete API the blind gate shipped and the first defect it hides: the spec marks
  // `crypto.randomUUID` as secure-context-only, so its absence is the property, not an
  // accident of the product's code.
  const randomUUIDType = await page.evaluate(
    () => typeof (window.crypto as Crypto & { randomUUID?: unknown }).randomUUID,
  );
  expect(
    randomUUIDType,
    `the gate is pointed at a secure origin (${origin}), where crypto.randomUUID is "${randomUUIDType}" — ${insecureCaseNotExercised(origin)}`,
  ).toBe("undefined");
});
