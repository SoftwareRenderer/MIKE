import { test, expect } from '@playwright/test';
import { openSettingsTab } from './helpers';

// ============================================================
// Extension update row layout
// ============================================================
//
// What is here is what only a layout engine can answer: the Update button stays pinned to the right edge
// of its row whatever the extension's name is, so the name truncates rather than pushing the button onto
// a wrapped, left-aligned second line. jsdom has no layout, so no unit file can measure a truncation.
//
// What the update payload means — the changelog entries rendered inline, oldest first, the short hash
// chip carrying the full commit in its tooltip, and no changelog block for an update that shipped none —
// is staged as a prop in `ConfigModal/__tests__/extension-updates-changelog.unit.svelte.ts`, because a
// route stub in this lane is a payload only the spec reading it ever produced.

// Long enough that the name cannot fit beside the Update button at the default
// 1280px viewport — the row used to wrap and drop the button to the LEFT edge.
const LONG_NAME = 'a-very-long-extension-name-for-layout-regressions-that-keeps-going-and-going-and-going-and-going';

test.describe('Extension update row layout', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/extensions/check-updates', (route) =>
      route.fulfill({
        status: 200,
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify([
          {
            id: 'long-name-ext',
            name: LONG_NAME,
            current_version: '1.0.0',
            available_version: '2.0.0',
          },
        ]),
      })
    );
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open Settings → Extensions.
    await openSettingsTab(page, 'Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });
    await page.click('button:has-text("Check for Updates")');
    await expect(page.locator('.update-item')).toHaveCount(1, { timeout: 10000 });
  });

  test('keeps the update button on the right for a long extension name', async ({ page }) => {
    const item = page.locator('.update-item');
    const button = item.locator('button.update-action');
    const name = item.locator('.update-name');

    await expect(button).toBeVisible();
    // The full name is still reachable once truncated.
    await expect(name).toHaveAttribute('title', LONG_NAME);

    const [itemBox, buttonBox, nameBox] = [await item.boundingBox(), await button.boundingBox(), await name.boundingBox()];
    if (!itemBox || !buttonBox || !nameBox) throw new Error('update row has no layout boxes');

    // Right-hand side: the button's right edge sits on the row's right edge.
    expect(itemBox.x + itemBox.width - (buttonBox.x + buttonBox.width)).toBeLessThan(2);
    // Not wrapped below the name and left-aligned: the two share the same row.
    expect(buttonBox.y).toBeLessThan(nameBox.y + nameBox.height);
    expect(nameBox.y).toBeLessThan(buttonBox.y + buttonBox.height);
    expect(buttonBox.x).toBeGreaterThan(nameBox.x);

    // The name is what gives: it is clipped, not the layout broken.
    expect(await name.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true);
  });
});
