import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync } from 'fs';
import { join } from 'path';
import { getProjectId, getLastTaskId, navigateToTask, csrfFetch, waitForChatForm } from './helpers';

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
        // Find a task that is in_progress with a branch (has an active worktree)
        const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
        if (!task) return null;
        return { projectId, taskId: task.id };
    } catch {
        return null;
    }
}

test.describe.serial('Delta diff updates', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should update diff panel for changed files via delta updates', async ({ page }) => {
        // Try to find an existing in-progress task with a worktree
        let taskInfo = getTaskInfo();
        let worktreeDir = findWorktreeDir();

        // If no active worktree, try to start a task via the send endpoint (triggers lazy-start)
        if (!taskInfo || !worktreeDir) {
            const projectId = await getProjectId(page);
            const taskId = await getLastTaskId(page, projectId);

            // Move to in_progress first
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
                method: 'POST',
                body: { status: 'in_progress' },
            });

            // Send a message via the send endpoint to trigger lazy-start (creates worktree)
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

            // Re-check task info
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
            // No in-progress task with worktree — skip
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const fileA = `delta-test-a-${uniqueId}.txt`;
        const fileB = `delta-test-b-${uniqueId}.txt`;

        // Create two untracked files
        writeFileSync(join(worktreeDir, fileA), 'content of file A v1');
        writeFileSync(join(worktreeDir, fileB), 'content of file B v1');

        // Open task detail
        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
        await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible({ timeout: 10000 });

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

        // Verify both files appear in the diff panel
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(fileA);
            expect(diffText).toContain(fileB);
        }).toPass({ timeout: 8000 });

        // Now modify only file A — triggers a delta update
        writeFileSync(join(worktreeDir, fileA), 'content of file A v2 modified');

        // Wait for delta update (watcher debounce 2s + fetch)
        await page.waitForTimeout(5000);

        // Verify file A's updated content appears in the diff
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('v2 modified');
        }).toPass({ timeout: 8000 });

        // Now modify only file B — another delta update
        writeFileSync(join(worktreeDir, fileB), 'content of file B v2 modified');

        // Wait for delta update
        await page.waitForTimeout(5000);

        // Verify file B's updated content also appears
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('v2 modified');
        }).toPass({ timeout: 8000 });

        // Clean up
        execSync(`rm -f ${join(worktreeDir, fileA)} ${join(worktreeDir, fileB)}`);
    });

    test('should log console.error when diff-file fetch fails', async ({ page }) => {
        // Try to find an existing in-progress task with a worktree
        let taskInfo = getTaskInfo();
        let worktreeDir = findWorktreeDir();

        if (!taskInfo || !worktreeDir) {
            const projectId = await getProjectId(page);
            const taskId = await getLastTaskId(page, projectId);

            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
                method: 'POST',
                body: { status: 'in_progress' },
            });

            await page.evaluate(async ({ pid, tid }) => {
                await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ content: 'hello' }),
                });
            }, { pid: projectId, tid: taskId });

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
        const testFile = `delta-error-${uniqueId}.txt`;
        writeFileSync(join(worktreeDir, testFile), 'initial content');

        // Open task detail
        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
        await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible({ timeout: 10000 });

        // Switch to Changes tab if needed
        const diffColumn = page.locator('.column-diff');
        if (!await diffColumn.isVisible().catch(() => false)) {
            const changesTab = page.locator('.tab-btn:has-text("Changes")');
            if (await changesTab.isVisible().catch(() => false)) {
                await changesTab.click();
            }
        }

        // Wait for initial diff to load
        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });
        await page.waitForTimeout(3000);

        // Intercept diff-file endpoint to simulate failure
        await page.route('**/api/projects/*/tasks/*/diff-file*', (route) => {
            route.fulfill({
                status: 500,
                body: 'internal error',
            });
        });

        // Collect console errors
        const consoleErrors: string[] = [];
        page.on('console', (msg) => {
            if (msg.type() === 'error') {
                consoleErrors.push(msg.text());
            }
        });

        // Modify the file to trigger a delta update (which will try to fetch and fail)
        writeFileSync(join(worktreeDir, testFile), 'modified content for error test');

        // Wait for the delta update cycle to attempt the fetch
        await page.waitForTimeout(5000);

        // Verify a console.error was logged
        const hasDiffError = consoleErrors.some(err =>
            err.toLowerCase().includes('diff') ||
            err.toLowerCase().includes('fetch') ||
            err.toLowerCase().includes('file')
        );
        expect(hasDiffError).toBe(true);

        // Clean up route interception
        await page.unroute('**/api/projects/*/tasks/*/diff-file*');

        // Clean up
        execSync(`rm -f ${join(worktreeDir, testFile)}`);
    });
});
