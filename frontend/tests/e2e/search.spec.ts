import { test, expect, Page } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync, rmSync } from 'fs';
import { join } from 'path';

test.describe.configure({ mode: 'parallel' });

// --- Shared utilities ---

function getConfig() {
    return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function findWorktreeDir(): string | null {
    const worktreesDir = getConfig().workspace_dir;
    try {
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

function getTaskInfo(): { projectId: string; taskId: string } | null {
    try {
        const projects = JSON.parse(execSync(
            'curl -s http://localhost:3000/api/projects', { encoding: 'utf-8' }
        ));
        if (!projects.length) return null;
        const projectId = projects[0].id;
        const tasks = JSON.parse(execSync(
            `curl -s http://localhost:3000/api/projects/${projectId}/tasks`, { encoding: 'utf-8' }
        ));
        const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
        if (!task) return null;
        return { projectId, taskId: task.id };
    } catch {
        return null;
    }
}

/**
 * Open the file editor from a diff row in the task detail page.
 * Creates a unique test file, waits for it to appear in the diff, and clicks to open the editor.
 * Returns the file path for cleanup.
 */
async function openEditorWithTestFile(page: Page, worktreeDir: string): Promise<string> {
    const uniqueId = Date.now();
    const testFile = `search-test-${uniqueId}.txt`;
    const filePath = join(worktreeDir, testFile);
    const fileContent = [
        'function hello() {',
        '  console.log("hello world");',
        '  return "hello";',
        '}',
        '',
        'function goodbye() {',
        '  console.log("goodbye world");',
        '  return "goodbye";',
        '}',
        '',
        'const greeting = hello();',
        'const farewell = goodbye();',
    ].join('\n');

    writeFileSync(filePath, fileContent);

    // Wait for the diff panel to show the new file
    await expect(async () => {
        const diffText = await page.locator('.diff-scroll').textContent().catch(() => '');
        expect(diffText).toContain(testFile);
    }).toPass({ timeout: 10000 });

    // Click a diff row to open the editor
    await expect(async () => {
        const clickableRows = await page.locator('.diff-row-clickable').count();
        expect(clickableRows).toBeGreaterThan(0);
    }).toPass({ timeout: 10000 });

    // Find the clickable row associated with our test file
    const diffRows = page.locator('.diff-row-clickable');
    const rowCount = await diffRows.count();
    for (let i = 0; i < rowCount; i++) {
        const row = diffRows.nth(i);
        const rowText = await row.textContent().catch(() => '');
        if (rowText.includes('hello') || rowText.includes('console')) {
            await row.click();
            break;
        }
    }

    // Wait for the editor to load (either branch-editor or file-editor)
    await expect(async () => {
        const hasBranchEditor = await page.locator('.branch-editor').isVisible().catch(() => false);
        const hasFileEditor = await page.locator('.file-editor').isVisible().catch(() => false);
        expect(hasBranchEditor || hasFileEditor).toBe(true);
    }).toPass({ timeout: 10000 });

    // Wait for CodeMirror to be ready
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

    // Wait for the editor footer (indicates editor is fully loaded)
    await expect(page.locator('.editor-footer')).toBeVisible({ timeout: 5000 });

    return filePath;
}

// --- Tests ---

test.describe('CustomSearchPanel', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(30000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('should open search panel via Find button', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        // Navigate to the first task
        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        // Open the editor with a test file
        const filePath = await openEditorWithTestFile(page, worktreeDir);

        try {
            // Click the Find button in the editor footer
            await page.locator('.footer-find-btn').click();

            // Wait for the search panel to appear
            await expect(page.locator('.search-panel')).toBeVisible({ timeout: 5000 });

            // The search input should be present and focused
            await expect(page.locator('.search-input[placeholder="Find"]')).toBeVisible();

            // Verify the search panel has the expected structure
            await expect(page.locator('.search-row')).toBeVisible();
            await expect(page.locator('.search-nav')).toBeVisible();
        } finally {
            rmSync(filePath, { force: true });
        }
    });

    test('should find matches for text in the file', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const filePath = await openEditorWithTestFile(page, worktreeDir);

        try {
            // Open search panel
            await page.locator('.footer-find-btn').click();
            await expect(page.locator('.search-panel')).toBeVisible({ timeout: 5000 });

            // Type search query into the search input
            const searchInput = page.locator('.search-input[placeholder="Find"]');
            await searchInput.fill('hello');

            // Wait for match count to appear (should find 3 matches: "hello world", "hello", "hello()")
            await expect(async () => {
                const matchCount = await page.locator('.match-count').textContent();
                expect(matchCount).toBeTruthy();
                // Should have at least 2 matches
                const count = parseInt(matchCount!.match(/(\d+)/)?.[0] || '0');
                expect(count).toBeGreaterThanOrEqual(2);
            }).toPass({ timeout: 5000 });
        } finally {
            rmSync(filePath, { force: true });
        }
    });

    test('should show zero matches for non-existent text', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const filePath = await openEditorWithTestFile(page, worktreeDir);

        try {
            // Open search panel
            await page.locator('.footer-find-btn').click();
            await expect(page.locator('.search-panel')).toBeVisible({ timeout: 5000 });

            // Search for text that doesn't exist
            const searchInput = page.locator('.search-input[placeholder="Find"]');
            await searchInput.fill('zzzzznonexistentzzzzz');

            // Wait for match count to show 0 matches
            await expect(async () => {
                const matchCount = await page.locator('.match-count').textContent();
                expect(matchCount).toBeTruthy();
                expect(matchCount).toContain('0');
            }).toPass({ timeout: 5000 });
        } finally {
            rmSync(filePath, { force: true });
        }
    });

    test('should close search panel with Escape', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const filePath = await openEditorWithTestFile(page, worktreeDir);

        try {
            // Open search panel
            await page.locator('.footer-find-btn').click();
            await expect(page.locator('.search-panel')).toBeVisible({ timeout: 5000 });

            // Press Escape to close
            await page.keyboard.press('Escape');

            // Search panel should be hidden/removed
            await expect(page.locator('.search-panel')).not.toBeVisible({ timeout: 5000 });
        } finally {
            rmSync(filePath, { force: true });
        }
    });

    test('should navigate between matches with navigation buttons', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const filePath = await openEditorWithTestFile(page, worktreeDir);

        try {
            // Open search panel
            await page.locator('.footer-find-btn').click();
            await expect(page.locator('.search-panel')).toBeVisible({ timeout: 5000 });

            // Search for a term with multiple matches
            const searchInput = page.locator('.search-input[placeholder="Find"]');
            await searchInput.fill('console');

            // Wait for matches to be found
            await expect(async () => {
                const matchCount = await page.locator('.match-count').textContent();
                expect(matchCount).toBeTruthy();
                const count = parseInt(matchCount!.match(/(\d+)/)?.[0] || '0');
                expect(count).toBeGreaterThanOrEqual(2);
            }).toPass({ timeout: 5000 });

            // Verify navigation buttons are visible
            const navBtns = page.locator('.search-nav .nav-btn');
            await expect(navBtns).toHaveCount(3, { timeout: 5000 }); // prev, next, close

            // Click the next match button
            const nextBtn = navBtns.nth(1); // second button is "next"
            await nextBtn.click();

            // Wait briefly for the editor selection to update
            await page.waitForTimeout(300);

            // The active line gutter should be visible (selection moved)
            await expect(page.locator('.cm-activeLineGutter')).toBeVisible();
        } finally {
            rmSync(filePath, { force: true });
        }
    });

    test('should toggle replace panel', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const filePath = await openEditorWithTestFile(page, worktreeDir);

        try {
            // Open search panel
            await page.locator('.footer-find-btn').click();
            await expect(page.locator('.search-panel')).toBeVisible({ timeout: 5000 });

            // Replace row should not be visible initially
            await expect(page.locator('.replace-row')).not.toBeVisible();

            // Click the toggle replace button
            await page.locator('.toggle-replace-btn').click();

            // Replace row should now be visible with a replace input
            await expect(page.locator('.replace-row')).toBeVisible({ timeout: 5000 });
            await expect(page.locator('.replace-input')).toBeVisible();

            // Replace actions should be visible
            await expect(page.locator('.replace-actions .action-btn')).toHaveCount(2); // Replace, All
        } finally {
            rmSync(filePath, { force: true });
        }
    });

    test('should close search panel with close button', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const filePath = await openEditorWithTestFile(page, worktreeDir);

        try {
            // Open search panel
            await page.locator('.footer-find-btn').click();
            await expect(page.locator('.search-panel')).toBeVisible({ timeout: 5000 });

            // Click the close button (third nav button with aria-label="Close search")
            await page.locator('.nav-btn[aria-label="Close search"]').click();

            // Search panel should be hidden/removed
            await expect(page.locator('.search-panel')).not.toBeVisible({ timeout: 5000 });
        } finally {
            rmSync(filePath, { force: true });
        }
    });
});
