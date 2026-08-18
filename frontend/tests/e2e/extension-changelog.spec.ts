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

test.describe('Extension update changelogs', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/extensions/check-updates', (route) =>
      route.fulfill({
        status: 200,
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(CHECK_UPDATES_FIXTURE),
      })
    );
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show changelog entries inline in the Updates Available section', async ({ page }) => {
    // Open Settings → Extensions.
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

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
