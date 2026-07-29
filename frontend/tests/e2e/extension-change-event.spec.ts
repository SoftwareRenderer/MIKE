import { test, expect } from '@playwright/test';
import { csrfFetch } from './helpers';

test.describe.configure({ mode: 'parallel' });

// ============================================================
// Extension Change WebSocket Event
// ============================================================

test.describe('Extension Change WebSocket Event', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should broadcast extension_change event when extension is toggled', async ({ page }) => {
    // Create a UI extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `ws-toggle-test-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: {
          display_name: 'WS Toggle Test',
          description: '',
          author: '',
          ui: {
            module_url: '/extensions/test/Test.js',
            injection_points: ['create-task'],
          },
        },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Listen for WebSocket messages
    const wsMessages: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'log' && msg.text().startsWith('[WS-test]')) {
        wsMessages.push(msg.text().slice('[WS-test] '.length));
      }
    });

    // Inject a WS message listener via page.evaluate
    await page.evaluate(() => {
      const wsUrl = `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api/stream`;
      const ws = new WebSocket(wsUrl);
      ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        if (msg.type === 'extension_change') {
          console.log('[WS-test] extension_change');
        }
      };
    });

    // Wait for WS to connect
    await page.waitForTimeout(500);

    // Toggle the extension off
    await csrfFetch(page, `/api/extensions/${extId}/enabled`, {
      method: 'PATCH',
      body: { enabled: false },
    });

    // Wait for the WS event
    await page.waitForTimeout(1000);

    // Verify the extension_change event was received
    expect(wsMessages).toContain('extension_change');

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should reload extensions in UI after toggle without page refresh', async ({ page }) => {
    // Create a UI extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `ui-refresh-test-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: {
          display_name: 'UI Refresh Test',
          description: '',
          author: '',
        },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Open settings > Extensions tab
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Wait for extension to appear
    await expect(page.locator('.extension-card').filter({ hasText: 'UI Refresh Test' })).toBeVisible({ timeout: 5000 });

    // Verify it's enabled (toggle should be in "on" state)
    const extCard = page.locator('.extension-card').filter({ hasText: 'UI Refresh Test' });
    const toggleSwitch = extCard.locator('.toggle-switch');

    // Click toggle to disable
    await toggleSwitch.click();
    await page.waitForTimeout(500);

    // Verify via API that it was disabled (the toggle should have worked)
    const checkResult = await page.evaluate(async (id) => {
      const res = await fetch('/api/extensions');
      const extensions = await res.json();
      return extensions.find((ext: any) => ext.id === id);
    }, extId);
    expect(checkResult.enabled).toBe(false);

    // Now toggle from a different tab/context via API to test WS event
    // Re-enable via API
    await csrfFetch(page, `/api/extensions/${extId}/enabled`, {
      method: 'PATCH',
      body: { enabled: true },
    });

    // Wait for WS event to trigger reload
    await page.waitForTimeout(1000);

    // The extension card should still be visible (page was not refreshed)
    await expect(extCard).toBeVisible();

    // Verify it's enabled again
    const checkResult2 = await page.evaluate(async (id) => {
      const res = await fetch('/api/extensions');
      const extensions = await res.json();
      return extensions.find((ext: any) => ext.id === id);
    }, extId);
    expect(checkResult2.enabled).toBe(true);

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should broadcast extension_change event when extension is created', async ({ page }) => {
    // Listen for WebSocket messages
    const wsMessages: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'log' && msg.text().startsWith('[WS-test]')) {
        wsMessages.push(msg.text().slice('[WS-test] '.length));
      }
    });

    // Inject a WS message listener
    await page.evaluate(() => {
      const wsUrl = `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api/stream`;
      const ws = new WebSocket(wsUrl);
      ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        if (msg.type === 'extension_change') {
          console.log('[WS-test] extension_change');
        }
      };
    });

    // Wait for WS to connect
    await page.waitForTimeout(500);

    // Create a new extension
    await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `ws-create-test-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: { display_name: 'WS Create Test', description: '', author: '' },
      },
    });

    // Wait for the WS event
    await page.waitForTimeout(1000);

    // Verify the extension_change event was received
    expect(wsMessages).toContain('extension_change');
  });

  test('should broadcast extension_change event when extension is deleted', async ({ page }) => {
    // Create an extension first
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `ws-delete-test-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: { display_name: 'WS Delete Test', description: '', author: '' },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Listen for WebSocket messages
    const wsMessages: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'log' && msg.text().startsWith('[WS-test]')) {
        wsMessages.push(msg.text().slice('[WS-test] '.length));
      }
    });

    // Inject a WS message listener
    await page.evaluate(() => {
      const wsUrl = `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api/stream`;
      const ws = new WebSocket(wsUrl);
      ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        if (msg.type === 'extension_change') {
          console.log('[WS-test] extension_change');
        }
      };
    });

    // Wait for WS to connect
    await page.waitForTimeout(500);

    // Delete the extension
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });

    // Wait for the WS event
    await page.waitForTimeout(1000);

    // Verify the extension_change event was received
    expect(wsMessages).toContain('extension_change');
  });
});
