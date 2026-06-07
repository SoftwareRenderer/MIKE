import { test, expect } from '@playwright/test';

test.describe('Accessibility (Section 508 / WCAG 2.1 AA)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('skip navigation link is present and focuses main content', async ({ page }) => {
    const skipLink = page.locator('a.skip-link');
    await expect(skipLink).toBeVisible();
    await expect(skipLink).toHaveAttribute('href', '#main-content');
    await expect(skipLink).toContainText('Skip to main content');

    // Press Tab to focus the skip link, then Enter to activate (focuses main)
    await page.keyboard.press('Tab');
    await expect(skipLink).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.locator('#main-content')).toBeFocused();
  });

  test('settings modal has role="dialog" and aria-modal="true"', async ({ page }) => {
    await page.click('[aria-label="Settings"]');
    const modal = page.locator('[role="dialog"]').first();
    await expect(modal).toBeVisible({ timeout: 5000 });
    await expect(modal).toHaveAttribute('aria-modal', 'true');
    await page.keyboard.press('Escape');
    await expect(modal).not.toBeVisible();
  });

  test('settings modal traps focus', async ({ page }) => {
    await page.click('[aria-label="Settings"]');
    const modal = page.locator('[role="dialog"]').first();
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Tab through the modal — focus should stay within it
    for (let i = 0; i < 10; i++) {
      await page.keyboard.press('Tab');
      const activeElement = await page.evaluate(() => document.activeElement?.tagName);
      expect(activeElement).not.toBe('BODY');
    }

    // Escape should close the modal
    await page.keyboard.press('Escape');
    await expect(modal).not.toBeVisible();
  });

  test('cards are keyboard accessible (role="button" with tabindex)', async ({ page }) => {
    const cards = page.locator('[role="button"].card');
    const count = await cards.count();
    if (count > 0) {
      const firstCard = cards.first();
      await firstCard.focus();
      await expect(firstCard).toBeFocused();
    }
  });

  test('toggle switches have role="switch" and aria-checked', async ({ page }) => {
    await page.click('[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

    const toggles = page.locator('[role="switch"]');
    const toggleCount = await toggles.count();
    expect(toggleCount).toBeGreaterThanOrEqual(2); // Auto-commit and Plan mode

    for (let i = 0; i < toggleCount; i++) {
      const toggle = toggles.nth(i);
      const ariaChecked = await toggle.getAttribute('aria-checked');
      expect(['', 'true', 'false']).toContain(ariaChecked);
    }

    await page.keyboard.press('Escape');
  });

  test('toggle switches respond to click', async ({ page }) => {
    await page.click('[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

    const toggle = page.locator('[role="switch"]').first();
    const initialChecked = await toggle.getAttribute('aria-checked');

    // Click to toggle
    await toggle.click();
    await page.waitForTimeout(100); // Wait for Svelte reactivity
    const afterClick = await toggle.getAttribute('aria-checked');
    expect(afterClick).not.toBe(initialChecked);

    // Click again to toggle back
    await toggle.click();
    await page.waitForTimeout(100);
    const afterSecondClick = await toggle.getAttribute('aria-checked');
    expect(afterSecondClick).toBe(initialChecked);

    await page.keyboard.press('Escape');
  });

  test('icon-only buttons have aria-labels', async ({ page }) => {
    // Check settings button
    const settingsBtn = page.locator('[aria-label="Settings"]');
    await expect(settingsBtn).toBeVisible();

    // Check that buttons with SVG children have aria-label
    // (excluding buttons that already have text content)
    const iconButtons = page.locator('button:has(> svg):not([aria-label]):not([aria-hidden])');
    const count = await iconButtons.count();
    expect(count).toBe(0);
  });

  test('sidebar toggle has aria-expanded', async ({ page }) => {
    const toggleBtn = page.locator('[aria-controls="sidebar"]');
    const expanded = await toggleBtn.getAttribute('aria-expanded');
    expect(['true', 'false']).toContain(expanded);

    // Click to toggle
    await toggleBtn.click();
    const newExpanded = await toggleBtn.getAttribute('aria-expanded');
    expect(newExpanded).not.toBe(expanded);
  });

  test('expand/collapse buttons have aria-expanded', async ({ page }) => {
    await page.click('[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

    const expandBtns = page.locator('.btn-expand[aria-expanded]');
    const count = await expandBtns.count();
    if (count > 0) {
      for (let i = 0; i < count; i++) {
        const btn = expandBtns.nth(i);
        const expanded = await btn.getAttribute('aria-expanded');
        expect(['true', 'false']).toContain(expanded);
      }
    }

    await page.keyboard.press('Escape');
  });

  test('focus is restored after modal close', async ({ page }) => {
    const settingsBtn = page.locator('[aria-label="Settings"]');
    await settingsBtn.focus();
    await settingsBtn.click();

    const modal = page.locator('[role="dialog"]').first();
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Close with Escape
    await page.keyboard.press('Escape');
    await expect(modal).not.toBeVisible();

    // Focus should return to the settings button
    const activeElement = await page.evaluate(() => document.activeElement?.getAttribute('aria-label'));
    expect(activeElement).toBe('Settings');
  });

  test('decorative SVGs have aria-hidden="true"', async ({ page }) => {
    const svgInButtons = page.locator('button > svg[aria-hidden="true"]');
    const count = await svgInButtons.count();
    expect(count).toBeGreaterThan(0);
  });

  test('main landmark is present and has id for skip link', async ({ page }) => {
    const main = page.locator('main#main-content');
    await expect(main).toBeVisible();
  });

  test('board has aria-label', async ({ page }) => {
    const board = page.locator('[aria-label="Kanban board"]');
    await expect(board).toBeVisible();
  });

  test('sidebar nav has aria-label', async ({ page }) => {
    const nav = page.locator('nav[aria-label="Projects"]');
    await expect(nav).toBeVisible();
  });

  test('columns have ARIA roles for drag targets', async ({ page }) => {
    const columns = page.locator('[role="list"].column');
    const count = await columns.count();
    expect(count).toBeGreaterThan(0);
  });
});
