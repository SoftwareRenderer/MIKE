import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync } from 'fs';
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

function getTaskInfo(): { projectId: string; taskId: string } | null {
    try {
        const projects = JSON.parse(execSync(
            `curl -s http://localhost:3000/api/projects`, { encoding: 'utf-8' }
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

function getDiffViaApi(projectId: string, taskId: string): { diff: string; untracked: Array<{ path: string }> } {
    return JSON.parse(execSync(
        `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff"`,
        { encoding: 'utf-8' }
    ));
}

test.describe.serial('Untracked file diff clears when file is deleted', () => {
    test('diff panel clears when untracked file is removed', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            // Skip if no in-progress task with worktree
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `untracked-clear-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        // Open task detail
        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 5000 });

        // Wait for initial diff to load
        await page.waitForTimeout(3000);

        // Create an untracked file
        execSync(`echo "test content" > ${filePath}`);

        // Wait for broadcast update (poller runs every 2s)
        await page.waitForTimeout(5000);

        // Verify the untracked file appears in the diff panel via API
        const diffBefore = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diffBefore.untracked.length).toBeGreaterThan(0);
        expect(diffBefore.untracked.some((u: any) => u.path === testFile)).toBe(true);

        // Also verify it's visible in the UI
        const untrackedSectionsBefore = await page.locator('.diff-file-section-untracked').count();
        expect(untrackedSectionsBefore).toBeGreaterThan(0);

        // Delete the untracked file
        execSync(`rm -f ${filePath}`);

        // Wait for broadcast update (poller runs every 2s, add buffer)
        await page.waitForTimeout(6000);

        // Verify the diff panel no longer shows the deleted file
        // Check via API - the untracked list should NOT contain the deleted file
        const diffAfter = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diffAfter.untracked.some((u: any) => u.path === testFile)).toBe(false);

        // Check the UI - the untracked section should be gone or not contain the file
        const panelContent = await page.textContent('.diff-content');
        expect(panelContent).not.toContain(testFile);
    });
});
