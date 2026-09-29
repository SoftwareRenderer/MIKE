import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, ownProjectId, createTaskViaApi, ensureTaskStarted,
  gotoProject, waitForChatForm, csrfFetch,
} from './helpers';

// A reopened chat has to ask for the server's replay: a call that started while its chat was closed is
// announced once, its row only says the tool began, and no later frame puts the card back. The row read is
// held shut during the away-and-back, so what the chat draws can only have come from the replay.
//
// The last describe is the mirror image: the rows are read and the announcement is the thing that is lost,
// so the rows are the only evidence the screen holds and the turn's own status frame must not undo them.

isolatedProject();

const CARD = '.tool-call-card';
const PENDING = '.tool-status.pending';
const NOTICE = '.history-notice';
const RETRY = '.history-notice-retry';
const OUTPUT = 'SLOW-TOOL-PROBE-DONE';
const LONG_RUNNING_OUTPUT = 'LONG-RUNNING-PROBE-DONE';

/** Delay the task's rows by `ms`: what the chat draws in that window came from the stream. */
async function holdRows(page: Page, projectId: string, taskId: string, ms: number) {
  await page.route(`**/api/projects/${projectId}/tasks/${taskId}/messages*`, async (route) => {
    await new Promise((r) => setTimeout(r, ms));
    await route.continue();
  });
}

/**
 * Wait for the call's row, written when the tool starts: from there its announcement exists only in the
 * server's replay buffer. Read out of band, since the chat's own read is what the spec blocks.
 */
async function waitForToolStart(page: Page, projectId: string, taskId: string): Promise<void> {
  await expect(async () => {
    const res = await page.request.get(`/api/projects/${projectId}/tasks/${taskId}/messages`);
    if (!res.ok()) throw new Error(`rows unreadable: ${res.status()}`);
    const rows: Array<{ role: string }> = await res.json();
    expect(rows.some(r => r.role === 'tool_call')).toBe(true);
  }).toPass({ timeout: 45_000 });
}

/**
 * Cut one task off from the socket, so its board copy stays stale: what the chat can know about it then
 * comes from its own REST reads.
 */
async function deafToTask(page: Page, taskId: string): Promise<void> {
  await page.addInitScript((id) => {
    const w = window as any;
    w.__deafFrames = 0;
    const RealWebSocket = window.WebSocket;
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        if (!String(url).includes('/api/stream')) return;
        this.addEventListener('message', (e: MessageEvent) => {
          let frame: any;
          try { frame = JSON.parse(String(e.data)); } catch { return; }
          const mine = frame?.type === 'board_refresh'
            || (frame?.type === 'task_updated' && frame?.task?.id === id)
            || (frame?.type === 'task_event' && frame?.task_id === id);
          if (mine) { w.__deafFrames++; e.stopImmediatePropagation(); }
        });
      }
    };
  }, taskId);
}

/**
 * Hold the turn's frames back by `delayMs` so the chat always reads its rows first, and lose this task's
 * `tool_call` frames on the way in. The announcement is broadcast once, so from here the rows are the
 * only evidence the screen will ever get that the call began.
 */
async function slowStreamLosingTheCall(page: Page, taskId: string, delayMs: number): Promise<void> {
  await page.addInitScript(({ id, delay }) => {
    const w = window as any;
    w.__lostAnnouncements = 0;
    const Real = window.WebSocket;
    class Slow extends Real {
      __h: ((e: MessageEvent) => void) | null = null;
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        if (!String(url).includes('/api/stream')) return;
        const self = this as any;
        Real.prototype.addEventListener.call(this, 'message', (e: MessageEvent) => {
          let frame: any;
          try { frame = JSON.parse(String(e.data)); } catch { /* not a frame this spec reads */ }
          if (frame?.type === 'task_event' && frame?.task_id === id && frame?.event === 'tool_call') {
            w.__lostAnnouncements++;
            e.stopImmediatePropagation();
            return;
          }
          setTimeout(() => { if (self.__h) self.__h.call(self, e); }, delay);
        });
        // The app assigns `onmessage`; a class field would be overwritten by it, so the socket's own
        // delivery goes through the same property the app writes.
        Object.defineProperty(this, 'onmessage', {
          get() { return self.__h; },
          set(fn: (e: MessageEvent) => void) { self.__h = fn; },
          configurable: true,
        });
      }
    }
    window.WebSocket = Slow as any;
  }, { id: taskId, delay: delayMs });
}

