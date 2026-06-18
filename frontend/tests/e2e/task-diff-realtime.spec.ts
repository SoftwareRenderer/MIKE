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
        const entries = readdirSync(worktreesDir);
        for (const entry of entries) {
            const fullPath = join(worktreesDir, entry);
            if (statSync(fullPath).isDirectory()) {
                return fullPath;
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

function getDiffViaApi(projectId: string, taskId: string): string {
    return JSON.parse(execSync(
        `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff"`,
        { encoding: 'utf-8' }
    )).diff;
}

test.describe.serial('Real-time diff updates for same-line changes', () => {
    test('diff API returns correct same-line diff', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `diff-api-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        // Create initial file
        execSync(`echo "line one" > ${filePath}`);
        execSync(`cd ${worktreeDir} && git add -A`);

        // Make same-line change
        execSync(`echo "line modified" > ${filePath}`);

        // Check diff via API
        const diff = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff).toContain('line one');
        expect(diff).toContain('line modified');

        // Make another same-line change
        execSync(`echo "line changed again" > ${filePath}`);

        // Check diff via API again
        const diff2 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff2).toContain('line modified');
        expect(diff2).toContain('line changed again');

        // Clean up
        execSync(`rm -f ${filePath}`);
        execSync(`cd ${worktreeDir} && git checkout -- ${testFile} 2>/dev/null || true`);
    });

    test('diff panel updates in UI for same-line changes', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `diff-ui-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        // Create initial file
        execSync(`echo "hello world" > ${filePath}`);
        execSync(`cd ${worktreeDir} && git add -A`);

        // Open task detail
        await page.locator('.card').first().click();
        await page.waitForSelector('.tab-bar', { timeout: 5000 });

        // Wait for initial diff to load via broadcast
        await page.waitForTimeout(4000);

        // Make first same-line change
        execSync(`echo "hello goodbye" > ${filePath}`);

        // Wait for broadcast update (poller runs every 2s)
        await page.waitForTimeout(5000);

        // Check diff via API to confirm backend has the change
        const diff1 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff1).toContain('hello goodbye');

        // Make second same-line change
        execSync(`echo "goodbye world" > ${filePath}`);

        // Wait for broadcast update
        await page.waitForTimeout(5000);

        // Check diff via API
        const diff2 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff2).toContain('goodbye world');

        // Check diff panel content
        const panelContent = await page.textContent('.diff-scroll');
        expect(panelContent).toContain('goodbye world');

        // Clean up
        execSync(`rm -f ${filePath}`);
        execSync(`cd ${worktreeDir} && git checkout -- ${testFile} 2>/dev/null || true`);
    });
});
