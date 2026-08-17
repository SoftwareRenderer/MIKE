import { test, expect } from '@playwright/test';

/**
 * Review Changes is a first-class URL view (view=review):
 *  - opening the panel rewrites the URL from view=explorer to view=review (push)
 *  - the browser's refresh button restores the panel with fresh data
 *  - the back button closes the panel and keeps the explorer
 *  - closing via the panel's X replaces the URL (no extra history entry)
 *
 * Uses the shared test project — these tests only open/close the panel and
 * never mutate the repo, so they are safe to run in parallel.
 */

/** Navigate to the board and open the Explorer view. */
async function openExplorer(page: Page) {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
}

test.describe.serial('Review Changes URL view (view=review)', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(60000);
        await openExplorer(page);
    });

    test('opening the panel switches the URL to view=review', async ({ page }) => {
        await expect(page).toHaveURL(/view=explorer/);
        await expect(page).not.toHaveURL(/view=review/);

        await page.locator('.git-status-btn').click();

        await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
        await expect(page).toHaveURL(/view=review/);
        await expect(page).not.toHaveURL(/view=explorer/);
    });

    test('browser refresh restores the panel with fresh data', async ({ page }) => {
        await page.locator('.git-status-btn').click();
        await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
        await expect(page).toHaveURL(/view=review/);

        await page.reload();

        // The panel reopens from the URL — fully functional (commit button rendered).
        await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 15000 });
        await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 15000 });
        await expect(page.locator('.panel-commit-btn')).toBeVisible({ timeout: 10000 });
        await expect(page).toHaveURL(/view=review/);
    });

    test('back button closes the panel and keeps the explorer', async ({ page }) => {
        await page.locator('.git-status-btn').click();
        await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
        await expect(page).toHaveURL(/view=review/);

        await page.goBack();
        await page.waitForLoadState('domcontentloaded');

        // Panel closed, explorer still open, URL back to view=explorer.
        await expect(page).toHaveURL(/view=explorer/);
        await expect(page.locator('.explorer-view')).toBeVisible();
        await expect(page.locator('.panel-title:has-text("Changes")')).not.toBeVisible();
        // Explorer columns are back (file browser visible).
        await expect(page.locator('.explorer-left-column')).toBeVisible({ timeout: 10000 });

        // The toggle still works after the back navigation.
        await page.locator('.git-status-btn').click();
        await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
        await expect(page).toHaveURL(/view=review/);
    });

    test('closing via the panel X removes view=review without a history entry', async ({ page }) => {
        await page.locator('.git-status-btn').click();
        await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
        await expect(page).toHaveURL(/view=review/);

        await page.locator('button[aria-label="Close changes panel"]').click();

        await expect(page).toHaveURL(/view=explorer/);
        await expect(page.locator('.panel-title:has-text("Changes")')).not.toBeVisible();
        await expect(page.locator('.explorer-view')).toBeVisible();

        // Close used replaceState: back never re-enters the review view —
        // it lands on the plain explorer, then the board.
        await page.goBack();
        await page.waitForLoadState('domcontentloaded');
        await expect(page).toHaveURL(/view=explorer/);
        await expect(page.locator('.panel-title:has-text("Changes")')).not.toBeVisible();

        await page.goBack();
        await page.waitForLoadState('domcontentloaded');
        await expect(page.locator('.explorer-view')).not.toBeVisible();
        await expect(page.locator('.panel-title:has-text("Changes")')).not.toBeVisible();
        await expect(page.locator('.project-item').first()).toBeVisible();
    });

    test('reopening the explorer does not restore a closed panel', async ({ page }) => {
        await page.locator('.git-status-btn').click();
        await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
        await page.locator('button[aria-label="Close changes panel"]').click();
        await expect(page.locator('.panel-title:has-text("Changes")')).not.toBeVisible();

        // Leave the explorer entirely, then come back.
        await page.locator('button[aria-label="Close branch view"]').click();
        await expect(page.locator('.explorer-view')).not.toBeVisible();
        await page.locator('.explorer-btn').click();
        await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

        // The panel stays closed (no stale state).
        await expect(page.locator('.panel-title:has-text("Changes")')).not.toBeVisible();
        await expect(page).not.toHaveURL(/view=review/);
        await expect(page.locator('.explorer-left-column')).toBeVisible({ timeout: 10000 });
    });
});
