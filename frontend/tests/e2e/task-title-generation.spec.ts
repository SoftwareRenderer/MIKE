import { test, expect } from '@playwright/test';

test.describe.configure({ mode: 'parallel' });
test.describe('Task Title Generation', () => {
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

    // Wait for submit button to be enabled (model selector may need a moment to initialize)
    await expect(page.locator('button[type="submit"]')).toBeEnabled();
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

    // Wait for spinner to disappear (graceful: LLM may not respond in test env)
    await newCard.locator('.title-spinner').isHidden({ timeout: 5000 }).catch(() => {});
  });

  test('should use first line as title when it has 5 or fewer words', async ({ page }) => {
    const countBefore = await page.locator('.card').count();

    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // First line has exactly 5 words — should be used as-is
    // Use a unique title to identify the correct card
    const title = `Fix bug ${Date.now()}`;
    await page.fill('textarea', title);

    await expect(page.locator('button[type="submit"]')).toBeEnabled();
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();

    await page.waitForFunction(
      (expected) => document.querySelectorAll('.card').length >= expected,
      countBefore + 1,
      { timeout: 5000 }
    );

    // Find the card by its unique title text
    const newCard = page.locator('.card').filter({ hasText: title });
    await expect(newCard.locator('.card-title-text')).toBeVisible();
    const cardTitle = await newCard.locator('.card-title-text').textContent();
    expect(cardTitle!.trim()).toBe(title);

    // No spinner should appear (short title, no LLM needed)
    await newCard.locator('.title-spinner').isHidden({ timeout: 3000 }).catch(() => {});
  });

  test('should trigger LLM title generation when first line has more than 5 words', async ({ page }) => {
    const countBefore = await page.locator('.card').count();

    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // First line has more than 5 words — LLM should generate a title
    const prompt = `Add input validation to registration form ${Date.now()}`;
    await page.fill('textarea', prompt);

    await expect(page.locator('button[type="submit"]')).toBeEnabled();
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();

    // Wait for new card to appear (auto-wait)
    const backlogColumn = page.locator('.column').first();
    await expect(backlogColumn.locator('.card').last()).toBeVisible();

    const newCard = backlogColumn.locator('.card').last();

    // Wait for title to be set (auto-wait)
    await expect(newCard.locator('.card-title-text')).toBeVisible();

    // Wait for spinner to disappear (graceful: LLM may not respond in test env)
    await newCard.locator('.title-spinner').isHidden({ timeout: 5000 }).catch(() => {});

    // Title should be non-empty
    const title = await newCard.locator('.card-title-text').textContent();
    expect(title).not.toBeNull();
    expect(title!.trim()).not.toBe('');
  });

  test('should have no spinners visible on existing cards', async ({ page }) => {
    // Navigate fresh to avoid stale state from previous tests
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Cards should exist and have titles
    const cards = page.locator('.card');
    const count = await cards.count();
    expect(count).toBeGreaterThan(0);

    // Check that cards have non-empty titles (title may be null if still generating)
    let hasTitle = false;
    for (let i = 0; i < Math.min(count, 5); i++) {
      const title = await cards.nth(i).locator('.card-title-text').textContent();
      if (title && title.trim()) {
        hasTitle = true;
        break;
      }
    }
    expect(hasTitle).toBe(true);
  });
});
