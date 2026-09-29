import { test, expect } from '@playwright/test';
import { getProjectId } from './helpers';

test.describe.configure({ mode: 'serial' });

test.describe('Find in Files', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });

        // Navigate to the test project (global-setup project)
        await page.locator('.project-item').first().click();
        await page.waitForSelector('.card, .empty-state, .file-tree', { timeout: 10000 });

        // Open the Explorer view
        await page.locator('.explorer-btn').click();
        await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    });

    test('should open find panel with Ctrl+Shift+F', async ({ page }) => {
        // Press Ctrl+Shift+F to open find panel
        await page.keyboard.down('Control');
        await page.keyboard.down('Shift');
        await page.keyboard.press('KeyF');
        await page.keyboard.up('Shift');
        await page.keyboard.up('Control');

        // Find panel should be visible
        await expect(page.locator('.file-search-panel')).toBeVisible();
        await expect(page.locator('.search-input')).toBeVisible();
    });

    test('should search filenames and show results', async ({ page }) => {
        // Open find panel
        await page.keyboard.down('Control');
        await page.keyboard.down('Shift');
        await page.keyboard.press('KeyF');
        await page.keyboard.up('Shift');
        await page.keyboard.up('Control');

        await expect(page.locator('.file-search-panel')).toBeVisible();

        // Toggle to filename-only search
        await page.locator('button[title="Search Filenames Only"]').click();

        // Type search query - search for "test" which matches the test project files
        await page.locator('.search-input').fill('test');

        // Wait for results (debounced search + network)
        await page.waitForSelector('.search-results, .empty-state', { timeout: 10000 });

        // Should show match count in footer
        await expect(page.locator('.search-footer .match-count')).toBeVisible();
    });

    test('should close find panel with Escape', async ({ page }) => {
        // Open find panel
        await page.keyboard.down('Control');
        await page.keyboard.down('Shift');
        await page.keyboard.press('KeyF');
        await page.keyboard.up('Shift');
        await page.keyboard.up('Control');

        await expect(page.locator('.file-search-panel')).toBeVisible();

        // Press Escape to close
        await page.keyboard.press('Escape');

        // Find panel should be hidden
        await expect(page.locator('.file-search-panel')).not.toBeVisible();
    });

    test('should toggle find panel with Ctrl+Shift+F', async ({ page }) => {
        // Open find panel
        await page.keyboard.down('Control');
        await page.keyboard.down('Shift');
        await page.keyboard.press('KeyF');
        await page.keyboard.up('Shift');
        await page.keyboard.up('Control');

        await expect(page.locator('.file-search-panel')).toBeVisible();

        // Press Ctrl+Shift+F again to close
        await page.keyboard.down('Control');
        await page.keyboard.down('Shift');
        await page.keyboard.press('KeyF');
        await page.keyboard.up('Shift');
        await page.keyboard.up('Control');

        await expect(page.locator('.file-search-panel')).not.toBeVisible();
    });

    test('should show search options toggles', async ({ page }) => {
        // Open find panel
        await page.keyboard.down('Control');
        await page.keyboard.down('Shift');
        await page.keyboard.press('KeyF');
        await page.keyboard.up('Shift');
        await page.keyboard.up('Control');

        await expect(page.locator('.file-search-panel')).toBeVisible();

        // Check toggles are present (using title attributes)
        await expect(page.locator('button[title="Match Case"]')).toBeVisible();
        await expect(page.locator('button[title="Whole Word"]')).toBeVisible();
        await expect(page.locator('button[title="Use Regex"]')).toBeVisible();
    });
});

test.describe('Sidebar persistence', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });
    });

    test('should persist sidebar collapsed state', async ({ page }) => {
        // Find the collapse toggle button
        const toggleBtn = page.locator('.desktop-collapse-btn');
        await expect(toggleBtn).toBeVisible();

        // Collapse the sidebar
        await toggleBtn.click();
        await expect(page.locator('#sidebar')).toHaveClass(/collapsed/);

        await page.reload();
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });

        // Sidebar should still be collapsed
        await expect(page.locator('#sidebar')).toHaveClass(/collapsed/);

        // Expand the sidebar
        await toggleBtn.click();
        await expect(page.locator('#sidebar')).not.toHaveClass(/collapsed/);

        // Reload again
        await page.reload();
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });

        // Sidebar should be expanded
        await expect(page.locator('#sidebar')).not.toHaveClass(/collapsed/);
    });
});
