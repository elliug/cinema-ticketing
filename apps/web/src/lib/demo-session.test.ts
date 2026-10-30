// Demonstration session (M3.5 · T3 · S2, S8).
//
// The session this milestone puts in the header is not authentication and must
// never be mistaken for it. What it does is narrow: it picks one of two fixed
// demo people and remembers the choice in this browser so the interface can call
// the visitor by a name. What it must never do is break a page or imply that a
// name protects anything, so every failure below — no storage, a storage that
// throws, a stored value this build does not know — has the same answer: the
// default person, and no exception.
//
// The module is storage-agnostic on purpose (lib/payment-key.ts is the house
// pattern): it is given a `getItem`/`setItem`/`removeItem` triple, so the rules
// are testable without a DOM and the one client component that renders the
// switcher is the only place that touches `window.sessionStorage`.
//
// The label is a single exported constant because it is the promise S8 makes: a
// copy of that sentence in a component could drift from the documented one, and
// a drift would be a claim about the system that is not true.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { DemoSessionStorage } from "./demo-session.ts";
import {
  DEMO_PEOPLE,
  DEMO_SESSION_DEFAULT_ID,
  DEMO_SESSION_LABEL,
  DEMO_SESSION_STORAGE_NAME,
  clearDemoSession,
  demoSessionStorage,
  findDemoPerson,
  readDemoSession,
  switchDemoSession,
} from "./demo-session.ts";

/** An in-memory stand-in for `window.sessionStorage`. */
function memoryStorage(initial?: Record<string, string>): DemoSessionStorage & {
  entries: Map<string, string>;
} {
  const entries = new Map<string, string>(Object.entries(initial ?? {}));
  return {
    entries,
    getItem: (key) => entries.get(key) ?? null,
    setItem: (key, value) => {
      entries.set(key, value);
    },
    removeItem: (key) => {
      entries.delete(key);
    },
  };
}

/** A storage that refuses every call, like a browsing mode that blocks it. */
function brokenStorage(): DemoSessionStorage {
  const refuse = () => {
    throw new Error("storage is not available in this browsing mode");
  };
  return { getItem: refuse, setItem: refuse, removeItem: refuse };
}

test("the two demo people are fixed, and each has a display name", () => {
  assert.deepEqual(
    DEMO_PEOPLE.map((person) => person.id),
    ["ana", "luis"],
  );
  assert.deepEqual(
    DEMO_PEOPLE.map((person) => person.displayName),
    ["Ana", "Luis"],
  );
  assert.equal(findDemoPerson("luis")?.displayName, "Luis");
  assert.equal(findDemoPerson("marta"), null);
  assert.equal(
    findDemoPerson(DEMO_SESSION_DEFAULT_ID)?.id,
    DEMO_SESSION_DEFAULT_ID,
    "the default person must be one of the two demo people",
  );
});

test("a first-time visitor is the default person, because nothing is stored", () => {
  const storage = memoryStorage();

  const person = readDemoSession(storage);

  assert.equal(person.id, "ana");
  assert.equal(person.displayName, "Ana");
});

test("switching stores the choice, so a reload shows the same name", () => {
  const storage = memoryStorage();

  const chosen = switchDemoSession(storage, "luis");
  // A reload is a second read of the same storage, with no other state.
  const afterReload = readDemoSession(storage);

  assert.equal(chosen.displayName, "Luis");
  assert.equal(afterReload.displayName, "Luis");
  assert.equal(storage.entries.get(DEMO_SESSION_STORAGE_NAME), "luis");
});

test("the stored choice is namespaced and is the only entry written", () => {
  const storage = memoryStorage();

  switchDemoSession(storage, "luis");

  assert.match(DEMO_SESSION_STORAGE_NAME, /^cinema\./);
  assert.deepEqual([...storage.entries.keys()], [DEMO_SESSION_STORAGE_NAME]);
});