test.describe('Returning to a task whose turn ran while its chat was closed', () => {
  test('the tool call running in the background is on screen', async ({ page }) => {
    test.setTimeout(150_000);
    const projectId = ownProjectId();
    const stamp = Date.now();
    const prompt = `away-and-back slow-tool-probe ${stamp}`;
    const taskId = await createTaskViaApi(page, projectId, prompt);

    await gotoProject(page);
    const cardLink = page.locator('.card', { hasText: prompt }).first();
    await cardLink.click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });
    await waitForChatForm(page);
    await ensureTaskStarted(page, projectId, taskId);
    await expect(page.locator(CARD).first()).toBeVisible({ timeout: 45_000 });
    // The tool has started: from here its announcement exists nowhere but the replay buffer.
    await waitForToolStart(page, projectId, taskId);

    // Now the rows go out of reach: whatever the reopened chat draws came from the stream.
    await holdRows(page, projectId, taskId, 8000);

    await page.goBack();
    await expect(page.locator('.board')).toBeVisible({ timeout: 15_000 });
    await cardLink.click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });

    // Measured inside the hold: the rows cannot have answered this yet.
    await expect(page.locator(CARD).first(),
      'the call running while the chat was closed never came back: its frames went to a subscription the '
      + 'closed chat still held, so no replay was asked for, or the replayed announcement was swallowed '
      + 'as a duplicate of a card that had already been discarded').toBeVisible({ timeout: 3000 });
    await expect(page.locator(PENDING).first()).toBeVisible();

    // The same call, and it is the one that ran: the card is answered with the output it produced.
    await expect(page.locator(CARD).filter({ hasText: OUTPUT }).first()).toBeVisible({ timeout: 60_000 });
  });

  test('switching tasks and back brings the task you left with it', async ({ page }) => {
    test.setTimeout(150_000);
    const projectId = ownProjectId();
    const stamp = Date.now();
    const running = `switch-away slow-tool-probe ${stamp}`;
    const other = `switch-other ${stamp}`;
    const runningId = await createTaskViaApi(page, projectId, running);
    await createTaskViaApi(page, projectId, other);

    await gotoProject(page);
    await page.locator('.card', { hasText: running }).first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });
    await waitForChatForm(page);
    await ensureTaskStarted(page, projectId, runningId);
    await expect(page.locator(CARD).first()).toBeVisible({ timeout: 45_000 });
    await waitForToolStart(page, projectId, runningId);

    // Task A → B → A with A's rows out of reach: leaving A releases its key, so the return asks again.
    await holdRows(page, projectId, runningId, 25_000);
    await page.goBack();
    await expect(page.locator('.board')).toBeVisible({ timeout: 15_000 });
    await page.locator('.card', { hasText: other }).first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });
    await page.goBack();
    await expect(page.locator('.board')).toBeVisible({ timeout: 15_000 });
    await page.locator('.card', { hasText: running }).first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });

    await expect(page.locator(CARD).first(),
      'the task you left came back without the call it was running').toBeVisible({ timeout: 8000 });
    await expect(page.locator(CARD).filter({ hasText: OUTPUT }).first()).toBeVisible({ timeout: 60_000 });
  });
});

test.describe('A call whose announcement never reaches the screen', () => {
  // `long-running-probe` runs one bash call for 20s. The rows say the call began; the frames that
  // announced it reach a late screen as a replay only, and here that replay loses them — the shape a
  // trimmed buffer or a gated frame leaves behind.
  test('the rows alone keep the card, because the turn\'s status frame cannot undo a row', async ({ page }) => {
    test.setTimeout(150_000);
    const projectId = ownProjectId();
    const stamp = Date.now();
    const prompt = `lost-announcement long-running-probe ${stamp}`;
    const taskId = await createTaskViaApi(page, projectId, prompt);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForToolStart(page, projectId, taskId);

    await slowStreamLosingTheCall(page, taskId, 3000);
    await gotoProject(page);
    const cardLink = page.locator('.card', { hasText: prompt }).first();
    await expect(cardLink).toBeVisible({ timeout: 15_000 });
    await cardLink.click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });

    // Read from the rows, before the turn's replay has been read at all.
    await expect(page.locator(CARD).first(),
      'the rows never drew the call').toBeVisible({ timeout: 3000 });
    await expect(page.locator(PENDING).first()).toBeVisible();

    // Past the replay: a running frame reaching a screen that already read the rows used to delete the
    // entry it read, and nothing re-added it — the call ran to its end with no card at all.
    await expect
      .poll(() => page.evaluate(() => (window as any).__lostAnnouncements as number),
        { timeout: 30_000, message: 'no tool_call frame was ever dropped at this screen' })
      .toBeGreaterThan(0);
    await expect(page.locator(CARD).filter({ hasText: LONG_RUNNING_OUTPUT }).first()).toBeVisible({ timeout: 60_000 });
  });
});

