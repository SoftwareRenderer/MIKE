import { test, expect } from '@playwright/test';
import { spawn, execSync } from 'node:child_process';
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  waitForChatForm,
  csrfFetch,
  waitForTaskState,
} from './helpers';

/**
 * PAUSE AFTER SERVER RESTART
 *
 * A task that was `running` when the server dies (hard kill — no graceful
 * shutdown) is recovered on startup: it transitions in_progress → paused and
 * is broadcast, so the board shows "Paused" + Resume immediately instead of a
 * phantom "Running" whose Stop 404s ("task not running").
 *
 * Runs on the ISOLATED server (port 3001, serialized 1-worker project): the
 * test kills and relaunches that server with the same data dir, mirroring the
 * global-setup spawn. The session/CSRF state lives in SQLite, so the existing
 * browser session stays valid across the restart.
 */

const __dirname = path.dirname(new URL(import.meta.url).pathname);
// Spec lives in frontend/tests/e2e/ → three levels up is the repo root
// (global-setup lives one level shallower, hence its '../..').
const PROJECT_ROOT = path.resolve(__dirname, '../../..');
const TEST_DATA_DIR = '/tmp/kanban-test-data-isolated';
const TEST_WORKTREES = '/tmp/kanban-test-worktrees';
const TEST_REGISTRY_URL = 'file://' + path.resolve(__dirname, '../registry-fixture.json');
const PORT = 3001;
const TEST_PASSWORD = 'test-password-1234';

test.describe.configure({ mode: 'serial' });

function serverEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    MIKE_WORKTREES_DIR: TEST_WORKTREES,
    MIKE_LOGIN_PASSWORD: TEST_PASSWORD,
    MIKE_REGISTRY_URL: TEST_REGISTRY_URL,
    MIKE_TLS: '0',
    // This spec tests restart recovery, not stale-container GC: skip the
    // startup podman cleanup so the relaunch is fast and deterministic
    // (podman is wedged/flaky in constrained environments).
    MIKE_SANDBOX_SKIP_STARTUP_CLEANUP: '1',
    DANGER_ZONE: '1',
  };
}

function pidsOnPort(port: number): string[] {
  // -sTCP:LISTEN: only the LISTENING process. Plain `lsof -ti:PORT` also
  // returns every CLIENT connection to the port — which would SIGKILL the
  // Playwright worker holding its own test page's socket.
  try {
    return execSync(`lsof -ti:${port} -sTCP:LISTEN`, { encoding: 'utf-8', timeout: 5000 })
      .split('\n').map((p) => p.trim()).filter((p) => p && p !== String(process.pid));
  } catch {
    return [];
  }
}

/** Hard-kill whatever is listening on the port (crash simulation). */
function killPort(port: number): void {
  for (const pid of pidsOnPort(port)) {
    try { process.kill(parseInt(pid, 10), 'SIGKILL'); } catch { /* already gone */ }
  }
}

async function waitPortFree(port: number, timeoutMs = 10000): Promise<void> {
  const start = Date.now();
  for (;;) {
    if (pidsOnPort(port).length === 0) return;
    if (Date.now() - start > timeoutMs) {
      throw new Error(`port ${port} still in use after ${timeoutMs}ms: ${pidsOnPort(port).join(',')}`);
    }
    await new Promise((r) => setTimeout(r, 250));
  }
}

const SERVER_LOG = '/tmp/restart-server.log';

function waitForServer(port: number, timeoutMs: number): Promise<void> {
  return new Promise((resolve, reject) => {
    const start = Date.now();
    let done = false;
    const finish = (err?: Error) => {
      if (done) return;
      done = true;
      if (err) reject(err);
      else resolve();
    };
    // Per-attempt deadline: a connection that is accepted but never answers
    // (or otherwise hangs) must not wedge the whole wait.
    const tryConnect = () => {
      if (done) return;
      const req = http.get(`http://localhost:${port}/`, () => {
        req.destroy(); // release the port socket
        finish();
      });
      req.on('error', () => {
        if (Date.now() - start < timeoutMs) {
          setTimeout(tryConnect, 200);
        } else {
          finish(new Error(`Server on port ${port} did not start within ${timeoutMs}ms`));
        }
      });
      req.setTimeout(5000, () => {
        req.destroy();
        if (Date.now() - start < timeoutMs) {
          setTimeout(tryConnect, 500);
        } else {
          finish(new Error(`Server on port ${port} hung on all attempts within ${timeoutMs}ms`));
        }
      });
      req.end();
    };
    tryConnect();
  });
}

/** Relaunch the isolated server exactly as global-setup does. */
function relaunchIsolatedServer(): void {
  const out = fs.openSync(SERVER_LOG, 'w');
  const srv = spawn('setsid', ['go', 'run', '.', '--data-dir', TEST_DATA_DIR, '--port', String(PORT)], {
    cwd: PROJECT_ROOT,
    env: serverEnv(),
    stdio: ['ignore', out, out],
    detached: true,
  });
  srv.unref();
}

test.describe('Pause after server restart', () => {
  test('in-flight task is recovered as Paused after a crash; chat Resume re-runs it', async ({ page }) => {
    // Generous: includes `go run` compile time for the relaunch.
    test.setTimeout(300000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    // "stop-test" → slow fixture; "slow-start" → +3s initial LLM delay, so the
    // task is guaranteed still in-flight when the server is killed.
    const taskId = await createTaskViaApi(page, projectId, `stop-test slow-start: pause after restart ${Date.now()}`);

    // Start the task (in_progress auto-start re-runs the prompt) and wait for running.
    const move = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    expect(move.ok).toBe(true);
    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.execution_state === 'running' || t?.execution_state === 'pulling_image',
    );

    // Crash the isolated server while the task is in flight.
    killPort(PORT);
    await waitPortFree(PORT);

    // Relaunch with the same data dir (sessions + config survive).
    relaunchIsolatedServer();
    // `go run` may compile; allow plenty of time.
    await waitForServer(PORT, 180000);

    // Reload: startup recovery has transitioned the stale running row to
    // paused — the board shows "Paused" in In Progress, not phantom "Running".
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 15000 });

    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.status === 'in_progress' && t?.execution_state === 'paused',
      15000,
    );

    // Column-scoped: the recovered task stays in In Progress for the rest of
    // the test (scoping also keeps the locator unambiguous if a column move
    // is still fading out when the board renders).
    const card = page.locator(`.column[aria-label="In Progress column"] .card[data-task-id="${taskId}"]`);
    await expect(card).toBeVisible({ timeout: 10000 });
    await expect(card).toHaveAttribute('aria-label', /Status: In Progress/);
    await expect(card.locator('.badge:not(.model-badge)')).toHaveText('Paused', { timeout: 10000 });
    await expect(card.locator('button[aria-label="Resume task"]')).toBeVisible();

    // Stop works now even though no live executor exists (pre-restart this
    // was the 404 "task not running" dead end) — and Resume re-runs the turn.
    await navigateToTask(page, taskId);
    await waitForChatForm(page);
    const resumeBtn = page.locator('button[title="Resume the paused task"]');
    await expect(resumeBtn).toBeVisible({ timeout: 10000 });
    await resumeBtn.click();
    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.execution_state === 'running' || t?.execution_state === 'pulling_image',
    );
    await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 15000 });

    // Cleanup: leave the task paused (no live executor, no gate slot held).
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' });
    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.status === 'in_progress' && t?.execution_state === 'paused',
    );
  });
});
