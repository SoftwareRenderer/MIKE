import { test, expect, type Page } from '@playwright/test';
import { createTaskViaApi, csrfFetch, getProjectId, navigateToTask } from './helpers';

/**
 * The bash card must stop showing "Running" when the command finishes — even when
 * the WebSocket dropped while the command was still in flight.
 *
 * Why the drop matters: on reconnect TaskChat reloads history, and the streaming
 * entry (temp id) is replaced by the DB-loaded one (message UUID, SAME tool_id).
 * Every `activeToolCalls` ref captured at tool_call time then points at a detached
 * copy, so `tool_result`/`tool_output` used to resolve to nothing — the card kept
 * spinning "Running for …" while the backend had long finished the command, and a
 * manual reload was the only cure.
 *
 * This spec pins the user-visible invariant (no card may outlive its command). The
 * discriminating coverage for the resolution logic itself is unit-level: the
 * "stale activeToolCalls ref after a history reload" block in
 * src/lib/services/__tests__/stream-handler.unit.ts — those cases fail against the
 * pre-fix handler, whereas from the browser several self-heals (the replayed
 * `status: running` frame clears `activeToolCalls`, which re-enables the old
 * scan-by-tool_id fallback) mask the defect.
 *
 * The live socket is closed through a wrapped WebSocket constructor (Playwright's
 * page.route cannot intercept WebSockets) — same technique as
 * reconnect-refresh.spec.ts. The fixture is routed by keyword in tests/mock-llm.js
 * (`bash-reconnect-probe` → sleep 6 + marker echo), which keeps the tool in flight
 * long enough to drop and reconnect.
 */

async function installSocketDrop(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const RealWebSocket = window.WebSocket;
    (window as any).__sockets = [] as WebSocket[];
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url as string, protocols);
        (window as any).__sockets.push(this);
      }
    };
    (window as any).__dropSockets = () => {
      for (const s of (window as any).__sockets) {
        try { s.close(); } catch { /* already gone */ }
      }
    };
  });
}

test.describe.serial('Bash card resolves after a mid-command WebSocket drop', () => {
  test('bash card clears "Running" once the result arrives after reconnect', async ({ page }) => {
    test.setTimeout(150000);
    await installSocketDrop(page);

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 15000 });
    const projectId = await getProjectId(page);

    const taskId = await createTaskViaApi(page, projectId, `bash-reconnect-probe-${Date.now()}: run the probe command`);
    const start = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
    expect(start.ok, `start failed: ${JSON.stringify(start.data)}`).toBe(true);

    await navigateToTask(page, taskId, projectId);

    // The bash card shows up and reports the command as running.
    const bashCard = page.locator('.tool-call-card', { hasText: 'E2E-BASH-CARD-DONE' }).first();
    await expect(bashCard).toBeVisible({ timeout: 45000 });
    await expect(bashCard.locator('.tool-status.pending')).toBeVisible({ timeout: 15000 });

    // Drop the live connection while the command still runs (sleep 6): the client
    // reconnects (~1s backoff) and reloads history in the middle of the turn, so the
    // pending card the user now sees is the DB-loaded one and the captured ref of
    // the streaming copy is stale.
    await page.evaluate(() => (window as any).__dropSockets());

    // The command has finished: the card must switch to Completed.
    await expect(bashCard.locator('.tool-status.success')).toBeVisible({ timeout: 90000 });
    // Nothing in this task's chat is left pretending to run.
    await expect(page.locator('.task-detail-page .tool-status.pending')).toHaveCount(0);

    // Cleanup: don't leave the task running for parallel suites.
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' }).catch(() => {});
  });
});
