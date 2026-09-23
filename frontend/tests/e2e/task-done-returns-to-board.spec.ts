import { test, expect, type Page } from '@playwright/test';
import {
  apiGetJson,
  clearBoardExcept,
  createTaskViaApi,
  csrfFetch,
  getProjectId,
  gotoProject,
  isolatedProject,
  navigateToTask,
  waitForWorktree,
  writeFileInWorktree,
} from './helpers';

// ============================================================
// A DONE CLICK IN THE TASK VIEW RETURNS TO THE BOARD
// ============================================================
// The review bar's three Done buttons all end with the task view closing over the board, where
// the card now sits: "→ Done" (a plain move), "Move to Done" (the same move on the primary
// button, with nothing staged) and "Commit and move to Done" (commit, merge, move).

isolatedProject();

const TASK_VIEW = '.task-detail-page';
const REVIEW_BAR = '.chat-review-bar';

/** The board card for a task — the card's own deep link, so it is unique without a title match. */
const cardOf = (taskId: string) => `.card-open-link[href*="task=${taskId}"]`;

async function statusOf(page: Page, projectId: string, taskId: string): Promise<string> {
  const task = await apiGetJson<any>(page, `/api/projects/${projectId}/tasks/${taskId}`);
  return task?.status || '';
}

/**
 * The review bar answers a task in review with "Resume" while the chat's last entry is a user
 * prompt, which is exactly what a fresh task's only entry is. Removing that entry through the
 * chat's own delete is what leaves the bar offering Done, and it keeps this spec off the
 * executor's turn queue — the shared mock LLM serialises turns across workers, and a spec about
 * navigation has no business waiting behind them.
 */
async function dropPromptEntry(page: Page, projectId: string, taskId: string): Promise<void> {
  const messages = await apiGetJson<any[]>(page, `/api/projects/${projectId}/tasks/${taskId}/messages`);
  const prompt = (messages || []).filter((m: any) => m.role === 'user').slice(-1)[0];
  expect(prompt, `task ${taskId} has no prompt entry to remove`).toBeTruthy();
  const removed = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
    method: 'DELETE',
    body: { message_id: prompt.id, delete_single: true },
  });
  expect(removed.ok).toBe(true);
}

/** Put the task in review and open it, waiting for the review bar to render. */
async function openInReview(page: Page, projectId: string, taskId: string): Promise<void> {
  await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
    method: 'POST',
    body: { status: 'review' },
  });
  await navigateToTask(page, taskId);
  await expect(page.locator(REVIEW_BAR)).toBeVisible({ timeout: 15000 });
}

/** Back on the board: no task view, no `task` param, and the task is a board card again. */
async function expectOnBoard(page: Page, taskId: string): Promise<void> {
  await expect(page.locator(TASK_VIEW)).toHaveCount(0);
  await expect(page).not.toHaveURL(/task=/);
  await expect(page.locator(cardOf(taskId))).toBeVisible();
}

/**
 * Column the narrow board comes to rest on.
 *
 * The scroll after a Done click is smooth and begins a tick after the task view closes, and at
 * `scrollLeft` 0 the column nearest the centre is already Backlog — so the first sample reads as
 * whatever the fresh board opens on. This waits for the scroller's position to stop moving, then
 * reads it: five unchanged samples, never fewer than five samples.
 */
async function restingColumn(page: Page): Promise<string | null> {
  return page.evaluate(async () => {
    const board = document.querySelector<HTMLElement>('.board');
    if (!board) return null;
    const nearest = (): string | null => {
      const centre = board.scrollLeft + board.clientWidth / 2;
      let winner: { label: string; distance: number; width: number } | null = null;
      for (const col of Array.from(document.querySelectorAll<HTMLElement>('.board > .column'))) {
        const distance = Math.abs(col.offsetLeft + col.clientWidth / 2 - centre);
        if (!winner || distance < winner.distance) {
          winner = { label: col.getAttribute('aria-label') || '', distance, width: col.clientWidth };
        }
      }
      return winner && winner.distance < winner.width / 2 ? winner.label.replace(' column', '') : null;
    };
    let still = 0;
    let last = -1;
    for (let i = 0; i < 50 && (still < 5 || i < 5); i++) {
      await new Promise((resolve) => setTimeout(resolve, 100));
      if (board.scrollLeft === last) still++;
      else {
        still = 0;
        last = board.scrollLeft;
      }
    }
    return nearest();
  });
}

