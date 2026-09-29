import { defineConfig, devices } from '@playwright/test';
import { ISOLATED_SPECS, MAIN_LANE, isolatedLanes } from './tests/lanes';

// The chromium lane's own ceiling: measured, and the measurement is in docs/testing.md → Concurrency is
// part of the environment. The lane servers each run one worker, whatever this is.
const CHROMIUM_WORKERS = 4;

// One lane server per shard, each with one worker. Both the project list below and global-setup's servers
// are built from this array, so a lane with no project and a project with no lane are both impossible.
const ISOLATED_LANES = isolatedLanes();

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // Measured per full run: 6 workers ≈ 2.7 min with 1-4 failures that rotate between runs; 4 workers
  // ≈ 3.0-3.4 min with 0-1. They are all "waiting for an executor turn", and every worker queues
  // turns on the one lane server. Slower and boring beats faster and red.
  //
  // The cap across EVERY project, so this is the number of browsers the machine runs at once — and it
  // stays at the chromium lane's ceiling rather than becoming `4 + N`. Playwright walks its queue in
  // project order and creates a worker only when a slot is free, so a lane worker takes a slot when the
  // chromium lane has stopped using one: the shards start at the tail, run beside each other, and never
  // join the run early. The headroom of `4 + N` measures faster and is not available: 6 workers/2 shards
  // came in at 501.4 s with 21 unexpected, and 8 workers/4 shards at 451.4 s with 14 — where 24 of
  // `chat-core`'s tests were skipped after its first failed, so that run covered less rather than losing
  // less, and both widened runs lost a spec on a shard lane (ADR-0003).
  workers: CHROMIUM_WORKERS,
  globalSetup: './tests/global-setup.ts',
  globalTeardown: './tests/global-teardown.ts',
  reporter: 'list',
  // Median test is ~2 s and p90 ~8 s; anything near a minute is a broken test, and
  // a long ceiling just turns a dead spec into a long dead spec.
  // 30s was tight for the tests that wait on an executor turn: every worker shares one
  // server, so a turn queues behind the other workers'. Timeouts cost nothing when green.
  timeout: 45000,
  expect: {
    // A task turn queues behind every other worker's turn on the one shared server, so
    // assertions that wait on executor output need slack; they return as soon as they hold.
    timeout: 15000,
  },
  use: {
    baseURL: `http://localhost:${MAIN_LANE.port}`,
    trace: 'off',
  },
  projects: [
    {
      name: MAIN_LANE.name,
      use: {
          ...devices['Desktop Chrome'],
          storageState: MAIN_LANE.storageState,
      },
      workers: CHROMIUM_WORKERS,
      // Exclude config/state-modifying suites — the whole set runs on a lane server. Derived from
      // ISOLATED_SPECS, so the two lanes can never drift out of sync.
      testMatch: new RegExp(`/tests/e2e/(?!${ISOLATED_SPECS.join('|')}).*\\.spec\\.ts$`),
    },
    // Config/state-modifying suites, dealt across the shards by tests/lanes.ts. One worker per shard:
    // a shard mutates its own server's global state, and two workers on one shard would mutate it
    // against each other. The array is the rule — see docs/testing.md → E2E conventions.
    ...ISOLATED_LANES.map((lane) => ({
      name: lane.name,
      use: {
          ...devices['Desktop Chrome'],
          baseURL: `http://localhost:${lane.port}`,
          storageState: lane.storageState,
      },
      testMatch: lane.specs.map((s) => `**/${s}.spec.ts`),
      fullyParallel: false,
      workers: 1,
    })),
  ],
});
