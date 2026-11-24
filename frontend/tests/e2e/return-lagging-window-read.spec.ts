import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, clearTasksAfterEach, ownProjectId, createTaskViaApi, ensureTaskStarted,
  navigateToTask, waitForChatForm, watchTheReturn, comeBack,
  PENDING_TOOL_STATUS as PENDING, TOOL_CALL_CARD as CARD,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();
clearTasksAfterEach();

/**
 * A window read whose bytes are older than the answer the screen has already drawn: a read taken across the gap
 * between a `tool_result` frame and its row must not un-answer the call, or the card counts "Running for…" over
 * a tool that finished. `docs/reconnection.md → The row that lags the answer the socket gave`
 *
 * `long-running-probe` runs one bash call for 20s, which is the window this spec arranges its reads inside. The
 * call's output is echoed by its own command, so every claim about the answer is asked of the card's result
 * area, never of the card's text.
 */

const LONG_RUNNING_OUTPUT = 'LONG-RUNNING-PROBE-DONE';
const RESULT = '.tool-result';

/** The card whose result holds the probe's output — the answered reading of the call. */
const answeredCard = (page: Page) => page
  .locator(CARD)
  .filter({ has: page.locator(RESULT, { hasText: LONG_RUNNING_OUTPUT }) })
  .first();

/**
 * Answer the first window read after it goes out with the rows as they were *before* the call was answered: the
 * stored `tool_result` is taken off the call's row, and the turn's assistant row is given a sentence only these
 * bytes carry — the marker is what proves the screen was handed them, since a read that painted nothing, or an
 * answer that arrived too late to be at risk, cannot pass the assertions on it.
 *
 * The first read only: any later read answers normally, so the spec can say nothing else re-read while this one
 * was held.
 */
function serveWindowFromBeforeTheAnswer(page: Page, projectId: string, taskId: string, marker: string,
    readsServed: string[]): Promise<() => void> {
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => { release = resolve; });
  return page
    .route((url) => url.pathname === `/api/projects/${projectId}/tasks/${taskId}/messages`, async (route) => {
      if (readsServed.push('read') > 1) return route.continue();   // only the one read is authored
      const response = await route.fetch();
      const rows = (await response.json()) as Array<Record<string, unknown>>;
      const call = rows.find(r => r.role === 'tool_call');
      if (!call) throw new Error('serveWindowFromBeforeTheAnswer: the read carries no tool call');
      delete call.tool_result;                       // the read was taken while the tool still ran
      const turn = rows.find(r => r.role === 'assistant');
      if (turn) turn.content = marker;               // and this is the only source of that sentence
      await gate;                                     // applied when the spec says
      await route.fulfill({
        status: 200,
        headers: {
          'content-type': 'application/json',
          ...(response.headers()['etag'] ? { ETag: response.headers()['etag']! } : {}),
          ...(response.headers()['x-messages-more'] ? { 'X-Messages-More': response.headers()['x-messages-more']! } : {}),
          ...(response.headers()['x-messages-before'] ? { 'X-Messages-Before': response.headers()['x-messages-before']! } : {}),
        },
        body: JSON.stringify(rows),
      });
    })
    .then(() => release);
}

/** Make the return's digest fail: with nothing to compare against, the screen reads the rows outright. */
async function digestStopsAnswering(page: Page, projectId: string, taskId: string): Promise<void> {
  await page.route((url) => url.pathname === `/api/projects/${projectId}/tasks/${taskId}/sync`,
    route => route.abort('failed'));
}

/** The call's row, written when the tool starts. Read out of band, since the chat's own reads may be held. */
async function waitForToolRow(page: Page, projectId: string, taskId: string): Promise<void> {
  await expect.poll(async () => {
    const res = await page.request.get(`/api/projects/${projectId}/tasks/${taskId}/messages`);
    if (!res.ok()) return false;
    return ((await res.json()) as Array<{ role: string }>).some(r => r.role === 'tool_call');
  }, { timeout: 60_000, message: 'the probe turn never reached its tool call' }).toBe(true);
}

test.describe('A returning window reads a call its socket already answered', () => {
  test('the answer the socket gave stands over the row that has not caught up with it', async ({ page }) => {
    test.setTimeout(150_000);
    await watchTheReturn(page);          // before the app boots: it installs the return's hooks
    const projectId = ownProjectId();
    const prompt = `lagging-window-read long-running-probe ${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, prompt);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForToolRow(page, projectId, taskId);

    await navigateToTask(page, taskId, projectId);
    await waitForChatForm(page);
    await expect(page.locator(PENDING).first(), 'the call the rows hold open was never drawn')
      .toBeVisible({ timeout: 30_000 });

    // The window stops being read; the socket keeps talking. Past the return's own dedupe window and the
    // reconcile the open started, either of which would swallow this gesture.
    await page.evaluate(() => { (window as any).__unfocused = true; });
    await page.waitForTimeout(3000);
    await digestStopsAnswering(page, projectId, taskId);
    const marker = `lagging-window read ${Date.now()}`;
    const readsServed: string[] = [];
    const releaseRead = await serveWindowFromBeforeTheAnswer(page, projectId, taskId, marker, readsServed);

    // The return: the digest cannot answer, so the rows are read — and those bytes are the ones from before
    // the call was answered.
    await comeBack(page);
    await expect.poll(() => readsServed.length, {
      timeout: 15_000, message: 'the return read no rows, so this run arranged nothing',
    }).toBeGreaterThan(0);

    // The call answers while the read is held: the frame settles the card whose row is still open. Settled is
    // the state the read must not take away, so it is proved before the bytes are let through.
    await expect(answeredCard(page), 'the tool_result frame never reached this screen')
      .toBeVisible({ timeout: 60_000 });
    await expect(answeredCard(page).locator(PENDING), 'the call was still counting when the bytes were held, '
      + 'so the screen had no answer to lose').toHaveCount(0, { timeout: 15_000 });

    releaseRead();

    // Asked now, not eventually: a turn ending a moment later drops the card, which would answer "no pending
    // band" for the wrong reason. Scoped to this call's card — the turn carries on after the answer, and the
    // call it makes next is legitimately counting.
    await expect(answeredCard(page), 'the read that lagged the answer hid the result the screen held')
      .toBeVisible({ timeout: 5_000 });
    await expect(answeredCard(page).locator(PENDING), 'a call the socket answered is counting again')
      .toHaveCount(0, { timeout: 5_000 });
    // The marker is in no row and no frame: only the read the spec authored carries it, so seeing it says the
    // stale bytes were the thing applied.
    await expect(page.locator('.chat-content'), 'the read the spec authored was never applied')
      .toContainText(marker, { timeout: 5_000 });
    // Nothing re-read in between: the screen had only the held bytes and the frame to go on.
    expect(readsServed.length, 'another read landed, so the screen was not judged on the lagging bytes')
      .toBe(1);
  });
});