test.describe('A board that never heard the task start', () => {
  // `long-running-probe` runs one bash call for 20s, so it stays unanswered across the whole reopen.
  test('the call is drawn as work, because the task row says it is', async ({ page }) => {
    test.setTimeout(150_000);
    const projectId = ownProjectId();
    const stamp = Date.now();
    const prompt = `stale-board long-running-probe ${stamp}`;
    const taskId = await createTaskViaApi(page, projectId, prompt);

    await deafToTask(page, taskId);
    await gotoProject(page);
    // The board's copy of this task is the one loaded here, and it is never updated again.
    const cardLink = page.locator('.card', { hasText: prompt }).first();
    await expect(cardLink).toBeVisible({ timeout: 15_000 });

    await ensureTaskStarted(page, projectId, taskId);
    await waitForToolStart(page, projectId, taskId);

    await cardLink.click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });

    await expect(page.locator(CARD).first(),
      'the task row says running and the board still says backlog: decided from the board copy, the rebuild '
      + 'treats the unanswered call as a leftover of a task that never started and hides its card').toBeVisible({ timeout: 10_000 });
    await expect(page.locator(PENDING).first()).toBeVisible();

    expect(await page.evaluate(() => (window as any).__deafFrames),
      'no frame about this task was swallowed, so the board was never stale and this run proved nothing')
      .toBeGreaterThan(0);
  });
});

test.describe('A conversation that could not be read says so', () => {
  test('a read that never reaches the server is reported, not shown as an empty task', async ({ page }) => {
    test.setTimeout(90_000);
    const projectId = ownProjectId();
    const stamp = Date.now();
    const prompt = `unreadable-history ${stamp}`;
    const taskId = await createTaskViaApi(page, projectId, prompt);
    const messages = `**/api/projects/${projectId}/tasks/${taskId}/messages*`;

    // Aborted from the first open, so this device holds no copy to fall back to either.
    await page.route(messages, route => route.abort('failed'));

    await gotoProject(page);
    await page.locator('.card', { hasText: prompt }).first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });

    await expect(page.locator(NOTICE)).toBeVisible({ timeout: 15_000 });

    // The notice is the way out: with the server reachable again, Retry re-reads and the notice ends.
    // The row is written after the abort is lifted because the route covers the write as well.
    await page.unroute(messages);
    const written = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST', body: { role: 'user', content: `row written during the outage ${stamp}` },
    });
    expect(written.ok).toBe(true);

    await page.locator(RETRY).click();
    await expect(page.locator(NOTICE)).toHaveCount(0);
    await expect(page.locator('.chat-entry', { hasText: `row written during the outage ${stamp}` })).toBeVisible();
  });

  test('a conversation served from this device is labelled as one', async ({ page }) => {
    test.setTimeout(90_000);
    const projectId = ownProjectId();
    const stamp = Date.now();
    const prompt = `mirror-history ${stamp}`;
    const taskId = await createTaskViaApi(page, projectId, prompt);

    await gotoProject(page);
    await page.locator('.card', { hasText: prompt }).first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });
    // The live read lands and caches the window: nothing to announce about it.
    await expect(page.locator('.chat-entry').first()).toBeVisible({ timeout: 15_000 });
    await expect(page.locator(NOTICE)).toHaveCount(0);

    // Now the server is unreachable, and the same screen opens on the stored copy.
    await page.route(`**/api/projects/${projectId}/tasks/${taskId}/messages*`, route => route.abort('failed'));
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator(NOTICE)).toBeVisible({ timeout: 15_000 });
  });
});
