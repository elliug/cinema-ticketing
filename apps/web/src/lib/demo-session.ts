// Demonstration session (M3.5 · T3 · S2, S8).
//
// S2 asks for a browser-only session that picks one of two demo people and scopes
// what the interface shows, and for the interface to say what it is. This module
// is the whole of that state, and it is deliberately small, because the thing it
// must never do is look like authentication:
//
//   - it authenticates nobody. There is no password, no token, no identity check,
//     and **no server ever sees it**: the choice lives in this browser and nothing
//     downstream trusts it (L3, and L4 for the reservation read). Tickets and
//     reservations are addressable by their identifier, exactly as in M3, so a
//     forwarded link opens the same page whoever is "active" here;
//   - it is labelled wherever it is visible. [DEMO_SESSION_LABEL] is the one
//     sentence that says so, exported rather than copied, so the header and the
//     documentation cannot drift apart;
//   - storage failures never break a page. A browser that refuses storage, a
//     privacy mode that throws on the getter, and a stored value written by a
//     different build all answer the default person instead of raising.
//
// Real accounts (users, password hashing, sessions, ticket ownership and checks)
// are a backend milestone, not a UI task: see the milestone log, L3.
//
// Like lib/payment-key.ts, the module is handed its storage instead of reaching
// for `window`, so every rule below is testable without a DOM. The one client
// component that renders the switcher (components/site-header.tsx) is the only
// place that touches `window.sessionStorage`, through [demoSessionStorage].
//
// `sessionStorage` rather than `localStorage`: the choice is a demonstration of
// the interface and belongs to the tab that made it, so a shared machine does not
// inherit the person someone else left behind.

/** The storage surface this module needs, satisfied by `window.sessionStorage`. */
export interface DemoSessionStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

/**
 * The single storage key this module reads and writes.
 *
 * It carries the artifact ("demo-session") under the same `cinema.` namespace as
 * the payment key, so it can never be mistaken for anything else the tab stores.
 */
export const DEMO_SESSION_STORAGE_NAME = "cinema.demo-session";

/** One of the two demo people the milestone fixes. */
export interface DemoPerson {
  /** Stored id; stable, lowercase, and never shown as-is. */
  readonly id: string;
  /** The name the interface shows. */
  readonly displayName: string;
}

/**
 * The demo people, fixed and documented.
 *
 * Two of them, no more: the demonstration is a name on a page, not a directory.
 * Ana is the default, so a first-time visitor always sees a session rather than
 * an empty switcher.
 */
export const DEMO_PEOPLE: readonly DemoPerson[] = [
  { id: "ana", displayName: "Ana" },
  { id: "luis", displayName: "Luis" },
];

/** The id of the person shown when this browser holds no choice of its own. */
export const DEMO_SESSION_DEFAULT_ID = "ana";

/**
 * The sentence that says what the session is. One line, one constant.
 *
 * It must not use "sign in", "log in", "account", or imply protection (S2, S8),
 * and it must say plainly that the choice grants no access — which is the exact
 * consequence recorded in the milestone log, L3.
 */
export const DEMO_SESSION_LABEL =
  "Demonstration session: it grants no access, and nothing here checks who you are.";

/** The person a first-time visitor sees. */
export function defaultDemoPerson(): DemoPerson {
  return findDemoPerson(DEMO_SESSION_DEFAULT_ID) ?? DEMO_PEOPLE[0];
}

/** The demo person with this id, or `null` when this build does not know it. */
export function findDemoPerson(id: string): DemoPerson | null {
  return DEMO_PEOPLE.find((person) => person.id === id) ?? null;
}

/**
 * The active demo person of this browser, or the default one.
 *
 * Never throws: a storage that refuses the read, a `null` storage and a stored
 * value this build does not recognise all answer the default person, because a
 * header that cannot name a session is worse than one that names the default.
 */
export function readDemoSession(
  storage: DemoSessionStorage | null,
): DemoPerson {
  if (storage === null) {
    return defaultDemoPerson();
  }

  let stored: string | null;
  try {
    stored = storage.getItem(DEMO_SESSION_STORAGE_NAME);
  } catch {
    // A browsing mode that refuses storage simply has not chosen anyone.
    return defaultDemoPerson();
  }
  if (typeof stored !== "string") {
    return defaultDemoPerson();
  }
  return findDemoPerson(stored.trim()) ?? defaultDemoPerson();
}

/**
 * Makes one of the demo people active, and answers the person now shown.
 *
 * An id this build does not know changes nothing: an unknown stored value is
 * never written over a valid one. A storage that refuses the write still answers
 * the chosen person, because the switcher must reflect the click for this page
 * load — only a reload loses a choice that could not be stored.
 */
export function switchDemoSession(
  storage: DemoSessionStorage | null,
  personId: string,
): DemoPerson {
  const chosen = findDemoPerson(personId);
  if (chosen === null) {
    return readDemoSession(storage);
  }

  if (storage !== null) {
    try {
      storage.setItem(DEMO_SESSION_STORAGE_NAME, chosen.id);
    } catch {
      // Nothing to repair: this page load still shows the chosen name.
    }
  }
  return chosen;
}

/**
 * Forgets this browser's choice, so the next read is the default person.
 *
 * There is nothing else to clear: the session holds no credential, no server
 * record and no order state, so "clear" is exactly the removal of one key.
 */
export function clearDemoSession(
  storage: DemoSessionStorage | null,
): DemoPerson {
  if (storage !== null) {
    try {
      storage.removeItem(DEMO_SESSION_STORAGE_NAME);
    } catch {
      // An unreadable storage holds no usable choice either.
    }
  }
  return defaultDemoPerson();
}

/** The part of `window` this module reads; kept narrow so a test can fake it. */
export interface DemoSessionWindowLike {
  sessionStorage?: DemoSessionStorage;
}

/**
 * This browser's session storage, or `null` when it cannot be used.
 *
 * Reading `window.sessionStorage` itself can throw — a browsing mode may deny
 * access to the property, not only to its methods — so the access is guarded
 * here, once, instead of at every call site.
 */
export function demoSessionStorage(
  source: DemoSessionWindowLike | null | undefined,
): DemoSessionStorage | null {
  if (source === null || source === undefined) {
    return null;
  }
  try {
    return source.sessionStorage ?? null;
  } catch {
    return null;
  }
}
