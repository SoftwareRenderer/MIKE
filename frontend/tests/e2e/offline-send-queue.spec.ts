import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

/**
 * Message send failure due to a network issue:
 * - The message must be queued (persisted to the IndexedDB offline-queue), not lost.
 * - It stays visible as an outgoing (queued) message.
 * - A visible but NON-persisted error notice appears.
 * - The ReviewBar "Resume" button is displayed (last non-error entry is the queued user message).
 * - A reload while still offline shows the message again, still pending: it has no row, so the queue
 *   is its only record — clock, notice and Resume all come back from there.
 * - Once the network is restored and the queue flushes, the message is sent, the applied row is on
 *   screen (not only on the server), and the notice is gone.
 */

/** Read the offline-queue action count from IndexedDB. */
async function queuedActionCount(page: Page): Promise<number> {
    return page.evaluate(() => {
        return new Promise<number>((resolve) => {
            const db = indexedDB.open('kanban-storage');
            db.onsuccess = () => {
                const s = db.result.transaction('offline-queue', 'readonly').objectStore('offline-queue');
                const req = s.get('actions');
                req.onsuccess = () => resolve(Array.isArray(req.result) ? req.result.length : 0);
                req.onerror = () => resolve(0);
            };
            db.onerror = () => resolve(0);
        });
    });
}

test.describe.serial('Offline send queue', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('network-failed message send is queued, kept as outgoing, shows error + Resume, and flushes on reload', async ({ page }) => {
        test.setTimeout(120000);
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `offline-send-${Date.now()}`);

        // Move the task to review (same pattern as ui-modals.spec.ts) so the
        // ReviewBar is visible and the "Resume" button can appear.
        const move = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
            method: 'POST',
            body: { status: 'review' },
        });
        expect(move.ok, `move to review failed: ${JSON.stringify(move.data)}`).toBe(true);

        // Open the task chat — review bar and chat form must be ready.
        await navigateToTask(page, taskId, projectId);
        await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });
        await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });

        // Block the send endpoint to simulate a network failure on send.
        await page.route(`**/api/projects/*/tasks/*/send`, (route) => route.abort('failed'));

        // Send a message via the chat UI.
        const msgContent = `queued-msg-${Date.now()}`;
        const chatTextarea = page.locator('textarea[placeholder*="message"]');
        await chatTextarea.fill(msgContent);
        await chatTextarea.press('Enter');

        // 1. The message is NOT lost — it stays visible as an outgoing message with the queued indicator.
        const msgEntry = page.locator('.chat-message').filter({ hasText: msgContent });
        await expect(msgEntry).toBeVisible({ timeout: 10000 });
        await expect(msgEntry.locator('.queued-indicator')).toBeVisible();

        // 2. A visible error notice appears ("Connection lost — message queued...").
        const errorCard = page.locator('.error-card');
        await expect(errorCard).toBeVisible({ timeout: 10000 });
        await expect(errorCard).toContainText('queued');

        // 3. The "Resume" button is displayed (review bar, last non-error entry is the queued user message).
        await expect(page.locator('.chat-review-bar .btn-commit-done', { hasText: 'Resume' })).toBeVisible({ timeout: 10000 });

        // 4. The action is persisted in the IndexedDB offline queue.
        await expect(async () => {
            expect(await queuedActionCount(page)).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        // 5. A reload while still offline shows it as pending again.
        await page.reload();
        await page.waitForSelector('.card', { timeout: 10000 });
        await navigateToTask(page, taskId, projectId);

        const reloadedMsg = page.locator('.chat-message').filter({ hasText: msgContent });
        await expect(reloadedMsg.first()).toBeVisible({ timeout: 15000 });
        await expect(reloadedMsg.first().locator('.queued-indicator')).toBeVisible();
        await expect(page.locator('.error-card', { hasText: 'queued' })).toBeVisible({ timeout: 10000 });
        await expect(page.locator('.chat-review-bar .btn-commit-done', { hasText: 'Resume' })).toBeVisible({ timeout: 10000 });

        // Restore the network and reload — the queue flushes on page load.
        await page.route(`**/api/projects/*/tasks/*/send`, async (route) => {
            await route.continue();
        });
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        await navigateToTask(page, taskId, projectId);

        // 6. The message is on the server, and on screen.
        await expect(async () => {
            const present = await page.evaluate(async ({ pid, tid, content }) => {
                const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
                if (!res.ok) return false;
                const data = await res.json();
                return Array.isArray(data) && data.some((m: any) => m.role === 'user' && m.content === content);
            }, { pid: projectId, tid: taskId, content: msgContent });
            expect(present).toBe(true);
        }).toPass({ timeout: 15000 });

        const applied = page.locator('.chat-message').filter({ hasText: msgContent });
        await expect(applied.first()).toBeVisible({ timeout: 15000 });
        await expect(applied.locator('.queued-indicator')).toHaveCount(0);

        // 7. The queue is drained.
        await expect(async () => {
            expect(await queuedActionCount(page)).toBe(0);
        }).toPass({ timeout: 10000 });

        // 8. The queued error notice is gone (client-side only — never persisted to the DB).
        await expect(page.locator('.error-card', { hasText: 'queued' })).toHaveCount(0);
    });
});
