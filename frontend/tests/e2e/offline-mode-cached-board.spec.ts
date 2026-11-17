import { test, expect, type Page } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, csrfFetch, isolatedProject, gotoProject, storageStatePath, readStored,
} from './helpers';

/**
 * The boot that answers from the mirror.
 *
 * Four things a real browser is the only witness to: the offline screen is offered by a timer while a
 * request is still out there; taking it answers from IndexedDB with no request in front of it; the loading
 * screen stays up over that read, so the empty app shell never paints; and the mirror holds what the socket
 * last delivered, not just what the last fetch returned.
 *
 * The flags behind those screens have their own unit files — `session-boot.unit.svelte.ts` owns the offer's
 * timer, its slow-not-gone wording, its mirror read and the socket the boot closes, and `board-mirror.unit.ts`
 * owns the mirror's coalesced write. What lives here is the composition: the real app booting against a
 * request that will not answer, a real IndexedDB, and real sockets counted off the wire.
 */

// This worker's own project, which `isolatedProject()` seeds before the test: the file that ran on this
// worker before this one leaves that project empty, and an empty board has no card for the waits below.
// See isolatedProject() in helpers.ts.
isolatedProject();

const OFFLINE_SCREEN = '.offline-screen';
const OFFLINE_BUTTON = '.offline-btn';
const SLOW_SESSION_MS = 6000; // past the 2 s offer, with room for the click in between

/** Read one task's status out of the mirrored board, as the next offline load will see it. */
async function mirroredStatus(page: Page, projectId: string, taskId: string): Promise<string | null> {
  const board = await readStored<{ data?: { tasks?: { id: string; status: string }[] } }>(
    page, 'board-cache', `board:${projectId}`);
  return board?.data?.tasks?.find(t => t.id === taskId)?.status ?? null;
}

/** What the mirror holds under a key, unwrapped from the envelope `storage.ts` writes. */
async function mirrored(page: Page, key: string): Promise<unknown> {
  return (await readStored<{ data?: unknown }>(page, 'board-cache', key))?.data ?? null;
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

/** Counts live `/api/stream` sockets: a socket the app no longer holds is still a socket. */
function countLiveStreamSockets(page: Page) {
  let live = 0;
  page.on('websocket', ws => {
    if (!ws.url().includes('/api/stream')) return;
    live++;
    ws.on('close', () => { live--; });
  });
  return () => live;
}

const column = (page: Page, title: string) => page.getByRole('list', { name: `${title} column` });

test.describe('The boot that answers from the mirror', () => {

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
    await expect(page.locator(OFFLINE_SCREEN)).toBeVisible({ timeout: 15000 });

    await watchScreens(page);
    await page.locator(OFFLINE_BUTTON).click();

    for (const task of tasks) {
      await expect(page.locator(`.card[data-task-id="${task.id}"]`)).toBeVisible({ timeout: 10000 });
    }
    // Cached data has to say so, even with the socket back up and every read answered from the mirror.
    await expect(page.locator('.cache-banner')).toContainText('Viewing cached data', { timeout: 10000 });
    expect(await screensSeen(page), 'the empty start pane painted while the mirror was read').not.toContain('start-pane');
  });

  test('a card moved by a frame is in its new column after a mirror reload', async ({ page }) => {
    test.setTimeout(120000);
    await gotoProject(page);
    await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });

    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `mirror-frames-${Date.now()}`);
    await expect(column(page, 'Backlog').locator(`.card[data-task-id="${taskId}"]`)).toBeVisible({ timeout: 15000 });

    // The move arrives over the socket; no board fetch happens here.
    const move = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    expect(move.ok, `move failed: ${JSON.stringify(move.data)}`).toBe(true);
    await expect(column(page, 'Review').locator(`.card[data-task-id="${taskId}"]`)).toBeVisible({ timeout: 15000 });

    // The write is coalesced, so wait for the mirror itself rather than a fixed delay.
    await expect
      .poll(() => mirroredStatus(page, projectId, taskId), {
        timeout: 20000,
        message: 'the mirror never took the frame: board:<projectId> still holds the old column',
      })
      .toBe('review');

    // Offline, then reload onto the mirror.
    await page.route('**/api/**', route => route.abort('failed'));
    await page.reload();
    await expect(page.locator(OFFLINE_SCREEN)).toBeVisible({ timeout: 15000 });
    await page.locator(OFFLINE_BUTTON).click();

    await expect(page.locator('.cache-banner')).toBeVisible({ timeout: 30000 });
    await expect(column(page, 'Review').locator(`.card[data-task-id="${taskId}"]`)).toBeVisible({ timeout: 15000 });
    await expect(column(page, 'Backlog').locator(`.card[data-task-id="${taskId}"]`)).toHaveCount(0);
  });

  test('a slow session answer is not an unreachable server: the offer comes down by itself', async ({ page }) => {
    // The first visit is what puts a projects list in the mirror, which is the only reason the
    // timer has anything to offer.
    await gotoProject(page);
    await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });

    const liveSockets = countLiveStreamSockets(page);
    await page.route('**/api/auth/session*', async route => {
      await new Promise(resolve => setTimeout(resolve, SLOW_SESSION_MS));
      await route.continue();
    });
    await page.reload();

    const offline = page.locator(OFFLINE_SCREEN);
    await expect(offline).toBeVisible({ timeout: 10000 });
    await expect(offline).toContainText('Taking longer than usual');
    await expect(offline).not.toContainText('Server unreachable');

    // The way this was hit: the user takes the mirror, and the session answer arrives while the
    // cached board is being painted.
    await page.locator(OFFLINE_BUTTON).click();

    await expect(page.locator('.card').first()).toBeVisible({ timeout: 30000 });
    await expect(offline).toHaveCount(0);

    await expect
      .poll(() => liveSockets(), { timeout: 15000, message: 'a second /api/stream socket was left open' })
      .toBe(1);
  });

  test('with nothing in the mirror, a slow answer is a wait and not a sign-in wall', async ({
    browser
  }, testInfo) => {
    // This worker's page has already opened the app, so its context holds a mirror. A browser that has
    // never seen this install is a context of its own: the same session, empty storage.
    const virgin = await browser.newContext({ storageState: storageStatePath(testInfo.project.name) });
    const tab = await virgin.newPage();
    await tab.route('**/api/auth/session*', async route => {
      await new Promise(resolve => setTimeout(resolve, 5000));
      await route.continue();
    });

    await tab.goto('/');
    await expect(tab.locator(OFFLINE_SCREEN)).toHaveCount(0);
    await expect(tab.locator('.loading-slow')).toBeVisible({ timeout: 10000 });

    await expect(tab.locator('.card').first()).toBeVisible({ timeout: 30000 });
    await expect(tab.locator('.loading-slow')).toHaveCount(0);
    await virgin.close();
  });
});
