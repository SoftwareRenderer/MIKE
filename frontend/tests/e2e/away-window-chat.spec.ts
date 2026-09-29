import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, ownProjectId, createTaskViaApi, ensureTaskStarted, navigateToTask,
  waitForChatForm, waitForTaskState,
} from './helpers';

isolatedProject();

// Three ways a window comes back having missed a whole turn: two arrive with a browser event, one is a page
// that was stopped and is thawed in silence. `docs/reconnection.md → The tab that comes back`.

const CARD = '.tool-call-card';
const OUTPUT = 'SLOW-TOOL-PROBE-DONE';
/** `taskIsInFlight`, spelled for a raw task row. */
const notInFlight = (t: any) => t?.execution_state !== 'running' && t?.execution_state !== 'pulling_image';

/** Longer than the probe's turn, and longer than any tick the app could blame on a slow frame. */
const FREEZE_MS = 25_000;

type Gesture = 'focus' | 'visible' | 'none';

/** Drop this task's `task_event` frames while `window.__away` is set, and answer `document.hasFocus()` from
 * `window.__unfocused`. A listener added in the wrapped constructor runs before the app assigns `onmessage`,
 * which is where a frame can be removed. */
async function missTheTurnWhileAway(page: Page, taskId: string): Promise<void> {
  await page.addInitScript((id) => {
    const w = window as any;
    w.__away = false;
    w.__unfocused = false;
    w.__swallowed = 0;
    w.__signals = { visibilitychange: 0, focus: 0, pageshow: 0 };
    for (const type of ['visibilitychange', 'focus', 'pageshow']) {
      window.addEventListener(type, () => { w.__signals[type]++; });
    }
    Object.defineProperty(document, 'hasFocus', {
        configurable: true,
        get: () => () => !w.__unfocused,
    });

    const RealWebSocket = window.WebSocket;
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        if (!String(url).includes('/api/stream')) return;
        this.addEventListener('message', (e: MessageEvent) => {
          let frame: any;
          try { frame = JSON.parse(String(e.data)); } catch { return; }
          if (w.__away && frame?.type === 'task_event' && frame?.task_id === id) {
            w.__swallowed++;
            e.stopImmediatePropagation();
          }
        });
      }
    };
  }, taskId);
}

const goAway = (page: Page, unfocused: boolean) =>
  page.evaluate((u) => { (window as any).__unfocused = u; (window as any).__away = true; }, unfocused);

/** End the absence. `none` is the case where the browser dispatches nothing at all. */
async function comeBack(page: Page, gesture: Gesture): Promise<void> {
  await page.evaluate((g) => {
    const w = window as any;
    w.__unfocused = false;
    w.__away = false;
    if (g === 'focus') window.dispatchEvent(new Event('focus'));
    // The browser fires this at `document`, where it bubbles as far as `window`.
    if (g === 'visible') document.dispatchEvent(new Event('visibilitychange', { bubbles: true }));
  }, gesture);
}

/** Stop the renderer — timers and frame delivery included — for `ms`, the way an OS stops a backgrounded
 * page. No event is dispatched on either edge. */
async function freezeRenderer(page: Page, ms: number): Promise<void> {
  await page.evaluate((d) => { const end = Date.now() + d; while (Date.now() < end) { /* stopped */ } }, ms);
}

async function waitForToolRow(page: Page, projectId: string, taskId: string): Promise<void> {
  await expect(async () => {
    const res = await page.request.get(`/api/projects/${projectId}/tasks/${taskId}/messages`);
    if (!res.ok()) throw new Error(`rows unreadable: ${res.status()}`);
    const rows: Array<{ role: string }> = await res.json();
    expect(rows.some(r => r.role === 'tool_call')).toBe(true);
  }).toPass({ timeout: 60_000 });
}

function annotate(label: string): void {
  console.log(`MEASURE ${label}`);
  test.info().annotations.push({ type: 'measurement', description: label });
}

/** Record what the page saw, so a green case says how it got there. */
async function annotateAway(page: Page, label: string): Promise<void> {
  const seen = await page.evaluate(() => ({
    signals: (window as any).__signals,
    swallowed: (window as any).__swallowed,
  }));
  annotate(`${label}: return signals=${JSON.stringify(seen.signals)} task frames dropped=${seen.swallowed}`);
  expect(seen.swallowed, `${label}: no frame was dropped, so this run proved nothing`).toBeGreaterThan(0);
}

/** Count this task's reads, in order and with their timing. A `threads` read beside the row means the chat
 * remounted rather than reconciled. */
