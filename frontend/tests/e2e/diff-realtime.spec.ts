import { test, expect, Page } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync, unlinkSync, rmSync } from 'fs';
import { join } from 'path';
import { getProjectId, getLastTaskId, navigateToTask, csrfFetch } from './helpers';

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

/**
 * Ensure an in-progress task with a worktree exists.
 * If none exists, moves the last task to in_progress and triggers lazy-start.
 * Returns { taskInfo, worktreeDir } or null if unavailable.
 */
async function ensureActiveWorktree(page: Page): Promise<{ taskInfo: { projectId: string; taskId: string }; worktreeDir: string } | null> {
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

    if (!taskInfo || !worktreeDir) return null;
    return { taskInfo, worktreeDir };
}

// --- Tests: real-time diff panel updates ---

test.describe.serial('Diff panel real-time file updates', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    // From original diff-realtime.spec.ts

    test('should show new untracked files in the diff panel', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        await page.goto(`/?project=${taskInfo.projectId}&task=${taskInfo.taskId}`);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });
        await expect(page.locator('.diff-viewer-loading')).not.toBeVisible({ timeout: 15000 });

        const testFileName = `realtime-test-${Date.now()}.txt`;
        const filePath = join(worktreeDir, testFileName);
        writeFileSync(filePath, 'Line 1: diagnostic test\nLine 2: content\n');

        await page.waitForTimeout(5000);

        const diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

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

        const testFileName = `modify-test-${Date.now()}.txt`;
        const filePath = join(worktreeDir, testFileName);
        writeFileSync(filePath, 'Initial content\n');

        await page.waitForTimeout(5000);
        let diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

        writeFileSync(filePath, 'Initial content\nModified line\nExtra line\n');
        await page.waitForTimeout(5000);

        diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

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

        const testFileName = `delete-test-${Date.now()}.txt`;
        const filePath = join(worktreeDir, testFileName);
        writeFileSync(filePath, 'Content to delete\n');

        await page.waitForTimeout(5000);
        let diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

        unlinkSync(filePath);
        await page.waitForTimeout(5000);

        diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).not.toContain(testFileName);
    });

    // From original task-diff-realtime.spec.ts

    test('diff API returns correct same-line diff', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `diff-api-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        execSync(`echo "line one" > ${filePath}`);
        execSync(`cd ${worktreeDir} && git add -A`);

        execSync(`echo "line modified" > ${filePath}`);

        const diff = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff).toContain('line one');
        expect(diff).toContain('line modified');

        execSync(`echo "line changed again" > ${filePath}`);

        const diff2 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff2).toContain('line modified');
        expect(diff2).toContain('line changed again');

        execSync(`rm -f ${filePath}`);
        execSync(`cd ${worktreeDir} && git checkout -- ${testFile} 2>/dev/null || true`);
    });

    test('diff panel updates in UI for same-line changes', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `diff-ui-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        execSync(`echo "hello world" > ${filePath}`);
        execSync(`cd ${worktreeDir} && git add -A`);

        await page.locator('.card').first().click();
        await page.waitForSelector('.tab-bar', { timeout: 5000 });

        await page.waitForTimeout(4000);

        execSync(`echo "hello goodbye" > ${filePath}`);
        await page.waitForTimeout(5000);

        const diff1 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff1).toContain('hello goodbye');

        execSync(`echo "goodbye world" > ${filePath}`);
        await page.waitForTimeout(5000);

        const diff2 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff2).toContain('goodbye world');

        const panelContent = await page.textContent('.diff-scroll');
        expect(panelContent).toContain('goodbye world');

        execSync(`rm -f ${filePath}`);
        execSync(`cd ${worktreeDir} && git checkout -- ${testFile} 2>/dev/null || true`);
    });

    // From original task-diff-delta-update.spec.ts

    test('should update diff panel for changed files via delta updates', async ({ page }) => {
        const result = await ensureActiveWorktree(page);
        if (!result) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }
        const { taskInfo, worktreeDir } = result;

        const uniqueId = Date.now();
        const fileA = `delta-test-a-${uniqueId}.txt`;
        const fileB = `delta-test-b-${uniqueId}.txt`;

        writeFileSync(join(worktreeDir, fileA), 'content of file A v1');
        writeFileSync(join(worktreeDir, fileB), 'content of file B v1');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
        await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible({ timeout: 10000 });

        const diffColumn = page.locator('.column-diff');
        if (!await diffColumn.isVisible().catch(() => false)) {
            const changesTab = page.locator('.tab-btn:has-text("Changes")');
            if (await changesTab.isVisible().catch(() => false)) {
                await changesTab.click();
            }
        }

        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });
        await page.waitForTimeout(4000);

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(fileA);
            expect(diffText).toContain(fileB);
        }).toPass({ timeout: 8000 });

        writeFileSync(join(worktreeDir, fileA), 'content of file A v2 modified');
        await page.waitForTimeout(5000);

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('v2 modified');
        }).toPass({ timeout: 8000 });

        writeFileSync(join(worktreeDir, fileB), 'content of file B v2 modified');
        await page.waitForTimeout(5000);

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('v2 modified');
        }).toPass({ timeout: 8000 });

        execSync(`rm -f ${join(worktreeDir, fileA)} ${join(worktreeDir, fileB)}`);
    });

    test('should log console.error when diff-file fetch fails', async ({ page }) => {
        const result = await ensureActiveWorktree(page);
        if (!result) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }
        const { taskInfo, worktreeDir } = result;

        const uniqueId = Date.now();
        const testFile = `delta-error-${uniqueId}.txt`;
        writeFileSync(join(worktreeDir, testFile), 'initial content');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
        await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible({ timeout: 10000 });

        const diffColumn = page.locator('.column-diff');
        if (!await diffColumn.isVisible().catch(() => false)) {
            const changesTab = page.locator('.tab-btn:has-text("Changes")');
            if (await changesTab.isVisible().catch(() => false)) {
                await changesTab.click();
            }
        }

        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });
        await page.waitForTimeout(3000);

        await page.route('**/api/projects/*/tasks/*/diff-file*', (route) => {
            route.fulfill({
                status: 500,
                body: 'internal error',
            });
        });

        const consoleErrors: string[] = [];
        page.on('console', (msg) => {
            if (msg.type() === 'error') {
                consoleErrors.push(msg.text());
            }
        });

        writeFileSync(join(worktreeDir, testFile), 'modified content for error test');
        await page.waitForTimeout(5000);

        const hasDiffError = consoleErrors.some(err =>
            err.toLowerCase().includes('diff') ||
            err.toLowerCase().includes('fetch') ||
            err.toLowerCase().includes('file')
        );
        expect(hasDiffError).toBe(true);

        await page.unroute('**/api/projects/*/tasks/*/diff-file*');
        execSync(`rm -f ${join(worktreeDir, testFile)}`);
    });

    // From original task-diff-untracked-edit.spec.ts

    test('should update diff when untracked file content changes but line count stays the same', async ({ page }) => {
        const result = await ensureActiveWorktree(page);
        if (!result) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }
        const { taskInfo, worktreeDir } = result;

        const uniqueId = Date.now();
        const testFile = `untracked-edit-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        writeFileSync(filePath, 'todo');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

        const diffColumn = page.locator('.column-diff');
        if (!await diffColumn.isVisible().catch(() => false)) {
            const changesTab = page.locator('.tab-btn:has-text("Changes")');
            if (await changesTab.isVisible().catch(() => false)) {
                await changesTab.click();
            }
        }

        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });
        await page.waitForTimeout(4000);

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 8000 });

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo');
        }).toPass({ timeout: 5000 });

        writeFileSync(filePath, 'todo app');
        await page.waitForTimeout(5000);

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo app');
        }).toPass({ timeout: 10000 });

        rmSync(filePath, { force: true });
    });
});
