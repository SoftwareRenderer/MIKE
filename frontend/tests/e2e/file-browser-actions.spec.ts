import { test, expect } from '@playwright/test';
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { execSync } from 'node:child_process';

const TEST_NON_GIT = '/tmp/kanban-test-nongit';
const TEST_NEW_FOLDER_PARENT = '/tmp/kanban-test-mkdir-parent';

test.describe.configure({ mode: 'serial' });

test.describe('File Browser Actions', () => {
  test.beforeEach(async ({ page }) => {
    try { rmSync(TEST_NON_GIT, { recursive: true, force: true }); } catch { /* ignore */ }
    mkdirSync(TEST_NON_GIT, { recursive: true });
    writeFileSync(`${TEST_NON_GIT}/README.txt`, 'not a git repo');

    try { rmSync(TEST_NEW_FOLDER_PARENT, { recursive: true, force: true }); } catch { /* ignore */ }
    mkdirSync(TEST_NEW_FOLDER_PARENT, { recursive: true });
    execSync('git init', { cwd: TEST_NEW_FOLDER_PARENT });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: TEST_NEW_FOLDER_PARENT });


    await page.goto('/');
    await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });
  });

  test('should initialize git in a non-git directory and select it as project', async ({ page }) => {
    const initialCount = await page.locator('.project-item').count();

    // Open the file browser
    await page.click('.add-project-btn');
    await expect(page.locator('.browser')).toBeVisible({ timeout: 5000 });

    // Navigate to the non-git directory
    const pathInput = page.locator('.path-input');
    await pathInput.fill(TEST_NON_GIT);
    await pathInput.press('Enter');

    // Wait for current path to update (confirms navigation completed)
    await expect(async () => {
        const pathText = await page.locator('.current-path').textContent();
        expect(pathText).toContain('kanban-test-nongit');
    }).toPass({ timeout: 5000 });

    // Git indicator should not be visible (only shown for git repos)
    await expect(page.locator('.git-indicator')).not.toBeVisible();

    // Footer button should say "Initialize Git"
    await expect(page.locator('.btn-select')).toContainText('Initialize Git');

    // Click "Initialize Git"
    await page.click('.btn-select');

    // Wait for init to complete by polling the API (via page.evaluate for auth)
    await expect(async () => {
        const data = await page.evaluate(async (path) => {
            const res = await fetch(`/api/fs?path=${encodeURIComponent(path)}`);
            return res.json();
        }, TEST_NON_GIT);
        expect(data.isGitRepo).toBe(true);
    }).toPass({ timeout: 15000 });

    // Browser should close automatically after init (auto-select)
    await expect(page.locator('.browser')).toBeHidden({ timeout: 5000 });

    // New project should be added
    await expect(page.locator('.project-item')).toHaveCount(initialCount + 1, { timeout: 5000 });

    // Board should be visible (project was selected)
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
  });

  test('should show error when creating a folder that already exists', async ({ page}) => {
    await page.click('.add-project-btn');
    await expect(page.locator('.browser')).toBeVisible({ timeout: 5000 });

    const pathInput = page.locator('.path-input');
    await pathInput.fill(TEST_NEW_FOLDER_PARENT);
    await pathInput.press('Enter');
    await expect(page.locator('.git-indicator')).toBeVisible({ timeout: 5000 });

    // Open new folder dialog
    await page.click('.icon-btn-sm[title="New folder"]');
    await expect(page.locator('.folder-name-input')).toBeVisible({ timeout: 5000 });

    // Create a folder
    const folderName = `dup-folder-${Date.now()}`;
    await page.fill('.folder-name-input', folderName);
    await page.locator('.folder-name-input').press('Enter');
    await expect(page.locator('.folder-name-input')).toBeHidden({ timeout: 5000 });

    // Go back to parent
    await pathInput.fill(TEST_NEW_FOLDER_PARENT);
    await pathInput.press('Enter');
    await expect(page.locator('.file-list')).toBeVisible({ timeout: 5000 });

    // Try to create the same folder again
    await page.click('.icon-btn-sm[title="New folder"]');
    await expect(page.locator('.folder-name-input')).toBeVisible({ timeout: 5000 });
    await page.fill('.folder-name-input', folderName);
    await page.locator('.folder-name-input').press('Enter');

    // Should show an error
    await expect(page.locator('.error')).toBeVisible({ timeout: 5000 });
    const errorText = await page.locator('.error').textContent();
    expect(errorText).toContain('already exists');
  });

  test('should create project with sandbox disabled when checkbox is unchecked', async ({ page}) => {
    const initialCount = await page.locator('.project-item').count();

    // Open the file browser
    await page.click('.add-project-btn');
    await expect(page.locator('.browser')).toBeVisible({ timeout: 5000 });

    // Uncheck "Use Sandbox"
    const sandboxCheckbox = page.locator('.sandbox-toggle input[type="checkbox"]');
    await sandboxCheckbox.click();
    await expect(sandboxCheckbox).not.toBeChecked();

    // Navigate to a unique directory
    const uniqueDir = `/tmp/kanban-test-sandbox-off-${Date.now()}`;
    try { rmSync(uniqueDir, { recursive: true, force: true }); } catch { /* ignore */ }
    mkdirSync(uniqueDir, { recursive: true });
    execSync('git init', { cwd: uniqueDir });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', { cwd: uniqueDir });

    const pathInput = page.locator('.path-input');
    await pathInput.fill(uniqueDir);
    await pathInput.press('Enter');

    // Wait for git indicator
    await expect(page.locator('.git-indicator')).toBeVisible({ timeout: 5000 });

    // Click Select
    await page.click('.btn-select');

    // Browser should close
    await expect(page.locator('.browser')).toBeHidden({ timeout: 5000 });

    // New project should be added
    await expect(page.locator('.project-item')).toHaveCount(initialCount + 1, { timeout: 5000 });

    // Verify via API that the last project has sandbox_enabled: false
    await expect(async () => {
        const projects = await page.evaluate(async () => {
            const res = await fetch('/api/projects');
            return res.json();
        });
        const lastProject = projects[projects.length - 1];
        expect(lastProject.sandbox_enabled).toBe(false);
    }).toPass({ timeout: 5000 });
  });
});
