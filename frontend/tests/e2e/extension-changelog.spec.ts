import { test, expect } from '@playwright/test';

test.describe.configure({ mode: 'parallel' });

// ============================================================
// Extension update changelogs
// ============================================================
//
// The check-updates endpoint is intercepted: the backend fetch/parse/filter
// logic (published changelog → entries newer than the installed hash) is
// covered by Go unit tests (backend/extensions), and the hosted central
// registry is not reachable from the test environment. This spec verifies
// the real UI contract: updates with a changelog render the entries inline,
// updates without one do not.

// Long enough that the name cannot fit beside the Update button at the default
// 1280px viewport — the row used to wrap and drop the button to the LEFT edge.
const LONG_NAME = 'a-very-long-extension-name-for-layout-regressions-that-keeps-going-and-going-and-going-and-going';

const COMMIT_A = 'a'.repeat(64);
const COMMIT_B = 'b'.repeat(64);
const COMMIT_C = 'c'.repeat(64);

const CHECK_UPDATES_FIXTURE = [
  {
    id: 'changelog-ext',
    name: 'changelog-ext',
    current_version: '1.0.0',
    available_version: '1.2.0',
    current_commit: COMMIT_A,
    available_commit: COMMIT_C,
    changelog: [
      { hash: COMMIT_B, version: '1.1.0', description: 'Fix hover offsets' },
      { hash: COMMIT_C, version: '1.2.0', description: 'Add dark mode support' },
    ],
  },
  {
    id: 'plain-ext',
    name: 'plain-ext',
    current_version: '1.0.0',
    available_version: '2.0.0',
  },
];

async function mockCheckUpdates(page: import('@playwright/test').Page, updates: unknown[]) {
  await page.route('**/api/extensions/check-updates', (route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(updates),
    })
  );
}

async function openExtensionsTab(page: import('@playwright/test').Page) {
  // Open Settings → Extensions.
  await page.click('.icon-btn[aria-label="Settings"]');
  await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
  await page.click('text=Extensions');
  await page.waitForSelector('.extensions-tab', { timeout: 5000 });
}

test.describe('Extension update changelogs', () => {
  test.beforeEach(async ({ page }) => {
    await mockCheckUpdates(page, CHECK_UPDATES_FIXTURE);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show changelog entries inline in the Updates Available section', async ({ page }) => {
    await openExtensionsTab(page);

    // Check for updates (response is the intercepted fixture).
    await page.click('button:has-text("Check for Updates")');

    // Both updates are listed.
    const updateItems = page.locator('.update-item');
    await expect(updateItems).toHaveCount(2, { timeout: 10000 });

    // The update WITH a changelog renders the entries, oldest → newest.
    const withChangelog = updateItems.filter({ hasText: 'changelog-ext' });
    const changelog = withChangelog.locator('.update-changelog');
    await expect(changelog).toBeVisible();
    await expect(changelog.locator('.update-changelog-title')).toHaveText('Changes since 1.0.0:');

    const items = changelog.locator('.update-changelog-item');
    await expect(items).toHaveCount(2);
    await expect(items.nth(0).locator('.update-changelog-desc')).toHaveText('Fix hover offsets');
    await expect(items.nth(1).locator('.update-changelog-desc')).toHaveText('Add dark mode support');

    // Short hash chips carry the full hash in the tooltip.
    await expect(items.nth(0).locator('.update-changelog-hash')).toHaveText('bbbbbbb');
    await expect(items.nth(0).locator('.update-changelog-hash')).toHaveAttribute('title', COMMIT_B);
    await expect(items.nth(1).locator('.update-changelog-hash')).toHaveAttribute('title', COMMIT_C);

    // The update WITHOUT a changelog renders no changelog block.
    const plain = updateItems.filter({ hasText: 'plain-ext' });
    await expect(plain.locator('.update-changelog')).toHaveCount(0);
  });
});

test.describe('Extension update row layout', () => {
  test.beforeEach(async ({ page }) => {
    await mockCheckUpdates(page, [
      {
        id: 'long-name-ext',
        name: LONG_NAME,
        current_version: '1.0.0',
        available_version: '2.0.0',
      },
    ]);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await openExtensionsTab(page);
    await page.click('button:has-text("Check for Updates")');
    await expect(page.locator('.update-item')).toHaveCount(1, { timeout: 10000 });
  });

  // The button is pinned to the right edge of the row whatever the name is: the
  // name truncates instead of pushing the button onto a wrapped, left-aligned line.
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