function countReconcileReads(page: Page, projectId: string, taskId: string): () => string {
  const base = `/api/projects/${projectId}/tasks/${taskId}`;
  const names: Record<string, string> = {
    [base]: 'row',
    [`${base}/messages`]: 'messages',
    [`${base}/turns`]: 'turns',
    [`${base}/threads`]: 'threads',
  };
  const started = Date.now();
  const seen: string[] = [];
  page.on('request', (req) => {
    const path = new URL(req.url()).pathname;
    const name = names[path];
    if (!name) return;
    seen.push(`+${Date.now() - started}ms ${name}${new URL(req.url()).search}`);
  });
  return () => (seen.length ? seen.join(', ') : 'no read at all');
}

/** Whether the transcript holds the turn's card, failing softly so the measurement is still logged. */
async function cardReturned(page: Page): Promise<boolean> {
  return page.locator(CARD).filter({ hasText: OUTPUT }).first()
    .waitFor({ timeout: 20_000 }).then(() => true).catch(() => false);
}

test.describe('A window that came back having missed a turn', () => {
  test('an unfocused window that was never hidden: the window\'s own focus event', async ({ page }) => {
    test.setTimeout(180_000);
    const prompt = `away-unfocused slow-tool-probe ${Date.now()}`;
    const projectId = ownProjectId();
    const taskId = await createTaskViaApi(page, projectId, prompt);
    await missTheTurnWhileAway(page, taskId);
    await navigateToTask(page, taskId, projectId);
    await waitForChatForm(page);

    // Away before the turn's first frame: the screen never sees the call it is missing.
    await goAway(page, true);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForToolRow(page, projectId, taskId);
    await waitForTaskState(page, projectId, taskId, notInFlight, 90_000);
    await annotateAway(page, 'unfocused / focus event');

    await expect(page.locator(CARD)).toHaveCount(0);

    const reads = countReconcileReads(page, projectId, taskId);
    await comeBack(page, 'focus');
    const returned = await cardReturned(page);
    annotate(`unfocused / focus event: ${reads()} card back=${returned}`);

    expect(returned, 'the missed turn never came back: a window that missed a whole turn cannot report '
      + 'that it is behind, because the turn left no pending card, no open assistant and no unresolved call '
      + 'behind it').toBe(true);
  });

  test('a page whose renderer was stopped, with no event on return', async ({ page }) => {
    test.setTimeout(180_000);
    const prompt = `away-frozen slow-tool-probe ${Date.now()}`;
    const projectId = ownProjectId();
    const taskId = await createTaskViaApi(page, projectId, prompt);
    await missTheTurnWhileAway(page, taskId);
    await navigateToTask(page, taskId, projectId);
    await waitForChatForm(page);

    await goAway(page, true);
    await ensureTaskStarted(page, projectId, taskId);

    // No card yet: every frame of the turn is dropped before the app sees it.
    await expect(page.locator(CARD)).toHaveCount(0);

    // The turn ends while the page is stopped, and the frames queued behind the freeze are still dropped
    // because the absence has not ended. Reads are counted from before it, since the clock may answer the
    // moment the page runs again.
    await waitForToolRow(page, projectId, taskId);
    const reads = countReconcileReads(page, projectId, taskId);
    await freezeRenderer(page, FREEZE_MS);
    await page.waitForTimeout(2000); // the turn must end while the renderer is still frozen, so the window is the subject
    await waitForTaskState(page, projectId, taskId, notInFlight, 90_000);
    await annotateAway(page, 'frozen renderer / no event');

    // The page was visible and focused on both sides of the freeze, so nothing is dispatched here.
    await comeBack(page, 'none');
    const returned = await cardReturned(page);
    annotate(`frozen renderer / no event: ${reads()} signals=${JSON.stringify(
      await page.evaluate(() => (window as any).__signals))} card back=${returned}`);

    expect(returned, 'nothing told a page whose clock jumped that it had been unread, so the transcript '
      + 'stayed where it was').toBe(true);
  });

  test('control: the same missed turn, returned to with visibilitychange', async ({ page }) => {
    test.setTimeout(180_000);
    const prompt = `away-hidden slow-tool-probe ${Date.now()}`;
    const projectId = ownProjectId();
    const taskId = await createTaskViaApi(page, projectId, prompt);
    await missTheTurnWhileAway(page, taskId);
    await navigateToTask(page, taskId, projectId);
    await waitForChatForm(page);

    await goAway(page, true);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForToolRow(page, projectId, taskId);
    await waitForTaskState(page, projectId, taskId, notInFlight, 90_000);
    await annotateAway(page, 'control: visibilitychange event');
    await expect(page.locator(CARD)).toHaveCount(0);

    const reads = countReconcileReads(page, projectId, taskId);
    await comeBack(page, 'visible');
    const returned = await cardReturned(page);
    annotate(`control: visibilitychange event: ${reads()} card back=${returned}`);

    expect(returned).toBe(true);
  });
});
