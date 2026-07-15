import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync, readFileSync, rmSync } from 'fs';
import { join } from 'path';
import { getProjectId, getLastTaskId, navigateToTask, csrfFetch } from './helpers';

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

test.describe.serial('Untracked file content update (same line count)', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should update diff when untracked file content changes but line count stays the same', async ({ page }) => {
        // Try to find an existing in-progress task with a worktree
        let taskInfo = getTaskInfo();
        let worktreeDir = findWorktreeDir();

        // If no active worktree, try to start a task
        if (!taskInfo || !worktreeDir) {
            const projectId = await getProjectId(page);
            const taskId = await getLastTaskId(page, projectId);

            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
                method: 'POST',
                body: { status: 'in_progress' },
            });

            // Trigger lazy-start via send endpoint
            await page.evaluate(async ({ pid, tid }) => {
                await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ content: 'hello' }),
                });
            }, { pid: projectId, tid: taskId });

            // Wait for worktree to be created
            for (let i = 0; i < 15; i++) {
                worktreeDir = findWorktreeDir();
                if (worktreeDir) break;
                await page.waitForTimeout(1000);
            }

            const tasksData = JSON.parse(execSync(
                `curl -s "http://localhost:3000/api/projects/${projectId}/tasks"`, { encoding: 'utf-8' }
            ));
            const tasks = Array.isArray(tasksData) ? tasksData : (tasksData?.tasks || []);
            const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
            if (task) {
                taskInfo = { projectId, taskId: task.id };
            }
        }

        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `untracked-edit-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        // Create an untracked file with single-line content
        writeFileSync(filePath, 'todo');

        // Open task detail
        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

        // Switch to Changes tab if in mobile mode
        const diffColumn = page.locator('.column-diff');
        if (!await diffColumn.isVisible().catch(() => false)) {
            const changesTab = page.locator('.tab-btn:has-text("Changes")');
            if (await changesTab.isVisible().catch(() => false)) {
                await changesTab.click();
            }
        }

        // Wait for diff content area
        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });

        // Wait for initial diff broadcast (watcher debounce is 2s)
        await page.waitForTimeout(4000);

        // Verify the untracked file appears in the diff panel
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 8000 });

        // Verify the initial content "todo" appears
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo');
        }).toPass({ timeout: 5000 });

        // Now modify the file — same line count (1 line), different content
        // This is the key scenario: "todo" -> "todo app" (1 line -> 1 line)
        writeFileSync(filePath, 'todo app');

        // Wait for the watcher to detect the change and broadcast the update
        // The watcher debounce is 2s, plus time for the frontend to fetch and render
        await page.waitForTimeout(5000);

        // Verify the updated content "todo app" appears in the diff
        // This is the assertion that validates the fix for the stale content bug
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo app');
        }).toPass({ timeout: 10000 });

        // Clean up
        rmSync(filePath, { force: true });
    });
});
