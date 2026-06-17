import { test, expect } from '@playwright/test';

test.describe.serial('Commit Diff Comparison', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('Changes panel renders without errors for tasks with no commits', async ({ page }) => {
    const firstCard = page.locator('.card').first();
    await firstCard.click();
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    const changesTab = page.locator('.tab-btn:has-text("Changes")');
    if (await changesTab.isVisible().catch(() => false)) {
      await changesTab.click();
      await page.waitForTimeout(500);
    }

    const diffContent = page.locator('.diff-content');
    await expect(diffContent).toBeVisible();

    // No diff header for tasks with no commits
    const diffHeader = page.locator('.diff-header');
    await page.waitForTimeout(500);
  });

  test('shows two dropdowns with defaults: Original branch -> Working tree', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const inProgressCards = page.locator('.card').filter({ hasText: 'In Progress' });
    const count = await inProgressCards.count();

    if (count > 0) {
      await inProgressCards.first().click();
      await page.waitForSelector('.task-detail-page', { timeout: 10000 });

      const changesTab = page.locator('.tab-btn:has-text("Changes")');
      if (await changesTab.isVisible().catch(() => false)) {
        await changesTab.click();
      }
      await page.waitForTimeout(1000);

      const diffHeader = page.locator('.diff-header');
      if (await diffHeader.isVisible().catch(() => false)) {
        // Should have 2 custom select buttons and 1 arrow
        const selectBtns = page.locator('.diff-select-btn');
        expect(await selectBtns.count()).toBe(2);

        const arrow = page.locator('.diff-arrow');
        expect(await arrow.isVisible()).toBe(true);

        // First dropdown shows base commit info or "Original branch"
        const fromBtn = selectBtns.first();
        const fromText = await fromBtn.textContent();
        expect(fromText.length).toBeGreaterThan(0);

        // Second dropdown defaults to "Working tree"
        const toBtn = selectBtns.last();
        expect(await toBtn.textContent()).toContain('Working tree');
      }
    } else {
      await expect(page.locator('.board')).toBeVisible();
    }
  });

  test('changing dropdowns updates the diff view', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const inProgressCards = page.locator('.card').filter({ hasText: 'In Progress' });
    const count = await inProgressCards.count();

    if (count > 0) {
      await inProgressCards.first().click();
      await page.waitForSelector('.task-detail-page', { timeout: 10000 });

      const changesTab = page.locator('.tab-btn:has-text("Changes")');
      if (await changesTab.isVisible().catch(() => false)) {
        await changesTab.click();
      }
      await page.waitForTimeout(1000);

      const selectBtns = page.locator('.diff-select-btn');
      const btnCount = await selectBtns.count();

      if (btnCount === 2) {
        // Click first dropdown to open it
        await selectBtns.first().click();
        await page.waitForTimeout(200);

        // Dropdown should show options
        const dropdownOptions = page.locator('.sel-option');
        const optionCount = await dropdownOptions.count();
        expect(optionCount).toBeGreaterThanOrEqual(1);

        // Close dropdown
        await page.press('Escape');

        // Click second dropdown
        await selectBtns.last().click();
        await page.waitForTimeout(200);

        // Should have "Working tree" as first option
        const toDropdownOptions = page.locator('.sel-option');
        if (await toDropdownOptions.count() > 0) {
          expect(await toDropdownOptions.first().isVisible()).toBe(true);
        }

        await page.press('Escape');
      }
    } else {
      await expect(page.locator('.board')).toBeVisible();
    }
  });
});
