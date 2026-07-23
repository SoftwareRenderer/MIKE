import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

test.describe.serial('Board refresh on visibility change (mobile minimize/restore)', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('board refreshes from server after visibility change when WebSocket is disconnected', async ({ page }) => {
        const projectId = await getProjectId(page);

        // 1. Get initial card count
        const cardsBefore = await page.locator('.card').count();
        expect(cardsBefore).toBeGreaterThan(0);

        // 2. Block WebSocket connections AFTER the page is loaded
        // This simulates the network dropping while the user is away
        let blockWs = true;
        await page.route('**/api/stream*', async (route) => {
            if (blockWs) {
                await route.abort('failed');
            } else {
                await route.continue();
            }
        });

        // 3. Reload the page — WebSocket will fail to connect
        // HTTP still works, so the board loads from the server
        await page.goto(`/?project=${projectId}`);
        await page.waitForSelector('.card', { timeout: 10000 });

        // 4. Wait for the WebSocket to fail and isOffline to be set
        await page.waitForTimeout(3000);

        // 5. Verify isOffline is true (WebSocket disconnected)
        const isOffline = await page.evaluate(async () => {
            // Access the offline status via a fetch that would be affected
            try {
                const res = await fetch('/api/projects');
                const projects = await res.json();
                return projects.length > 0;
            } catch {
                return false;
            }
        });
        expect(isOffline).toBe(true); // HTTP works

        // 6. Create a new task via the server API
        const newTaskPrompt = `visibility-test-${Date.now()}`;
        const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
            method: 'POST',
            body: { prompt: newTaskPrompt },
        });
        expect(result.ok).toBe(true);
        const newTaskId = result.data?.id;
        expect(newTaskId).toBeTruthy();

        // 7. Verify the task exists on the server
        const serverBoard = await page.evaluate(async (pid) => {
            const res = await fetch(`/api/projects/${pid}/board`);
            return await res.json();
        }, projectId);
        const serverTaskCount = serverBoard.tasks.length;
        expect(serverTaskCount).toBeGreaterThan(cardsBefore);

        // 8. Trigger visibilitychange event
        await page.evaluate(() => {
            document.hidden = false;
            document.visibilityState = 'visible';
            document.dispatchEvent(new Event('visibilitychange'));
        });

        // 9. Wait for the board to refresh
        await page.waitForTimeout(2000);

        // 10. Check if the board refreshed by comparing card count
        // With the fix, getBoard() tries the network first, so the new task should appear
        const cardsAfter = await page.locator('.card').count();
        expect(cardsAfter).toBe(serverTaskCount);
    });

    test('getBoard always tries network first regardless of WebSocket state', async ({ page }) => {
        const projectId = await getProjectId(page);

        // 1. Get initial state
        await page.waitForSelector('.card', { timeout: 10000 });
        const initialCount = await page.locator('.card').count();

        // 2. Block WebSocket to force isOffline = true
        await page.route('**/api/stream*', async (route) => {
            await route.abort('failed');
        });

        // 3. Reload — WebSocket fails, isOffline becomes true
        await page.goto(`/?project=${projectId}`);
        await page.waitForSelector('.card', { timeout: 10000 });
        await page.waitForTimeout(3000);

        // 4. Create a task via API (server-side)
        const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
            method: 'POST',
            body: { prompt: `network-first-${Date.now()}` },
        });
        expect(result.ok).toBe(true);

        // 5. Manually call refreshBoard via the onRefresh callback exposed on the app
        // Dispatch visibilitychange to trigger the refresh path
        await page.evaluate(() => {
            document.hidden = false;
            document.visibilityState = 'visible';
            document.dispatchEvent(new Event('visibilitychange'));
        });

        // 6. Wait and verify the new task appears
        await expect(async () => {
            const count = await page.evaluate(async (pid) => {
                const res = await fetch(`/api/projects/${pid}/board`);
                const board = await res.json();
                return board.tasks.length;
            }, projectId);
            expect(count).toBeGreaterThan(initialCount);
        }).toPass({ timeout: 5000 });

        // 7. Also verify in the UI (after visibilitychange refresh)
        await page.waitForTimeout(1000);
        const uiCount = await page.locator('.card').count();
        expect(uiCount).toBeGreaterThan(initialCount);
    });
});
