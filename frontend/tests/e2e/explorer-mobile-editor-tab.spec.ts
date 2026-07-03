import { test, expect } from '@playwright/test';
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { execSync } from 'node:child_process';

const TEST_DIR = '/tmp/kanban-test-explorer-mobile';

test.describe.serial('Explorer Mobile Editor Tab', () => {
  test.beforeEach(async ({ page }) => {
    // Setup test git repo
    try { rmSync(TEST_DIR, { recursive: true, force: true }); } catch { /* ignore */ }
    mkdirSync(TEST_DIR, { recursive: true });
    writeFileSync(`${TEST_DIR}/README.md`, '# Test Project\n\nHello World\n');
    writeFileSync(`${TEST_DIR}/main.go`, 'package main\n\nfunc main() {\n    println("hello")\n}\n');
    execSync('git init', { cwd: TEST_DIR });
    execSync('git add -A', { cwd: TEST_DIR });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "init"', { cwd: TEST_DIR });

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto('/');
    await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });

    // If no projects exist, add the test directory as a project
    const projectCount = await page.locator('.project-item').count();
    if (projectCount === 0) {
      await page.click('.add-project-btn');
      await expect(page.locator('.browser')).toBeVisible({ timeout: 5000 });
      const pathInput = page.locator('.path-input');
      await pathInput.fill(TEST_DIR);
      await pathInput.press('Enter');
      await expect(page.locator('.git-indicator')).toBeVisible({ timeout: 5000 });
      await page.click('.btn-select');
      await expect(page.locator('.browser')).toBeHidden({ timeout: 5000 });
    }
  });

  test('should stay on editor tab after resize event (virtual keyboard)', async ({ page }) => {
    // Open the explorer (branch view)
    await page.click('.explorer-btn');
    await expect(page.locator('.branch-view')).toBeVisible({ timeout: 10000 });

    // Wait for file list to load
    await expect(async () => {
      const count = await page.locator('.file-browser-item').count();
      expect(count).toBeGreaterThan(0);
    }).toPass({ timeout: 10000 });

    // Click a file to open it in the editor
    const fileItems = page.locator('.file-browser-item:not(.dir)');
    const fileCount = await fileItems.count();
    if (fileCount === 0) {
      return; // No files to test with
    }
    await fileItems.first().click();

    // Verify the editor tab becomes active
    const editorTab = page.locator('.mobile-tab-bar .tab-btn:has-text("Editor")');
    await expect(editorTab).toHaveClass(/active/, { timeout: 5000 });

    // Simulate the virtual keyboard appearing by firing a resize event.
    // On mobile, tapping a text input fires 'resize' which triggered the bug
    // (checkMobile() resetting activeTab to 'files').
    await page.evaluate(() => {
      window.dispatchEvent(new Event('resize'));
    });

    // Wait a moment for any reactive updates
    await page.waitForTimeout(100);

    // Bug: editor tab should STAY active (not switch to files tab)
    await expect(editorTab).toHaveClass(/active/);
  });
});
