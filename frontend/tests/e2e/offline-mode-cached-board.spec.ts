import { test, expect, type Page } from '@playwright/test';
import { getProjectId, gotoProject } from './helpers';

/**
 * The offline screen reads the mirror, so a request that hangs forever cannot sit between the click and the
 * cached board. The symptom was the empty start pane in its place.
 */

function mirrored(page: Page, key: string) {
  return page.evaluate((k) => new Promise<unknown>((resolve) => {
    const db = indexedDB.open('kanban-storage');
    db.onsuccess = () => {
      const store = db.result.transaction('board-cache', 'readonly').objectStore('board-cache');
      const req = store.get(k);
      req.onsuccess = () => resolve(req.result?.data ?? null);
      req.onerror = () => resolve(null);
    };
    db.onerror = () => resolve(null);
  }), key);
}

/** Note every screen painted from now on, so a brief flash of the empty one is still caught. */
function watchScreens(page: Page): Promise<void> {
  return page.evaluate(() => {
    const seen: string[] = [];
    (window as any).__screens = seen;
    const note = () => {
      if (document.querySelector('.start-pane') && !seen.includes('start-pane')) seen.push('start-pane');
    };
    note();
    new MutationObserver(note).observe(document.body, { childList: true, subtree: true });
  });
}

const screensSeen = (page: Page) => page.evaluate(() => (window as any).__screens as string[]);

test.describe('The offline screen paints what the mirror holds', () => {
  test('a request that hangs does not sit between the offline click and the cached board', async ({ page }) => {
    test.setTimeout(120000);
    await gotoProject(page);
    await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });
    const projectId = await getProjectId(page);

    // The mirror is written on a timer, so wait for the board itself rather than for a delay. The tasks
    // read once that wait closes are the ones the reload has to show.
    const mirroredBoardTasks = async () =>
      ((await mirrored(page, `board:${projectId}`)) as { tasks?: { id: string }[] } | null)?.tasks ?? [];
    await expect
      .poll(async () => (await mirroredBoardTasks()).length, {
        timeout: 20000,
        message: 'the mirror never took the board: board:<projectId> holds no task',
      })
      .toBeGreaterThan(0);
    const tasks = await mirroredBoardTasks();

    // Answer nothing: the session check hangs, so the boot offers the mirror, and the offline screen's own
    // reads would hang here too.
    await page.route('**/api/**', async route => {
      await new Promise(r => setTimeout(r, 30000));
      await route.abort('failed');
    });
    await page.reload();
    await expect(page.locator('.offline-screen')).toBeVisible({ timeout: 15000 });

    await watchScreens(page);
    await page.locator('.offline-btn').click();

    for (const task of tasks) {
      await expect(page.locator(`.card[data-task-id="${task.id}"]`)).toBeVisible({ timeout: 10000 });
    }
    // Cached data has to say so, even with the socket back up and every read answered from the mirror.
    await expect(page.locator('.cache-banner')).toContainText('Viewing cached data', { timeout: 10000 });
    expect(await screensSeen(page), 'the empty start pane painted while the mirror was read').not.toContain('start-pane');
  });
});
