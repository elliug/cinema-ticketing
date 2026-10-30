// A UUID v4 for values that must never repeat: the idempotency keys the hold and payment
// calls are addressed by.
//
// `crypto.randomUUID` exists only in a **secure context** — HTTPS, or a loopback host such
// as `localhost` or `127.0.0.1`. Open the application by a network address over plain HTTP
// (a LAN IP, a VPN address) and `crypto` is still there but `randomUUID` is not, so the
// first click of a checkout throws `crypto.randomUUID is not a function`. The browser gate
// never caught it because Playwright always navigates to loopback.
//
// `crypto.getRandomValues` has no such restriction, so the fallback builds a v4 UUID from
// it: the same randomness and the same shape, on any origin. `Math.random` is the last
// resort and is only reached where neither exists.
//
// The source is injectable so the fallback can be tested without pretending to be a
// browser.
export interface RandomSource {
  randomUUID?: () => string;
  getRandomValues?: <T extends Uint8Array>(array: T) => T;
}

export function randomUUID(source: RandomSource | undefined = globalThis.crypto): string {
  if (typeof source?.randomUUID === "function") {
    return source.randomUUID();
  }

  const bytes = new Uint8Array(16);
  if (typeof source?.getRandomValues === "function") {
    source.getRandomValues(bytes);
  } else {
    for (let index = 0; index < bytes.length; index += 1) {
      bytes[index] = Math.floor(Math.random() * 256);
    }
  }

  // The two version and variant nibbles of RFC 4122, so the result is a v4 UUID and not
  // merely sixteen random bytes wearing the shape of one.
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;

  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return [
    hex.slice(0, 8),
    hex.slice(8, 12),
    hex.slice(12, 16),
    hex.slice(16, 20),
    hex.slice(20),
  ].join("-");
}
