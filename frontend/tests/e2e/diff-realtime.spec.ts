import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync, unlinkSync } from 'fs';
import { join } from 'path';

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
        // Find any task with a branch (has a worktree)
        const task = tasks.find((t: any) => t.branch);
        if (!task) return null;
        return { projectId, taskId: task.id };
    } catch {
        return null;
    }
}

test.describe.serial('Diff panel real-time file updates', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('should show new untracked files in the diff panel', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            // No worktree available — verify board loads
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.goto(`/?project=${taskInfo.projectId}&task=${taskInfo.taskId}`);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        // Wait for diff viewer to load
        await expect(page.locator('.diff-viewer-loading')).not.toBeVisible({ timeout: 15000 });

        // Create a new untracked file
        const testFileName = `realtime-test-${Date.now()}.txt`;
        const filePath = join(worktreeDir, testFileName);
        writeFileSync(filePath, 'Line 1: diagnostic test\nLine 2: content\n');

        // Wait for worktree watcher to detect and broadcast
        await page.waitForTimeout(5000);

        // Verify the new file appears in the diff panel
        const diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

        // Cleanup
        try { unlinkSync(filePath); } catch { /* ignore */ }
    });

    test('should reflect file modifications in the diff panel', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.goto(`/?project=${taskInfo.projectId}&task=${taskInfo.taskId}`);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });
        await expect(page.locator('.diff-viewer-loading')).not.toBeVisible({ timeout: 15000 });

        // Create a file
        const testFileName = `modify-test-${Date.now()}.txt`;
        const filePath = join(worktreeDir, testFileName);
        writeFileSync(filePath, 'Initial content\n');

        await page.waitForTimeout(5000);
        let diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

        // Modify the file
        writeFileSync(filePath, 'Initial content\nModified line\nExtra line\n');
        await page.waitForTimeout(5000);

        // Verify still visible after modification
        diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

        // Cleanup
        try { unlinkSync(filePath); } catch { /* ignore */ }
    });

    test('should remove deleted files from the diff panel', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.goto(`/?project=${taskInfo.projectId}&task=${taskInfo.taskId}`);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });
        await expect(page.locator('.diff-viewer-loading')).not.toBeVisible({ timeout: 15000 });

        // Create a file
        const testFileName = `delete-test-${Date.now()}.txt`;
        const filePath = join(worktreeDir, testFileName);
        writeFileSync(filePath, 'Content to delete\n');

        await page.waitForTimeout(5000);
        let diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

        // Delete the file
        unlinkSync(filePath);
        await page.waitForTimeout(5000);

        // Verify file is gone from the diff panel
        diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).not.toContain(testFileName);
    });
});