test("switching to a person this build does not know keeps the active one", () => {
  const storage = memoryStorage();
  switchDemoSession(storage, "luis");

  const unchanged = switchDemoSession(storage, "marta");

  assert.equal(unchanged.displayName, "Luis");
  assert.equal(readDemoSession(storage).displayName, "Luis");
  assert.equal(
    storage.entries.get(DEMO_SESSION_STORAGE_NAME),
    "luis",
    "an unknown id must not be written over a valid one",
  );
});

test("clearing forgets the choice and returns to the default person", () => {
  const storage = memoryStorage();
  switchDemoSession(storage, "luis");

  const cleared = clearDemoSession(storage);

  assert.equal(cleared.id, DEMO_SESSION_DEFAULT_ID);
  assert.equal(readDemoSession(storage).id, DEMO_SESSION_DEFAULT_ID);
  assert.equal(storage.entries.has(DEMO_SESSION_STORAGE_NAME), false);
});

test("two browser profiles keep their own session, and one never sees the other", () => {
  const profileA = memoryStorage();
  const profileB = memoryStorage();

  switchDemoSession(profileA, "luis");
  switchDemoSession(profileB, "ana");

  assert.equal(readDemoSession(profileA).displayName, "Luis");
  assert.equal(readDemoSession(profileB).displayName, "Ana");

  clearDemoSession(profileA);

  assert.equal(profileA.entries.size, 0);
  assert.deepEqual(
    [...profileB.entries.values()],
    ["ana"],
    "clearing one profile must not touch another",
  );
});

test("a stored value this build does not know is ignored, never trusted", () => {
  for (const stored of ["marta", "", "   ", '{"id":"luis"}']) {
    const storage = memoryStorage({ [DEMO_SESSION_STORAGE_NAME]: stored });

    assert.equal(
      readDemoSession(storage).id,
      DEMO_SESSION_DEFAULT_ID,
      `stored value ${JSON.stringify(stored)} must fall back to the default`,
    );
  }
});

test("a storage that throws never breaks the page", () => {
  const storage = brokenStorage();

  assert.equal(readDemoSession(storage).id, DEMO_SESSION_DEFAULT_ID);
  // The click still names the person the visitor picked for this page load; only
  // a reload loses it, and then the default is the honest answer.
  assert.equal(switchDemoSession(storage, "luis").displayName, "Luis");
  assert.equal(clearDemoSession(storage).id, DEMO_SESSION_DEFAULT_ID);
});

test("no storage at all is the same answer as a broken one", () => {
  assert.equal(readDemoSession(null).id, DEMO_SESSION_DEFAULT_ID);
  assert.equal(switchDemoSession(null, "luis").displayName, "Luis");
  assert.equal(clearDemoSession(null).id, DEMO_SESSION_DEFAULT_ID);
});

test("a session storage getter that throws is treated as no storage at all", () => {
  const blocked = {
    get sessionStorage(): DemoSessionStorage {
      throw new Error("access to storage is denied");
    },
  };

  assert.equal(demoSessionStorage(blocked), null);
  assert.equal(demoSessionStorage({}), null);
  assert.equal(demoSessionStorage(null), null);

  const storage = memoryStorage();
  assert.equal(demoSessionStorage({ sessionStorage: storage }), storage);
});

test("the label says this is a demonstration and grants no access", () => {
  assert.equal(typeof DEMO_SESSION_LABEL, "string");
  assert.match(DEMO_SESSION_LABEL.toLowerCase(), /demonstration/);
  assert.match(DEMO_SESSION_LABEL.toLowerCase(), /grants no access/);
  assert.equal(
    DEMO_SESSION_LABEL.includes("\n"),
    false,
    "the label is one line, so the header and the docs can quote it whole",
  );
});

test("the label never presents the session as an identity or a protection", () => {
  const lowercase = DEMO_SESSION_LABEL.toLowerCase();
  const forbidden = [
    "sign in",
    "sign-in",
    "signin",
    "log in",
    "log-in",
    "login",
    "account",
    "secure",
    "protect",
    "authenticat",
  ];

  for (const term of forbidden) {
    assert.equal(
      lowercase.includes(term),
      false,
      `the demo-session label must not use "${term}"`,
    );
  }
});
