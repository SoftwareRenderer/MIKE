import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync } from 'fs';
import { join } from 'path';

function getConfig() {
    return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function findWorktreeDir(): string | null {
    const worktreesDir = getConfig().workspace_dir;
    try {
        // Path structure: worktreesDir/projectID/taskID
        const projectEntries = readdirSync(worktreesDir);
        for (const projEntry of projectEntries) {
            const projPath = join(worktreesDir, projEntry);
            if (!statSync(projPath).isDirectory()) continue;
            const taskEntries = readdirSync(projPath);
            for (const taskEntry of taskEntries) {
                const fullPath = join(projPath, taskEntry);
                if (statSync(fullPath).isDirectory()) {
                    return fullPath;
                }
            }
        }
    } catch { /* ignore */ }
    return null;
}

function getProjectId(): string | null {
    try {
        const projects = JSON.parse(execSync(
            'curl -s http://localhost:3000/api/projects', { encoding: 'utf-8' }
        ));
        return projects.length > 0 ? projects[0].id : null;
    } catch {
        return null;
    }
}

test.describe.serial('Explorer Create Folder', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should create a folder in the current directory and navigate into it', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = getProjectId();
    if (!worktreeDir || !projectId) {
      // No worktree or project — skip gracefully
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const folderName = `new-folder-${uniqueId}`;

    // Click on the project to select it (if not already selected)
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });

    // Open the explorer view by clicking the project title button
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Wait for the file browser to load
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

    // Get the current path before creating the folder
    const pathInput = page.locator('.file-browser-path-input');
    const currentPath = await pathInput.inputValue();

    // Click the "New folder" button (second button in file-actions)
    const newFolderBtns = page.locator('.new-file-btn');
    const newFolderBtn = newFolderBtns.nth(1); // second button is the folder one
    await newFolderBtn.click();

    // Wait for the folder name input to appear
    await expect(page.locator('.new-file-input')).toBeVisible({ timeout: 5000 });

    // Type the folder name and press Enter
    await page.fill('.new-file-input', folderName);
    await page.locator('.new-file-input').press('Enter');

    // Wait for the input to disappear (form closed)
    await expect(page.locator('.new-file-input')).toBeHidden({ timeout: 5000 });

    // Verify the path has been updated to include the new folder (navigated into it)
    await expect(async () => {
        const newPath = await pathInput.inputValue();
        expect(newPath).toContain(folderName);
    }).toPass({ timeout: 5000 });

    // Verify the folder exists on disk
    const folderPath = join(worktreeDir, folderName);
    try {
        const stats = statSync(folderPath);
        expect(stats.isDirectory()).toBe(true);
    } catch {
        test.fail('Folder was not created on disk');
    }

    // Clean up
    execSync(`rm -rf ${folderPath}`);
  });
});
