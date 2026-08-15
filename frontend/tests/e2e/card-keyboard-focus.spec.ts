import { test, expect } from '@playwright/test';

// ============================================================
// CARD KEYBOARD FOCUS — ACTION BUTTON VISIBILITY
// ============================================================
// Card action icons are hidden until hover; :focus-within must reveal them
// while focus is in the card and hide them again on blur.

test.describe.serial('Card action buttons on keyboard focus', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('focusing a card reveals its action buttons, blurring hides them', async ({ page }) => {
    const card = page.locator('.card').first();
    const actions = card.locator('.card-actions');
    const opacity = () => actions.evaluate((el) => getComputedStyle(el).opacity);

    // Hidden when focus is elsewhere
    await expect.poll(opacity).toBe('0');

    // Keyboard user focuses the card -> buttons appear
    await card.focus();
    await expect.poll(opacity).toBe('1');

    // The first action button is the next tab stop (now visible)
    await page.keyboard.press('Tab');
    const isActionButton = await page.evaluate(() =>
      document.activeElement instanceof HTMLElement &&
      document.activeElement.classList.contains('action-icon'));
    expect(isActionButton).toBe(true);

    // Focus leaves the card -> buttons hide again
    await page.evaluate(() => (document.activeElement as HTMLElement).blur());
    await expect.poll(opacity).toBe('0');
  });
});
