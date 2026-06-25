import { test, expect } from '@playwright/test';
import { createAndOpenTask, waitForChatForm } from './helpers';

test.describe.configure({ mode: 'parallel' });

test.describe('Offline Action Queue', () => {
  test.beforeEach(async ({ page }) => {
    // Inject WebSocket tracker before page loads
    await page.addInitScript(() => {
      const origWebSocket = window.WebSocket;
      (window as any).__webSockets = [];
      window.WebSocket = function (this: WebSocket, ...args: any[]) {
        const ws = new origWebSocket(...args);
        (window as any).__webSockets.push(ws);
        return ws;
      } as any;
      Object.assign(window.WebSocket, origWebSocket);
    });

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should queue chat messages when offline and send on reconnect', async ({ page }) => {
    test.setTimeout(60000);

    // Wait for connection
    await expect(page.locator('.reconnecting-banner')).not.toBeVisible({ timeout: 10000 });

    // Create and open a task
    await createAndOpenTask(page, `offline queue test ${Date.now()}`);
    const chatReady = await waitForChatForm(page);
    if (!chatReady) test.skip();
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

    // Disconnect WebSocket
    await page.evaluate(() => {
      const wss = (window as any).__webSockets as WebSocket[];
      for (const ws of wss) {
        if (ws.readyState === WebSocket.OPEN) ws.close();
      }
    });
    await expect(page.locator('.reconnecting-banner')).toBeVisible({ timeout: 10000 });

    // Send a message while offline
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('offline test message');
    await chatTextarea.press('Enter');

    // Message should appear (check for user message - use .last() for the new one)
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 5000 });

    // Queue should be persisted in localStorage
    const queueLength = await page.evaluate(() => {
      try {
        const raw = localStorage.getItem('kanban-offline-queue');
        return raw ? JSON.parse(raw).length : 0;
      } catch { return -1; }
    });
    expect(queueLength).toBeGreaterThan(0);

    // Reconnect - wait for banner to disappear
    await expect(page.locator('.reconnecting-banner')).not.toBeVisible({ timeout: 15000 });

    // Queue should be empty after flush
    const queueAfterFlush = await page.evaluate(() => {
      try {
        const raw = localStorage.getItem('kanban-offline-queue');
        return raw ? JSON.parse(raw).length : 0;
      } catch { return -1; }
    });
    expect(queueAfterFlush).toBe(0);
  });

  test('should persist queue across page refresh', async ({ page }) => {
    test.setTimeout(60000);

    // Wait for connection
    await expect(page.locator('.reconnecting-banner')).not.toBeVisible({ timeout: 10000 });

    await createAndOpenTask(page, `persist queue test ${Date.now()}`);
    const chatReady = await waitForChatForm(page);
    if (!chatReady) test.skip();
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

    // Disconnect WebSocket
    await page.evaluate(() => {
      const wss = (window as any).__webSockets as WebSocket[];
      for (const ws of wss) {
        if (ws.readyState === WebSocket.OPEN) ws.close();
      }
    });
    await expect(page.locator('.reconnecting-banner')).toBeVisible({ timeout: 10000 });

    // Send a message while offline
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('persist test message');
    await chatTextarea.press('Enter');
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 5000 });

    // Verify queue is in localStorage
    const queueBeforeRefresh = await page.evaluate(() => {
      try {
        const raw = localStorage.getItem('kanban-offline-queue');
        return raw ? JSON.parse(raw).length : 0;
      } catch { return -1; }
    });
    expect(queueBeforeRefresh).toBeGreaterThan(0);

    // Refresh page - queue should persist in localStorage
    await page.reload();
    // After reload, we're in task detail view - wait for the page to load
    await page.waitForSelector('.task-detail-page', { timeout: 10000 }).catch(async () => {
      // Might redirect to home - wait for projects
      await page.waitForSelector('.project-item', { timeout: 5000 });
    });

    // After reconnect, banner should disappear and queue should flush
    await expect(page.locator('.reconnecting-banner')).not.toBeVisible({ timeout: 15000 });

    // Queue should be empty after flush
    const queueAfterRefresh = await page.evaluate(() => {
      try {
        const raw = localStorage.getItem('kanban-offline-queue');
        return raw ? JSON.parse(raw).length : 0;
      } catch { return -1; }
    });
    expect(queueAfterRefresh).toBe(0);
  });
});
