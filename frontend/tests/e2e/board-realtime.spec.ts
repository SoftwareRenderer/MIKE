import { test, expect } from '@playwright/test';

test.describe('Board Real-time Updates', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should show new task on board without refresh after creation', async ({ page }) => {
    // Count initial cards
    const initialCount = await page.locator('.card').count();

    // Create a new task
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', `Real-time test ${Date.now()}`);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();

    // Wait briefly for the board to update (WebSocket + callback)
    await page.waitForTimeout(1000);

    // Board should now have one more card
    const newCount = await page.locator('.card').count();
    expect(newCount).toBeGreaterThan(initialCount);
  });

  test('should reflect task deletion on board without refresh', async ({ page }) => {
    // First create a task to delete
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', `Delete test ${Date.now()}`);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();
    await page.waitForTimeout(500);

    const countBeforeDelete = await page.locator('.card').count();

    // Open the task detail (click the last/newest card)
    await page.locator('.card').last().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Intercept browser confirm dialog
    page.on('dialog', dialog => dialog.accept());

    // Click delete button (title="Delete task")
    await page.click('button[title="Delete task"]');

    // Wait for board to update
    await page.waitForTimeout(1500);

    // Navigate back to board if still on task detail
    if (await page.locator('.task-detail-page').isVisible().catch(() => false)) {
      await page.click('.back-btn');
      await page.waitForTimeout(500);
    }

    const countAfterDelete = await page.locator('.card').count();
    expect(countAfterDelete).toBeLessThan(countBeforeDelete);
  });
});
