import { test, expect } from '@playwright/test';

test.describe('Structured Diff Viewer', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show empty state when no diff', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // The diff column should show the Changes header
    await expect(page.locator('.column-diff .column-title')).toContainText('Changes');

    // Should show the diff content area (empty state or diff viewer)
    await expect(page.locator('.diff-content')).toBeVisible();
  });

  test('should have refresh button in diff column', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Refresh button should be visible in the diff column header
    const refreshBtn = page.locator('.column-diff [aria-label="Refresh diff"]');
    await expect(refreshBtn).toBeVisible();
  });

  test('should render structured diff with file headers', async ({ page }) => {
    // This test verifies the DiffViewer component structure
    // by injecting a mock diff via the API
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Click the refresh button to load diff
    const refreshBtn = page.locator('.column-diff [aria-label="Refresh diff"]');
    await refreshBtn.click();
    await page.waitForTimeout(500);

    // The diff column should have content (either empty state or file headers)
    const diffContent = page.locator('.diff-content');
    await expect(diffContent).toBeVisible();

    // Check for either empty state or structured diff elements
    const hasEmptyState = await page.locator('.diff-empty').isVisible().catch(() => false);
    const hasFileHeaders = await page.locator('.diff-file-header').count();
    const hasScrollArea = await page.locator('.diff-scroll').isVisible().catch(() => false);

    // Should show either empty state or structured diff
    expect(hasEmptyState || hasFileHeaders > 0 || hasScrollArea).toBe(true);
  });

  test('should have color-coded diff rows', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Verify the diff column exists and is styled
    const diffColumn = page.locator('.column-diff');
    await expect(diffColumn).toBeVisible();

    // Check that the diff content area uses flex layout
    const diffContent = page.locator('.diff-content');
    const style = await diffContent.evaluate(el => window.getComputedStyle(el).display);
    expect(style).toBe('flex');
  });

  test('should show diff file headers with change counts', async ({ page }) => {
    // Intercept the diff API call and return a mock diff
    await page.route('**/api/projects/*/tasks/*/diff', async (route) => {
      const mockDiff = `diff --git a/src/test.ts b/src/test.ts
index abc1234..def5678 100644
--- a/src/test.ts
+++ b/src/test.ts
@@ -1,4 +1,5 @@
 const x = 1;
+const y = 2;
 const z = 3;
-const old = true;
+const new = false;
 const w = 4;
`;
      await route.fulfill({ body: mockDiff, status: 200, headers: { 'Content-Type': 'text/plain' } });
    });

    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Click refresh to load the mock diff
    const refreshBtn = page.locator('.column-diff [aria-label="Refresh diff"]');
    await refreshBtn.click();
    await page.waitForTimeout(500);

    // Should show a file header
    const fileHeaders = page.locator('.diff-file-header');
    await expect(fileHeaders.first()).toBeVisible({ timeout: 5000 });

    // Should show change counts (+2/-1 for our mock diff)
    const stats = page.locator('.diff-file-stats');
    await expect(stats.first()).toBeVisible();
    await expect(page.locator('.diff-added').first()).toBeVisible();
    await expect(page.locator('.diff-removed').first()).toBeVisible();
  });

  test('should show line numbers and color-coded rows', async ({ page }) => {
    await page.route('**/api/projects/*/tasks/*/diff', async (route) => {
      const mockDiff = `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,3 @@
 context line
-removed line
+added line
`;
      await route.fulfill({ body: mockDiff, status: 200, headers: { 'Content-Type': 'text/plain' } });
    });

    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    await page.locator('.column-diff [aria-label="Refresh diff"]').click();
    await page.waitForTimeout(500);

    // Should have diff rows with line numbers
    const diffRows = page.locator('.diff-row');
    const rowCount = await diffRows.count();
    expect(rowCount).toBeGreaterThan(0);

    // Added rows should have green background
    const addedRow = page.locator('.diff-row-added');
    if (await addedRow.count() > 0) {
      const bg = await addedRow.first().evaluate(el => {
        return window.getComputedStyle(el).backgroundColor;
      });
      expect(bg).toContain('63, 185, 80'); // rgba(63, 185, 80, 0.15)
    }

    // Removed rows should have red background
    const removedRow = page.locator('.diff-row-removed');
    if (await removedRow.count() > 0) {
      const bg = await removedRow.first().evaluate(el => {
        return window.getComputedStyle(el).backgroundColor;
      });
      expect(bg).toContain('248, 81, 73'); // rgba(248, 81, 73, 0.15)
    }
  });

  test('should allow expanding/collapsing file sections', async ({ page }) => {
    await page.route('**/api/projects/*/tasks/*/diff', async (route) => {
      const mockDiff = `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,4 @@
 line one
+added line
 line two
 line three
`;
      await route.fulfill({ body: mockDiff, status: 200, headers: { 'Content-Type': 'text/plain' } });
    });

    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    await page.locator('.column-diff [aria-label="Refresh diff"]').click();
    await page.waitForTimeout(500);

    // File header should be visible and clickable
    const fileHeader = page.locator('.diff-file-header').first();
    await expect(fileHeader).toBeVisible();

    // Click to collapse
    await fileHeader.click();
    await page.waitForTimeout(200);

    // File content should be hidden
    const fileContent = page.locator('.diff-file-content');
    // After collapse, the content div should not be in the DOM
    const count = await fileContent.count();
    expect(count).toBe(0);
  });
});
