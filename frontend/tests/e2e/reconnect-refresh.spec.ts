import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForSendButton, serverApi } from './helpers';

const BASE = 'http://localhost:3000';

/**
 * Verify that the UI updates automatically upon WebSocket reconnection.
 *
 * Since page.route doesn't intercept WebSockets in Playwright, the
 * spec wraps the WebSocket constructor via addInitScript to abort
 * /api/stream handshakes. The block flag is seeded in localStorage
 * to survive reloads. Server state is advanced via serverApi().
 */

async function installWsBlock(page: Page): Promise<void> {
    await page.addInitScript(() => {
        // Block /api/stream handshakes if the flag is set.
        // Seeded from localStorage to survive reloads.
        window.__wsBlock = { blocked: localStorage.getItem('__ws_blocked') === '1' };
        const RealWebSocket = window.WebSocket;
        window.WebSocket = class extends RealWebSocket {
            constructor(url, protocols) {
                super(url, protocols);
                if (window.__wsBlock.blocked && String(url).includes('/api/stream')) {
                    this.close();
                }
            }
        };
    });
}

async function enterOfflineState(page: Page): Promise<void> {
    // Seed block flag and reload to trigger the reconnecting banner.
    await page.evaluate(() => localStorage.setItem('__ws_blocked', '1'));
    await page.reload();
    await expect(page.locator('.reconnecting-banner')).toBeVisible({ timeout: 15000 });
}

async function restoreConnection(page: Page): Promise<void> {
    // Clear block flag to allow WebSocket reconnection.
    await page.evaluate(() => {
        (window as any).__wsBlock.blocked = false;
        localStorage.removeItem('__ws_blocked');
    });
}

test.describe.serial('Reconnection refreshes the current view', () => {
    test('board picks up tasks created while offline', async ({ page }) => {
        test.setTimeout(90000);
        await installWsBlock(page);
        // 1. Load board online.
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        const projectId = await getProjectId(page);
        const unique = `reconnect-board-${Date.now()}`;

        // 2. Go offline (WS down, HTTP up).
        await enterOfflineState(page);
        await page.waitForSelector('.card', { timeout: 15000 });

        // 3. Create task via server API.
        const created = await serverApi(BASE, `/api/projects/${projectId}/tasks`, {
            method: 'POST',
            body: { prompt: unique },
        });
        expect(created.ok, `task creation failed: ${JSON.stringify(created.data)}`).toBe(true);

        // 4. Task should not be visible yet.
        await expect(page.locator('.card', { hasText: unique })).toBeHidden();

        // 5. Restore connection.
        await restoreConnection(page);

        // 6. Board should refresh and show the new task.
        await expect(page.locator('.card', { hasText: unique })).toBeVisible({ timeout: 45000 });
        await expect(page.locator('.reconnecting-banner')).toBeHidden({ timeout: 15000 });
    });

    test('task view chat picks up messages that arrived while offline', async ({ page }) => {
        test.setTimeout(120000);
        await installWsBlock(page);
        // 1. Create + open task online.
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `reconnect-chat ${Date.now()}`);
        await navigateToTask(page, taskId, projectId);
        expect(await ensureTaskStarted(page, projectId, taskId)).toBe(true);
        await expect(page.locator('.chat-message.assistant')).toBeVisible({ timeout: 60000 });
        await waitForSendButton(page);
        const messagesBefore = await page.locator('.chat-message').count();
        expect(messagesBefore).toBeGreaterThan(0);

        // 2. Go offline. Chat loads history via HTTP; stream cannot connect.
        await enterOfflineState(page);
        // Sanity: the warmup turn is visible (loaded from the DB over HTTP).
        await expect(page.locator('.chat-message.assistant')).toBeVisible({ timeout: 15000 });

        // 3. Send message via server API.
        const marker = `offline-marker-${Date.now()}`;
        const sent = await serverApi(BASE, `/api/projects/${projectId}/tasks/${taskId}/send`, {
            method: 'POST',
            body: { content: marker },
        });
        expect(sent.ok, `send failed: ${JSON.stringify(sent.data)}`).toBe(true);

        // Wait for server-side turn completion.
        await expect(async () => {
            const res = await serverApi(BASE, `/api/projects/${projectId}/tasks/${taskId}/messages`);
            const messages: any[] = Array.isArray(res.data) ? res.data : [];
            const hasUser = messages.some((m) => m.role === 'user' && m.content === marker);
            const hasAssistant = messages.some((m) => m.role === 'assistant');
            expect(hasUser).toBe(true);
            expect(hasAssistant).toBe(true);
        }).toPass({ timeout: 30000 });

        // 4. Restore connection.
        await restoreConnection(page);

        // 5. Chat must update automatically on reconnect.
        await expect(page.locator('.chat-message.user', { hasText: marker })).toBeVisible({ timeout: 45000 });
        await expect(async () => {
            const count = await page.locator('.chat-message').count();
            expect(count).toBeGreaterThan(messagesBefore);
        }).toPass({ timeout: 15000 });
        await expect(page.locator('.reconnecting-banner')).toBeHidden({ timeout: 15000 });
    });
});
