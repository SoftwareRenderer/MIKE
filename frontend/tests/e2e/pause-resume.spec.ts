import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, waitForChatForm, ensureTaskStarted, waitForTaskState, isolatedProject, gotoProject, cardInColumn } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


/**
 * PAUSE / RESUME
 *
 * Stopping an in-progress task (card ⏸ or chat ■) transitions it to a visible
 * Paused state: it stays in In Progress, shows a "Paused" badge, and offers a
 * Resume affordance (card play button / chat Resume button over the input).
 * Resuming re-runs the last turn (slow mock fixture), so the task is running
 * again.
 *
 * Uses prompts containing "stop-test" so the mock LLM serves the slow
 * text_response_slow fixture — the stream stays visible long enough to
 * interrupt it.
 */

test.describe.configure({ mode: 'serial' });

/** The status badge (the card may also carry a model badge). */
const CARD_BADGE = '.badge:not(.model-badge)';

/**
 * Click a card action icon (pause ⏸ / play ▶). The icons live in an
 * absolutely-positioned overlay above the title and only become interactive
 * on hover, while the title's padding-right (150ms transition) still animates
 * — until it settles, the title span intercepts the button's center. Poll the
 * real hit test (elementFromPoint) until the button is actually on top.
 */
async function clickCardAction(page: any, card: any, button: any, taskId: string, column: string, selector: string) {
  // Retry: a board re-render (title gen, state change) can swap the element
  // mid-click and restart the title-padding transition.
  for (let attempt = 0; attempt < 3; attempt++) {
    await card.hover();
    const hitOk = await page
      .waitForFunction(({ tid, col, sel }: { tid: string; col: string; sel: string }) => {
        // Column-scoped: the fading-out copy in the source column must not be
        // the button we hit-test.
        const btn = document.querySelector(`.column[aria-label="${col} column"] .card[data-task-id="${tid}"] ${sel}`);
        if (!btn) return false;
        const r = btn.getBoundingClientRect();
        const el = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return el === btn || (el && btn.contains(el));
      }, { tid: taskId, col: column, sel: selector }, { timeout: 3000 })
      .catch(() => false);
    if (!hitOk) continue;
    try {
      await button.click({ timeout: 3000 });
      return;
    } catch { /* intercepted or re-rendered — retry */ }
  }
  // Last resort: dispatch the click directly (same handler, no mouse hit test).
  await button.dispatchEvent('click');
}

const CHAT_RESUME_BTN = 'button[title="Resume the paused task"]';

test.describe('Pause / Resume', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('chat Stop pauses the task in place; Resume re-runs it', async ({ page }) => {
    test.setTimeout(60000);
    // "stop-test" → slow fixture; "slow-start" → +3s initial LLM delay,
    // widening the in-flight window the stop click must land in.
    const taskId = await createTaskViaApi(page, projectId, `stop-test slow-start: pause from chat ${Date.now()}`);
    await navigateToTask(page, taskId);
    // Clicks "Start Task" (backlog → in_progress) which starts the slow stream.
    await waitForChatForm(page);

    // Wait for the LLM to be streaming (stop button visible).
    await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 15000 });

    // Stop via chat ■.
    await page.locator('.btn-stop').click();

    // Server truth: stays in In Progress, execution_state = paused.
    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.status === 'in_progress' && t?.execution_state === 'paused',
    );

    // Task view header shows the "Paused" badge.
    await expect(page.locator('.task-detail-page .header .badge')).toHaveText('Paused', { timeout: 10000 });

    // Resume button is rendered over the chat input; the input is usable again.
    const resumeBtn = page.locator(CHAT_RESUME_BTN);
    await expect(resumeBtn).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.btn-stop')).toBeHidden();
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });

    // Resume → the last turn re-runs (slow fixture) → running again.
    await resumeBtn.click();
    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.execution_state === 'running' || t?.execution_state === 'pulling_image',
    );
    await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 15000 });
    await expect(page.locator('.task-detail-page .header .badge')).toHaveText('Running');

    // Cleanup: leave the task paused (no live executor, no gate slot held).
    await page.locator('.btn-stop').click();
    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.status === 'in_progress' && t?.execution_state === 'paused',
    );
  });

  test('card Pause pauses the task in place; card play resumes it', async ({ page }) => {
    test.setTimeout(60000);
    // "stop-test" → slow fixture; "slow-start" → +3s initial LLM delay,
    // widening the in-flight window the pause click must land in.
    const taskId = await createTaskViaApi(page, projectId, `stop-test slow-start: pause from card ${Date.now()}`);

    // Load the board FIRST, then start the task: the slow mock stream only
    // stays in-flight for ~2s, so the board must already be live when the
    // running state lands (no page reload between start and pause).
    await gotoProject(page);
    const backlogCard = cardInColumn(page, taskId, 'Backlog');
    await expect(backlogCard).toBeVisible({ timeout: 10000 });

    // Start via API (in_progress auto-start re-runs the prompt → slow stream).
    await expect(await ensureTaskStarted(page, projectId, taskId)).toBe(true);

    // The task stays in In Progress for the rest of the test — scope the
    // locator to that column so the fading-out Backlog copy can never match.
    const card = cardInColumn(page, taskId, 'In Progress');

    // Wait for the running state (pause button visible on the card).
    const pauseBtn = card.locator('button[aria-label="Pause task"]');
    await expect(pauseBtn).toBeVisible({ timeout: 15000 });
    await expect(card.locator(CARD_BADGE)).toHaveText('Running', { timeout: 15000 });

    await clickCardAction(page, card, pauseBtn, taskId, 'In Progress', 'button[aria-label="Pause task"]');

    // Server truth: stays in In Progress, execution_state = paused.
    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.status === 'in_progress' && t?.execution_state === 'paused',
    );

    // Card still in the In Progress column with the "Paused" badge
    // and a play (Resume) button.
    await expect(card).toHaveAttribute('aria-label', /Status: In Progress/, { timeout: 10000 });
    await expect(card.locator(CARD_BADGE)).toHaveText('Paused', { timeout: 10000 });
    const resumeBtn = card.locator('button[aria-label="Resume task"]');
    await expect(resumeBtn).toBeVisible();
    await expect(pauseBtn).toBeHidden();

    // Resume via card play → running again.
    await clickCardAction(page, card, resumeBtn, taskId, 'In Progress', 'button[aria-label="Resume task"]');
    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.execution_state === 'running' || t?.execution_state === 'pulling_image',
    );
    await expect(pauseBtn).toBeVisible({ timeout: 15000 });
    await expect(card.locator(CARD_BADGE)).toHaveText('Running');

    // Cleanup: leave the task paused (no live executor, no gate slot held).
    await clickCardAction(page, card, pauseBtn, taskId, 'In Progress', 'button[aria-label="Pause task"]');
    await waitForTaskState(
      page, projectId, taskId,
      (t) => t?.status === 'in_progress' && t?.execution_state === 'paused',
    );
  });
});
