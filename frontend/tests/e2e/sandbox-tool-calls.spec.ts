import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureChatForm, csrfFetch, ensureTaskStarted } from './helpers';

test.describe.serial('Sandboxed Tool Calls', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    // Ensure sidebar is expanded (project items hidden when collapsed)
    if (await page.locator('.app.collapsed').isVisible().catch(() => false)) {
      await page.locator('.toggle-btn').click();
      await expect(page.locator('.app:not(.collapsed)')).toBeVisible({ timeout: 5000 });
    }
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show sandbox icon on tool calls when sandbox is enabled', async ({ page }) => {
    test.skip(true, 'Requires running executor/tool calls - integration test');
  });

  test('should not show sandbox icon on tool calls when sandbox is disabled', async ({ page }) => {
    test.skip(true, 'Requires running executor/tool calls - integration test');
  });
});
