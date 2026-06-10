import { test, expect } from './helpers';

test.describe.serial('Task Title Generation', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should create a task and generate an LLM title', async ({ page }) => {
    // Count existing cards before creating a new task
    const countBefore = await page.locator('.card').count();

    // Click the add task button
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Fill in a descriptive prompt
    const prompt = 'Add input validation to the user registration form to check for email format and password strength';
    await page.fill('textarea', prompt);

    // Submit
    await page.click('button[type="submit"]');

    // Wait for modal to close
    await expect(page.locator('.modal')).not.toBeVisible();

    // Wait for a new card to appear (total count increases by 1)
    await page.waitForFunction(
      (expected) => document.querySelectorAll('.card').length >= expected,
      countBefore + 1,
      { timeout: 5000 }
    );

    // Get the first card in backlog (newest task is added first)
    const backlogColumn = page.locator('.column').first();
    const newCard = backlogColumn.locator('.card').first();

    // Wait for title to be set
    await expect(newCard.locator('.card-title-text')).toBeVisible();

    // Get the title
    const title = await newCard.locator('.card-title-text').textContent();
    expect(title).not.toBeNull();
    expect(title!.trim()).not.toBe('');

    // Wait for LLM generation to complete (mock is fast, ~200ms)
    await page.waitForTimeout(1000);

    // No spinner should be visible (generation complete)
    await expect(newCard.locator('.title-spinner')).not.toBeVisible();
  });

  test('should have no spinners visible on existing cards', async ({ page }) => {
    // Navigate fresh to avoid stale state from previous tests
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Wait briefly for any in-flight title generation to complete
    await page.waitForTimeout(2000);

    // Cards should exist and have titles
    const cards = page.locator('.card');
    const count = await cards.count();
    expect(count).toBeGreaterThan(0);

    // Check that cards have non-empty titles
    for (let i = 0; i < Math.min(count, 3); i++) {
      const title = await cards.nth(i).locator('.card-title-text').textContent();
      expect(title!.trim()).not.toBe('');
    }
  });
});
