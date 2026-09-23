import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

/**
 * The mirror tracks the frames the page was sent, not just the boards it fetched, so a card moved by a
 * frame is in its new column on a mirror reload. A stale mirror shows it in the column it left.
 */

/** Read one task's status out of the mirrored board, as the next offline load will see it. */
async function mirroredStatus(page: Page, projectId: string, taskId: string): Promise<string | null> {
    return page.evaluate(({ pid, tid }) => new Promise<string | null>((resolve) => {
        const db = indexedDB.open('kanban-storage');
        db.onsuccess = () => {
            const store = db.result.transaction('board-cache', 'readonly').objectStore('board-cache');
            const req = store.get(`board:${pid}`);
            req.onsuccess = () => {
                const tasks = (req.result?.data as { tasks?: { id: string; status: string }[] })?.tasks;
                resolve(tasks?.find(t => t.id === tid)?.status ?? null);
            };
            req.onerror = () => resolve(null);
        };
        db.onerror = () => resolve(null);
    }), { pid: projectId, tid: taskId });
}

const column = (page: Page, title: string) => page.getByRole('list', { name: `${title} column` });

test.describe('The offline mirror follows the frames', () => {
    test('a card moved by a frame is in its new column after a mirror reload', async ({ page }) => {
        test.setTimeout(120000);
        await page.goto('/');
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
        await expect(page.locator('.offline-screen')).toBeVisible({ timeout: 15000 });
        await page.locator('.offline-btn').click();

        await expect(page.locator('.cache-banner')).toBeVisible({ timeout: 30000 });
        await expect(column(page, 'Review').locator(`.card[data-task-id="${taskId}"]`)).toBeVisible({ timeout: 15000 });
        await expect(column(page, 'Backlog').locator(`.card[data-task-id="${taskId}"]`)).toHaveCount(0);
    });
});
