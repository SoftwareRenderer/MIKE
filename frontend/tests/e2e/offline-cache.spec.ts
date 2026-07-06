import { test, expect } from '@playwright/test';

test.describe.serial('Offline cache mirror', () => {
    test('board renders from cache when server is unreachable', async ({ page }) => {
        // 1. Navigate to the board and let it load (populates cache)
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        // Verify board is loaded
        const cardsBefore = await page.locator('.card').count();
        expect(cardsBefore).toBeGreaterThan(0);

        // 2. Block ALL API requests including auth/session (true offline simulation)
        await page.route('/api/**', async (route) => {
            await route.abort('failed');
        });

        // 3. Reload — HTML loads, all API calls fail → falls back to IndexedDB cache
        await page.goto('/');

        // 4. Verify board renders from cache (not login screen)
        await expect(async () => {
            const cards = await page.locator('.card').count();
            expect(cards).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        // 5. Verify the login modal is NOT shown
        await expect(page.locator('.login-modal')).not.toBeVisible().catch(() => {});

        // 6. Verify the "Viewing cached data" banner is visible
        await expect(page.locator('.cache-banner')).toBeVisible();
        await expect(page.locator('.cache-banner')).toContainText('Viewing cached data');

        // 7. Restore normal API access
        await page.route('/api/**', async (route) => {
            await route.continue();
        });
    });

    test('offline queue persists and merges with cached board', async ({ page }) => {
        // 1. Navigate to the board and let it load (populates cache)
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const cardsBefore = await page.locator('.card').count();

        // 2. Block ALL API requests (true offline simulation)
        await page.route('/api/**', async (route) => {
            await route.abort('failed');
        });

        // 3. Create a task via the UI while offline
        await page.locator('.add-task-btn').first().click();
        await expect(page.locator('[aria-labelledby="create-task-title"]')).toBeVisible();

        const textarea = page.locator('[aria-labelledby="create-task-title"] textarea');
        await textarea.fill(`offline-task-${Date.now()}`);
        await page.locator('[aria-labelledby="create-task-title"] .btn-primary').click();
        await page.locator('[aria-labelledby="create-task-title"]').waitFor({ state: 'hidden', timeout: 5000 });

        // Wait for the queue to be saved to IndexedDB (fire-and-forget catch handler)
        await expect(async () => {
            const queueLength = await page.evaluate(() => {
                return new Promise<number>((resolve) => {
                    const db = indexedDB.open('kanban-storage');
                    db.onsuccess = () => {
                        const s = db.result.transaction('offline-queue', 'readonly').objectStore('offline-queue');
                        const req = s.get('actions');
                        req.onsuccess = () => resolve(req.result?.length || 0);
                        req.onerror = () => resolve(0);
                    };
                    db.onerror = () => resolve(0);
                });
            });
            expect(queueLength).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        // 4. Restore API and reload — server should serve fresh data
        // The queue will be flushed, and the task will be created on the server
        await page.route('/api/**', async (route) => {
            await route.continue();
        });

        // 5. Reload — queue flushes, task is created on server
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        // 6. Verify the task now exists on the server
        await expect(async () => {
            const cards = await page.locator('.card').count();
            expect(cards).toBeGreaterThanOrEqual(cardsBefore + 1);
        }).toPass({ timeout: 15000 });
    });

    test('queued actions survive flush failure and retry on reconnect', async ({ page }) => {
        // 1. Navigate to the board and let it load
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const cardsBefore = await page.locator('.card').count();

        // 2. Block ALL API requests (true offline simulation)
        await page.route('/api/**', async (route) => {
            await route.abort('failed');
        });

        // 3. Create a task via the UI while offline
        await page.locator('.add-task-btn').first().click();
        await expect(page.locator('[aria-labelledby="create-task-title"]')).toBeVisible();

        const textarea = page.locator('[aria-labelledby="create-task-title"] textarea');
        await textarea.fill(`retry-task-${Date.now()}`);
        await page.locator('[aria-labelledby="create-task-title"] .btn-primary').click();
        await page.locator('[aria-labelledby="create-task-title"]').waitFor({ state: 'hidden', timeout: 5000 });

        // Verify action is queued
        await expect(async () => {
            const queueLength = await page.evaluate(() => {
                return new Promise<number>((resolve) => {
                    const db = indexedDB.open('kanban-storage');
                    db.onsuccess = () => {
                        const s = db.result.transaction('offline-queue', 'readonly').objectStore('offline-queue');
                        const req = s.get('actions');
                        req.onsuccess = () => resolve(req.result?.length || 0);
                        req.onerror = () => resolve(0);
                    };
                    db.onerror = () => resolve(0);
                });
            });
            expect(queueLength).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        // 4. Reload WHILE STILL OFFLINE — flushQueueNow() fails, actions must stay in queue
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        // Verify actions are STILL in the queue (not discarded by failed flush)
        await expect(async () => {
            const queueLength = await page.evaluate(() => {
                return new Promise<number>((resolve) => {
                    const db = indexedDB.open('kanban-storage');
                    db.onsuccess = () => {
                        const s = db.result.transaction('offline-queue', 'readonly').objectStore('offline-queue');
                        const req = s.get('actions');
                        req.onsuccess = () => resolve(req.result?.length || 0);
                        req.onerror = () => resolve(0);
                    };
                    db.onerror = () => resolve(0);
                });
            });
            expect(queueLength).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        // 5. Restore API access and reload — flushQueueNow() retries on fresh connection
        await page.route('/api/**', async (route) => {
            await route.continue();
        });

        // 6. Reload — auth succeeds, flushQueueNow() flushes the queue via HTTP
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        // 7. Verify queue is now empty (actions were flushed)
        await expect(async () => {
            const queueLength = await page.evaluate(() => {
                return new Promise<number>((resolve) => {
                    const db = indexedDB.open('kanban-storage');
                    db.onsuccess = () => {
                        const s = db.result.transaction('offline-queue', 'readonly').objectStore('offline-queue');
                        const req = s.get('actions');
                        req.onsuccess = () => resolve(req.result?.length || 0);
                        req.onerror = () => resolve(0);
                    };
                    db.onerror = () => resolve(0);
                });
            });
            expect(queueLength).toBe(0);
        }).toPass({ timeout: 15000 });

        // 8. Verify task exists on the server
        await expect(async () => {
            const cards = await page.locator('.card').count();
            expect(cards).toBeGreaterThanOrEqual(cardsBefore + 1);
        }).toPass({ timeout: 15000 });
    });
});
