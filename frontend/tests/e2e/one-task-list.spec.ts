import { test, expect, type Page } from '@playwright/test';
import { csrfFetch, editTaskTitle, getProjectId, goOnline, gotoProject, isolatedProject, queuedActionCount } from './helpers';

/**
 * The app holds one task list, and it is `appStore.board`'s own list (`appStore.tasks` reads it, never a
 * copy of it), so the cards on the board and any panel reading the same task answer from one value.
 * These two drive the writes that used to have two homes — a board move, and a card edit taken by the
 * persisted queue — and assert the board still draws what the task row carries afterwards.
 * `docs/reconnection.md`.
 */

isolatedProject();
// One project per worker, and the two tests work on its two seed tasks: serial, so neither can pick a
// card the other has just moved out of the column.
test.describe.configure({ mode: 'serial' });

const column = (page: Page, title: string) => page.getByRole('list', { name: `${title} column` });
const board = (page: Page) => page.locator('section[aria-label="Kanban board"]');
const COLUMN_TITLES: Record<string, string> = {
  backlog: 'Backlog', in_progress: 'In Progress', review: 'Review', done: 'Done',
};

/** End a drag the way the browser does: a drop on the target column carrying the id the card set there. */
async function dropOnColumn(page: Page, taskId: string, target: string) {
  await page.evaluate(({ tid, targetTitle }) => {
    const list = document.querySelector(`[role="list"][aria-label="${targetTitle} column"]`);
    const transfer = new DataTransfer();
    transfer.setData('text/plain', tid);
    list?.dispatchEvent(new DragEvent('drop', { bubbles: true, dataTransfer: transfer }));
  }, { tid: taskId, targetTitle: target });
}

async function taskIdsIn(page: Page, columnTitle: string): Promise<string[]> {
  const ids = await column(page, columnTitle).locator('.card').evaluateAll(cards =>
    cards.map(c => (c as HTMLElement).dataset.taskId));
  return ids.filter((id): id is string => !!id);
}

/** The edit dialog's answer for one card: the title field names the dialog, its footer names the action. */
async function openBoard(page: Page): Promise<string> {
  await gotoProject(page);
  await expect(board(page).locator('.card').first()).toBeVisible({ timeout: 15000 });
  return getProjectId(page);
}

test.describe('One task list', () => {
  test('the board draws the status the task row carries', async ({ page }) => {
    test.setTimeout(120000);
    const projectId = await openBoard(page);
    const [taskId] = await taskIdsIn(page, 'Backlog');
    expect(taskId).toBeTruthy();
    const rowUrl = `/api/projects/${projectId}/tasks/${taskId}`;

    await dropOnColumn(page, taskId, 'In Progress');
    await expect(column(page, 'Backlog').locator(`.card[data-task-id="${taskId}"]`)).toHaveCount(0, { timeout: 15000 });

    // The row may answer with another column than the drop asked for — a subtask consolidates under its
    // parent — so the claim is that the card sits where the row says, not where it was dropped.
    await expect
      .poll(async () => (await csrfFetch(page, rowUrl, { method: 'GET' })).data.status, { timeout: 20000 })
      .not.toBe('backlog');
    const status = (await csrfFetch(page, rowUrl, { method: 'GET' })).data.status as string;
    await expect(column(page, COLUMN_TITLES[status]).locator(`.card[data-task-id="${taskId}"]`)).toBeVisible();

    // The task view opened off that board reads the same task, not a second copy of it.
    await board(page).locator(`.card-open-link[href*="task=${taskId}"]`).click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15000 });
  });

  test('a card edit taken by the offline queue lands once when the server answers again', async ({ page }) => {
    test.setTimeout(120000);
    const projectId = await openBoard(page);
    const ids = await taskIdsIn(page, 'Backlog');
    // The test above moved one seed task out of the column; the one left is this test's card.
    expect(ids.length).toBeGreaterThan(0);
    const taskId = ids[0];
    const title = `queued-edit-${Date.now()}`;

    // No connection: the edit is intent, so it belongs to the persisted queue and to the board on screen.
    await page.context().setOffline(true);
    await editTaskTitle(page, taskId, title);
    await expect(column(page, 'Backlog').locator(`.card[data-task-id="${taskId}"]`)).toContainText(title, { timeout: 15000 });
    // Read from the store the queue lives in, before the page is thrown away: the value on the card proves only
    // that the app drew it, and a reload kills a request still running.
    await expect.poll(() => queuedActionCount(page), { timeout: 15000 }).toBe(1);

    // The connection returns: the queue flushes at boot, and one card carries the new title.
    await goOnline(page);
    await page.reload();
    await expect(board(page).locator(`.card[data-task-id="${taskId}"]`)).toContainText(title, { timeout: 30000 });
    expect(await board(page).locator(`.card[data-task-id="${taskId}"]`).count(), 'the queued write landed twice').toBe(1);
    const row = (await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, { method: 'GET' })).data as { title: string };
    expect(row.title).toBe(title);
  });
});