test.describe.serial('Done returns to the board', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('the "→ Done" move leaves the task view', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `done-arrow-${Date.now()}`);
    await openInReview(page, projectId, taskId);

    await page.locator('.btn-move', { hasText: 'Done' }).click();

    await expectOnBoard(page, taskId);
    await expect.poll(() => statusOf(page, projectId, taskId), { timeout: 15000 }).toBe('done');
  });

  test('the "Move to Done" button leaves the task view', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `done-move-${Date.now()}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    await dropPromptEntry(page, projectId, taskId);
    await navigateToTask(page, taskId);
    await expect(page.locator(REVIEW_BAR)).toBeVisible({ timeout: 15000 });

    // The plain variant of the primary button: `.btn-commit-done` is also Resume and the commit
    // variant, and only this one moves without committing.
    const moveBtn = page.locator('.btn-commit-done:not(.btn-commit-and-done)', { hasText: /move to done/i });
    await expect(moveBtn).toBeVisible();
    await moveBtn.click();

    await expectOnBoard(page, taskId);
    await expect.poll(() => statusOf(page, projectId, taskId), { timeout: 15000 }).toBe('done');
  });

  test('the "Commit and move to Done" button leaves the task view', async ({ page }) => {
    // The prompt stays keyword-pinned: the move to in_progress hands it to the executor, and a
    // tool-call fixture would run inside this task's worktree.
    const taskId = await createTaskViaApi(page, projectId, `done-commit-${Date.now()} llm-fixture:text`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    await waitForWorktree(projectId, taskId);
    // Staged changes are what swap the plain Done button for the commit variant.
    writeFileInWorktree(projectId, taskId, 'returns-to-board.txt', 'staged for the commit\n');
    // Moving to review stops the executor, so the queued turn never reaches the chat.
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    await dropPromptEntry(page, projectId, taskId);
    await navigateToTask(page, taskId);
    await expect(page.locator(REVIEW_BAR)).toBeVisible({ timeout: 15000 });

    // The button renders once the diff stats report a staged file, and those arrive over the
    // board WebSocket behind the other workers' traffic.
    const commitBtn = page.locator('.btn-commit-and-done');
    await expect(commitBtn).toBeVisible({ timeout: 30000 });
    await commitBtn.click();

    const dialog = page.locator('[role="dialog"]').filter({ has: page.locator('.commit-textarea') });
    await expect(dialog).toBeVisible({ timeout: 30000 });
    // Filling the message also cancels the streaming generation, which is what frees Confirm.
    await dialog.locator('.commit-textarea').fill(`return to board ${Date.now()}`);
    const confirmBtn = dialog.locator('.modal-footer .btn-primary');
    await expect(confirmBtn).toBeEnabled({ timeout: 30000 });
    await confirmBtn.click();

    await expectOnBoard(page, taskId);
    await expect.poll(() => statusOf(page, projectId, taskId), { timeout: 30000 }).toBe('done');
  });
});

// A Done move leaves the board on Review when another task is waiting there, and on Backlog when
// none is — the finished card's own column is not where the next task comes from.
test.describe('Done lands the board on the next work', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('a Done move with another task in Review centres the Review column', async ({ page }) => {
    const projectId = await getProjectId(page);
    const waiting = await createTaskViaApi(page, projectId, `review-waiting-${Date.now()}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${waiting}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    const finishing = await createTaskViaApi(page, projectId, `review-finishing-${Date.now()}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${finishing}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    await navigateToTask(page, finishing, projectId);
    await expect(page.locator(REVIEW_BAR)).toBeVisible({ timeout: 15000 });
    await page.locator('.btn-move', { hasText: 'Done' }).click();

    await expectOnBoard(page, finishing);
    await expect.poll(() => restingColumn(page), { timeout: 10000 }).toBe('Review');
  });

  test('a Done move with nothing left in Review centres the Backlog column', async ({ page }) => {
    const projectId = await getProjectId(page);
    // A worker's project outlives the spec that filled it, and the board skips a scroll to an empty
    // column — so Review has to start empty and Backlog has to hold a card to land on.
    await clearBoardExcept(page, projectId, ['backlog']);
    await createTaskViaApi(page, projectId, `next-in-backlog-${Date.now()}`);
    const finishing = await createTaskViaApi(page, projectId, `review-only-${Date.now()}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${finishing}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    await navigateToTask(page, finishing, projectId);
    await expect(page.locator(REVIEW_BAR)).toBeVisible({ timeout: 15000 });
    await page.locator('.btn-move', { hasText: 'Done' }).click();

    await expectOnBoard(page, finishing);
    await expect.poll(() => restingColumn(page), { timeout: 10000 }).toBe('Backlog');
  });
});
