// The idempotency key must be generated on any origin, including the insecure ones where
// `crypto.randomUUID` is absent — which is exactly where a checkout used to break.
import assert from "node:assert/strict";
import { getRandomValues } from "node:crypto";
import { test } from "node:test";

import { randomUUID, type RandomSource } from "./uuid.ts";

const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

test("it uses randomUUID when the origin provides one", () => {
  const source: RandomSource = { randomUUID: () => "from-the-platform" };
  assert.equal(randomUUID(source), "from-the-platform");
});

test("it builds a v4 UUID where randomUUID is absent, which is what an insecure origin looks like", () => {
  // A LAN or VPN address over plain HTTP hands the page a Crypto object without
  // randomUUID. That used to throw on the first click.
  const source: RandomSource = {
    getRandomValues: <T extends Uint8Array>(array: T): T => {
      for (let index = 0; index < array.length; index += 1) array[index] = 0xff;
      return array;
    },
  };
  const generated = randomUUID(source);
  assert.match(generated, UUID_V4);
  // All-ones input must still carry the version and variant nibbles rather than echoes.
  assert.equal(generated, "ffffffff-ffff-4fff-bfff-ffffffffffff");
});

test("two fallback keys differ, because a repeated key would answer a new request with an old result", () => {
  const source: RandomSource = { getRandomValues: (array) => getRandomValues(array) };
  const first = randomUUID(source);
  const second = randomUUID(source);
  assert.match(first, UUID_V4);
  assert.match(second, UUID_V4);
  assert.notEqual(first, second);
});

test("it still answers where neither generator exists", () => {
  assert.match(randomUUID({}), UUID_V4);
});
