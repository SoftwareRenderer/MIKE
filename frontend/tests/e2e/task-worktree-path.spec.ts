import { test, expect } from './helpers';

test.describe('Task worktree display path', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item, .card', { timeout: 10000 });
  });

  test('should show correct worktree path (not ~/kanban/)', async ({ page }) => {
    // Navigate fresh to avoid stale state from previous tests
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Open first card
    await page.locator('.card').first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Check the project-prefix span in the header
    const prefix = page.locator('.project-prefix');
    await expect(prefix).toBeVisible();

    const prefixText = await prefix.textContent();

    // Path should contain the actual project location, not a hard-coded ~/kanban/
    expect(prefixText).not.toContain('~/kanban/');
  });
});
