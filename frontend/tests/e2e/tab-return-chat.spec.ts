import { test, expect, type Page } from '@playwright/test';
import {
  createTaskViaApi, navigateToTask, waitForChatForm, waitForTaskState,
  isolatedProject, getProjectId, gotoProject,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

// `slow-tool-probe` streams one bash call, runs it, then answers in plain text — the shape a
// backgrounded tab lands in when the tool's result frame goes missing.

const PENDING = '.tool-status.pending';
const CARD = '.tool-call-card';
const OUTPUT = 'SLOW-TOOL-PROBE-DONE';
/** `taskIsInFlight`, spelled for a raw task row. */
const notInFlight = (t: any) => t?.execution_state !== 'running' && t?.execution_state !== 'pulling_image';

/**
 * Drops every `tool_result` task event before the app sees it: the socket stays up, the frames simply
 * never arrive. A listener added in the wrapped constructor runs before the app assigns `onmessage`.
 */
async function swallowToolResults(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as any;
    w.__swallowedToolResults = 0;
    const RealWebSocket = window.WebSocket;
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        if (!String(url).includes('/api/stream')) return;
        this.addEventListener('message', (e: MessageEvent) => {
          let frame: any;
          try { frame = JSON.parse(String(e.data)); } catch { return; }
          if (frame?.type === 'task_event' && frame?.event === 'tool_result') {
            w.__swallowedToolResults++;
            e.stopImmediatePropagation();
          }
        });
      }
    };
  });
}

test.describe('Returning to a tab that missed a tool result', () => {
  test('the stored result appears without a reload', async ({ page }) => {
    test.setTimeout(120000);
    await swallowToolResults(page);
    await gotoProject(page);
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `slow-tool-probe tab-return ${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await waitForChatForm(page);
    await expect(page.locator(PENDING)).toBeVisible({ timeout: 45000 });

    // The unanswered call takes its entry with it, leaving no trace of work that is in the database.
    await waitForTaskState(page, projectId, taskId, notInFlight, 60000);
    await expect(page.locator(CARD), 'the unanswered call should have left the screen').toHaveCount(0, { timeout: 15000 });
    expect(await page.evaluate(() => (window as any).__swallowedToolResults),
      'no tool_result frame was swallowed, so this run proved nothing').toBeGreaterThan(0);

    // The browser fires this at `document`, where it bubbles as far as `window`.
    await page.evaluate(() => {
      document.dispatchEvent(new Event('visibilitychange', { bubbles: true }));
    });

    const card = page.locator(CARD).first();
    await expect(card, 'the missed result never came back from the stored row').toContainText(OUTPUT, { timeout: 15000 });
    await expect(page.locator(PENDING)).toHaveCount(0);

    await page.reload();
    await expect(page.locator(CARD).first()).toContainText(OUTPUT, { timeout: 25000 });
  });
});
