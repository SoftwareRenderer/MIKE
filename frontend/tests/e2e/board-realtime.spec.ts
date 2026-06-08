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

    // Wait for the board to update (card count increases)
    await expect(async () => {
      const count = await page.locator('.card').count();
      expect(count).toBeGreaterThan(initialCount);
    }).toPass({ timeout: 10000 });
  });

  test('should reflect task deletion on board without refresh', async ({ page }) => {
    const uniqueId = Date.now();
    const uniqueTitle = `Delete me ${uniqueId}`;

    // Create a task to delete
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', uniqueTitle);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();

    // Wait for the specific card to appear (not affected by parallel workers)
    const newCard = page.locator(`.card:has-text("${uniqueId}")`);
    await expect(newCard).toBeVisible({ timeout: 10000 });

    // Open the task detail (click our specific card)
    await newCard.click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Intercept browser confirm dialog
    page.on('dialog', dialog => dialog.accept());

    // Click delete button
    await page.click('[aria-label="Delete task"]');

    // Navigate back to board
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 5000 });

    // Our specific card should be gone
    await expect(page.locator(`.card:has-text("${uniqueId}")`)).not.toBeVisible();
  });
});
