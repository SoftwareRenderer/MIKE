import { test, expect } from '@playwright/test';

test.describe.configure({ mode: 'parallel' });

test.describe('WebSocket Reconnecting Banner', () => {
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

  test('should show reconnecting banner when websocket disconnects', async ({ page }) => {
    // Wait for WebSocket to connect (banner should not be visible)
    await expect(page.locator('.reconnecting-banner')).not.toBeVisible({ timeout: 10000 });

    // Close all open WebSockets to simulate disconnect
    await page.evaluate(() => {
      const wss = (window as any).__webSockets as WebSocket[];
      for (const ws of wss) {
        if (ws.readyState === WebSocket.OPEN) {
          ws.close();
        }
      }
    });

    // Wait for reconnecting banner to appear
    await expect(page.locator('.reconnecting-banner')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.reconnecting-banner')).toContainText('Reconnecting');

    // Wait for reconnection - banner should disappear
    await expect(page.locator('.reconnecting-banner')).not.toBeVisible({ timeout: 15000 });
  });
});
