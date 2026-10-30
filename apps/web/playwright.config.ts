// Playwright configuration for the M3.5 product gate (M3.5 · T9 · S7, M4.1 · T1 · S7).
//
// Two engines, not one: Chromium alone made "works in the browser" mean "works in
// Chromium", so the same suite now runs in Chromium and Firefox against the same web
// server. Both are driven by the gate's one `WEB_BASE_URL`; neither gets its own stack,
// its own timeouts or its own relaxations, because the question is whether the product
// works, not whether one engine is more forgiving.
//
// WebKit is deliberately absent: this slice closes the "one engine" gap, and Safari's
// engine needs its own decision (and its own risk of a gate that fails for reasons that
// have nothing to do with the product). `README.md` says so where the gaps are listed.
//
// Nothing here starts the stack. `scripts/verify_m35.sh` owns the three compose
// services, the throwaway database, the migrations, the seed, the bucket, the API and
// the web server, and it points this configuration at them through `WEB_BASE_URL`:
// a test runner that also booted a server would hide which of the two failed.
//
// `outputDir` is deliberately outside the repository. A failed run keeps its trace,
// screenshot and download under the directory the gate hands it in `PLAYWRIGHT_OUT`,
// and a run started by hand falls back to a temporary directory, so no artefact can
// be left behind inside the worktree. `next dev` is never used: the gate builds the
// web application once and serves that build, so the runner and a developer's dev
// server cannot collide over `.next`.
import { tmpdir } from "node:os";
import { join } from "node:path";
import { defineConfig, devices } from "@playwright/test";

/**
 * The web server the gate started, reached by the machine's own non-loopback address.
 *
 * A hand-run points this at `pnpm web:dev`, which listens on loopback — and loopback is a
 * secure context, so `e2e/insecure-origin.spec.ts` fails on it by design. That failure is
 * the gate's own promise being checked (S6): a run against a secure origin is a run that
 * does not exercise the insecure case.
 */
const WEB_BASE_URL = process.env.WEB_BASE_URL ?? "http://127.0.0.1:3000";

export default defineConfig({
  testDir: "./e2e",
  // The two scenarios that create a screening share one database and one seat map, so
  // they run one at a time rather than racing over the same room and the same seats.
  fullyParallel: false,
  workers: 1,
  // A retry would hide the flakiness this gate exists to find.
  retries: 0,
  forbidOnly: true,
  // The administration journey and the purchase it feeds are several screens long; the
  // budget is per test and generous enough for a slow CI runner.
  timeout: 180_000,
  expect: { timeout: 15_000 },
  reporter: [["list"]],
  outputDir: process.env.PLAYWRIGHT_OUT ?? join(tmpdir(), "cinema-m35-playwright"),
  use: {
    baseURL: WEB_BASE_URL,
    // A failure must be diagnosable from the artefact alone: the trace replays the
    // click path, the screenshot shows the screen, and both stay when a test fails.
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off",
    // The PDF link is a navigation to a non-HTML document: the browser has to hand it
    // to the download machinery for the scenario to be able to assert the file.
    acceptDownloads: true,
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
    {
      name: "firefox",
      use: { ...devices["Desktop Firefox"] },
    },
  ],
});
