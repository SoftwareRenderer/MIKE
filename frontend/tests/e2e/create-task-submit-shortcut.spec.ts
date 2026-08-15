import { test, expect } from '@playwright/test';

// ============================================================
// CREATE TASK MODAL — SUBMIT SHORTCUT (default, no config mutation)
// ============================================================
// Ctrl+Enter (and Cmd+Enter) creates the task from anywhere in the dialog.
// Plain Enter still inserts a newline; an empty description is a no-op.
// Runs in the default chromium project: only default bindings are exercised.

test.describe.serial('Create Task submit shortcut (default bindings)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('Control+Enter creates the task and closes the dialog', async ({ page }) => {
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });

    const prompt = `submit-ctrl-${Date.now()}`;
    await page.fill('#task-description', prompt);
    await page.keyboard.press('Control+Enter');

    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
    // The optimistic title is the prompt itself. .first(): the optimistic card
    // and the server card can briefly coexist during the create round-trip.
    await expect(page.locator('.card', { hasText: prompt }).first()).toBeVisible({ timeout: 10000 });
  });

  test('Meta+Enter also creates the task while the binding is unset', async ({ page }) => {
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });

    const prompt = `submit-meta-${Date.now()}`;
    await page.fill('#task-description', prompt);
    await page.keyboard.press('Meta+Enter');

    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
    await expect(page.locator('.card', { hasText: prompt }).first()).toBeVisible({ timeout: 10000 });
  });

  test('plain Enter keeps typing and does not submit', async ({ page }) => {
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });

    await page.fill('#task-description', 'line one');
    await page.keyboard.press('Enter');

    // Still open, newline inserted
    await expect(page.locator('#task-description')).toBeVisible();
    await expect(page.locator('#task-description')).toHaveValue('line one\n');

    // Clean up
    await page.keyboard.press('Escape');
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();
    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
  });

  test('Control+Enter with an empty description is a no-op', async ({ page }) => {
    const cardsBefore = await page.locator('.card').count();
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });

    await page.keyboard.press('Control+Enter');

    // Dialog stays open, no task created
    await expect(page.locator('#task-description')).toBeVisible();
    expect(await page.locator('.card').count()).toBe(cardsBefore);

    // Clean up (blank description closes without warning)
    await page.keyboard.press('Escape');
    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
  });

  test('keycap hints are hidden on mobile viewports (≤768px)', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await expect(page.locator('.add-task-btn .key-hint').first()).not.toBeVisible();

    // Dialog button: keycap hidden, shortcut still works
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.btn-primary kbd.key-hint')).not.toBeVisible();

    const prompt = `submit-mobile-${Date.now()}`;
    await page.fill('#task-description', prompt);
    await page.keyboard.press('Control+Enter');
    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
    await expect(page.locator('.card', { hasText: prompt }).first()).toBeVisible({ timeout: 10000 });
  });

  test('Create button shows the platform-appropriate shortcut hint', async ({ page }) => {
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });

    // Linux test env -> Ctrl+Enter default, in the tooltip and as a keycap
    const createBtn = page.locator('.btn-primary', { hasText: 'Create Task' });
    await expect(createBtn).toHaveAttribute('title', 'Create the task (Ctrl+Enter)');
    await expect(createBtn.locator('kbd.key-hint')).toHaveText('Ctrl+Enter');
    // The label appears exactly once (regression: duplicated button text)
    expect(((await createBtn.textContent()) || '').match(/Create Task/g) || []).toHaveLength(1);

    await page.keyboard.press('Escape');
    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
  });
});
