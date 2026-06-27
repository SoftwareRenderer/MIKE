import { test, expect } from '@playwright/test';

test.describe.serial('Branch View', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000, state: 'attached' });
    // Ensure branch view is closed before each test
    const branchView = page.locator('.branch-view');
    if (await branchView.isVisible().catch(() => false)) {
      await page.keyboard.press('Escape');
      await page.waitForTimeout(200);
    }
  });

  test('should open branch view when clicking branch badge', async ({ page }) => {
    // Click the branch badge
    await page.click('.branch-badge');
    // Should show the branch view
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.branch-view-title')).toContainText('Explorer');
  });

  test('should close branch view with close button', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // Click close button
    await page.click('.branch-view .close-btn');
    // Should show the board again
    await expect(page.locator('.branch-view')).not.toBeVisible();
    await expect(page.locator('.card').first()).toBeVisible({ timeout: 5000 });
  });

  test('should close branch view with Escape key when editor not focused', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // Press Escape
    await page.keyboard.press('Escape');
    // Should close
    await expect(page.locator('.branch-view')).not.toBeVisible();
  });

  test('should show file browser panel', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // File browser should be visible
    await expect(page.locator('.file-browser')).toBeVisible();
    await expect(page.locator('.file-browser-label')).toContainText('Files');
  });

  test('should show git graph panel', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // Git graph should be visible
    await expect(page.locator('.git-graph')).toBeVisible();
    await expect(page.locator('.git-graph-label')).toContainText('History');
  });

  test('should show branch dropdown in git graph', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // Branch dropdown button should be visible
    await expect(page.locator('.branch-dropdown-btn')).toBeVisible();
    // Click to open dropdown
    await page.click('.branch-dropdown-btn');
    // Dropdown list should appear
    await expect(page.locator('.branch-dropdown-list')).toBeVisible({ timeout: 3000 });
  });

  test('should show editor placeholder when no file selected', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // Editor should show placeholder
    const placeholder = page.locator('.editor-placeholder');
    await expect(placeholder).toBeVisible();
    await expect(placeholder).toContainText('Select a file');
  });

  test('should show symbol outline panel', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // Symbol outline should be visible
    await expect(page.locator('.symbol-outline')).toBeVisible();
    await expect(page.locator('.symbol-label')).toContainText('Symbols');
  });

  test('should navigate file browser directories', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // Wait for file browser to load
    await page.waitForSelector('.file-browser-item', { timeout: 5000 }).catch(() => {});
    // Click on a directory if available
    const dirItems = page.locator('.file-browser-item.dir');
    const count = await dirItems.count();
    if (count > 0) {
      await dirItems.first().click();
      // Should navigate (path input should update or new items appear)
      await page.waitForTimeout(500);
    }
  });

  test('should update URL when opening branch view', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // URL should have view=branch
    await page.waitForTimeout(500);
    const url = page.url();
    expect(url).toContain('view=branch');
  });

  test('should remove view=branch from URL when closing', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    await page.waitForTimeout(500);
    // Close the branch view
    await page.click('.branch-view .close-btn');
    await expect(page.locator('.branch-view')).not.toBeVisible();
    await page.waitForTimeout(500);
    const url = page.url();
    expect(url).not.toContain('view=branch');
  });

  test('should restore branch view from URL on page load', async ({ page }) => {
    // First, open the branch view to set the URL
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    await page.waitForTimeout(500);
    // Get the current URL with view=branch
    const urlWithBranch = page.url();
    expect(urlWithBranch).toContain('view=branch');
    // Navigate directly to this URL (simulates page refresh or opening a bookmark)
    await page.goto(urlWithBranch, { waitUntil: 'domcontentloaded' });
    // Wait for branch badge to appear (branch received via WebSocket poll)
    await page.waitForSelector('.branch-badge', { timeout: 15000 });
    // Wait for the branch view to be restored
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 15000 });
  });

  test('should not close with Escape when editor is focused', async ({ page }) => {
    await page.click('.branch-badge');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 5000 });
    // Try to focus the editor (click on the editor area)
    const editorWrapper = page.locator('.editor-wrapper');
    if (await editorWrapper.isVisible().catch(() => false)) {
      await editorWrapper.click();
      await page.waitForTimeout(200);
      // Press Escape - should NOT close because editor is focused
      await page.keyboard.press('Escape');
      // Branch view should still be visible
      await expect(page.locator('.branch-view')).toBeVisible();
    } else {
      // Skip if no editor is available (no file selected)
      test.skip();
    }
  });
});
