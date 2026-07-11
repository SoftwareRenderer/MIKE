import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch } from './helpers';

// Helper: type @ and trigger input event manually (Playwright type() doesn't always fire Svelte oninput)
async function typeAtSign(page: any, textarea: any) {
  await textarea.click();
  await page.evaluate(el => {
    (el as HTMLTextAreaElement).value = '@';
    (el as HTMLTextAreaElement).setSelectionRange(1, 1);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, await textarea.elementHandle());
}

test.describe.serial('File @ Mention', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    test.setTimeout(120000);
    await page.goto('/');
    await page.waitForSelector('.card');
    projectId = await getProjectId(page);

    // Mock the fetch to return file results for the @ mention search
    await page.route('/api/fs/search*', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          root: '/tmp/test-repo',
          results: [
            { path: 'README.md' },
            { path: 'src/app.ts' },
            { path: 'src/components/Button.svelte' },
            { path: 'src/utils.ts' },
            { path: 'package.json' },
          ],
        }),
      });
    });
  });

  test('should show file mention dropdown when typing @ in create task modal', async ({ page }) => {
    // Open the create task modal
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    // Type @ and trigger input
    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);

    // Wait for the dropdown to appear
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    // Should show file results
    const items = page.locator('.mention-item');
    await expect(items).toHaveCount(5);
  });

  test('should navigate dropdown with arrow keys', async ({ page}) => {
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    const items = page.locator('.mention-item');

    // First item should be active by default
    await expect(items.first()).toHaveClass(/active/);

    // Press ArrowDown to highlight second item
    await textarea.press('ArrowDown');
    await expect(items.nth(1)).toHaveClass(/active/);

    // Press ArrowUp to go back to first
    await textarea.press('ArrowUp');
    await expect(items.first()).toHaveClass(/active/);
  });

  test('should select with Enter key', async ({ page }) => {
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    // Press ArrowDown to select second item
    await textarea.press('ArrowDown');
    await expect(page.locator('.mention-item').nth(1)).toHaveClass(/active/);

    // Press Enter to select
    await textarea.press('Enter');

    // Dropdown should disappear
    await expect(page.locator('.mention-dropdown')).not.toBeVisible();

    // Textarea should contain @path of second item
    const value = await textarea.inputValue();
    expect(value).toBe('@src/app.ts');
  });

  test('should close dropdown with Escape', async ({ page }) => {
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    // Press Escape to close
    await textarea.press('Escape');

    // Dropdown should be hidden
    await expect(page.locator('.mention-dropdown')).not.toBeVisible();

    // The @ should still be in the textarea
    const value = await textarea.inputValue();
    expect(value).toBe('@');
  });

  test('should select a file by clicking without closing the modal', async ({ page }) => {
    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    // Click the first result
    await page.locator('.mention-item').first().click();

    // Dropdown should disappear
    await expect(page.locator('.mention-dropdown')).not.toBeVisible();

    // Modal should STILL be open (not closed by the click)
    await expect(page.locator('#task-description')).toBeVisible();

    // Textarea should contain @path of first item
    const value = await textarea.inputValue();
    expect(value).toBe('@README.md');
  });

  test('should filter results by query after @', async ({ page }) => {
    // Update mock to filter based on query
    await page.route('/api/fs/search*', async (route) => {
      const url = new URL(route.request().url());
      const q = url.searchParams.get('q') || '';

      const allFiles = [
        { path: 'README.md' },
        { path: 'src/app.ts' },
        { path: 'src/components/Button.svelte' },
        { path: 'src/utils.ts' },
        { path: 'package.json' },
      ];

      const results = q
        ? allFiles.filter(f => f.path.toLowerCase().includes(q.toLowerCase()))
        : allFiles;

      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ root: '/tmp/test-repo', results }),
      });
    });

    await page.locator('.add-task-btn').click();
    await expect(page.locator('#task-description')).toBeVisible();

    const textarea = page.locator('#task-description');
    await typeAtSign(page, textarea);

    // Wait for initial dropdown
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.mention-item')).toHaveCount(5);

    // Type more after @ to filter
    await page.evaluate(el => {
      (el as HTMLTextAreaElement).value = '@src';
      (el as HTMLTextAreaElement).setSelectionRange(4, 4);
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }, await textarea.elementHandle());

    // Wait for filtered results
    await expect(async () => {
      const count = await page.locator('.mention-item').count();
      expect(count).toBeGreaterThan(0);
      expect(count).toBeLessThan(5);
    }).toPass({ timeout: 5000 });
  });
});
