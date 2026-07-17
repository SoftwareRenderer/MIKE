import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { writeFileSync, unlinkSync, rmSync } from 'fs';
import { join } from 'path';
import { getProjectId, getLastTaskId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted } from './helpers';
import {
    findWorktreeDir,
    getTaskInfo,
    getDiffViaApi,
    ensureActiveWorktree,
    getWorktreePath,
    writeFileInWorktree,
    worktreeExists,
    waitForDiffFile,
    waitForDiffUpdate,
} from './diff-helpers';

// --- Tests: real-time diff panel updates ---

test.describe.serial('Diff panel real-time file updates', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

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

        // Poll diff API until the new file appears, then re-navigate to refresh UI
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            return diff.files && Object.keys(diff.files).some((k: string) => k.includes(testFileName));
        });

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

        // Poll diff API until file appears
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            return diff.files && Object.keys(diff.files).some((k: string) => k.includes(testFileName));
        });

        let diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

        // Modify the file and wait for the API to reflect the change
        writeFileSync(filePath, 'Initial content\nModified line\nExtra line\n');
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            if (!diff.files || !diff.files[testFileName]) return false;
            const fileData = diff.files[testFileName];
            // Check that the file has been updated (line count changed from 1 to 3)
            return fileData.line_count >= 3;
        });

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

        // Poll diff API until file appears
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            return diff.files && Object.keys(diff.files).some((k: string) => k.includes(testFileName));
        });

        let diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).toContain(testFileName);

        // Delete the file and wait for it to disappear from the diff API
        unlinkSync(filePath);
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            if (!diff.files) return true;
            return !Object.keys(diff.files).some((k: string) => k.includes(testFileName));
        });

        diffText = await page.locator('.column-diff').textContent().catch(() => '');
        expect(diffText).not.toContain(testFileName);
    });

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
        expect(diff.diff).toContain('line one');
        expect(diff.diff).toContain('line modified');

        execSync(`echo "line changed again" > ${filePath}`);

        const diff2 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff2.diff).toContain('line modified');
        expect(diff2.diff).toContain('line changed again');

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

        // Wait for diff content to be visible instead of blind timeout
        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });

        execSync(`echo "hello goodbye" > ${filePath}`);
        // Poll API until the file diff contains the new content
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            return diff.diff && diff.diff.includes('hello goodbye');
        });

        const diff1 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff1.diff).toContain('hello goodbye');

        execSync(`echo "goodbye world" > ${filePath}`);
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            return diff.diff && diff.diff.includes('goodbye world');
        });

        const diff2 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff2.diff).toContain('goodbye world');

        const panelContent = await page.textContent('.diff-scroll');
        expect(panelContent).toContain('goodbye world');

        execSync(`rm -f ${filePath}`);
        execSync(`cd ${worktreeDir} && git checkout -- ${testFile} 2>/dev/null || true`);
    });

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

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(fileA);
            expect(diffText).toContain(fileB);
        }).toPass({ timeout: 8000 });

        writeFileSync(join(worktreeDir, fileA), 'content of file A v2 modified');
        // Poll API until file A content is updated, then refresh UI
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            if (!diff.files || !diff.files[fileA]) return false;
            return diff.files[fileA].line_count >= 1;
        });

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('v2 modified');
        }).toPass({ timeout: 8000 });

        writeFileSync(join(worktreeDir, fileB), 'content of file B v2 modified');
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            if (!diff.files || !diff.files[fileB]) return false;
            return diff.files[fileB].line_count >= 1;
        });

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

        // Wait for the file to appear in the diff panel before setting up the route
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 8000 });

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
        // Poll API until the file is updated, then re-navigate to trigger the errored diff-file fetch
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            if (!diff.files || !diff.files[testFile]) return false;
            return true;
        });

        const hasDiffError = consoleErrors.some(err =>
            err.toLowerCase().includes('diff') ||
            err.toLowerCase().includes('fetch') ||
            err.toLowerCase().includes('file')
        );
        expect(hasDiffError).toBe(true);

        await page.unroute('**/api/projects/*/tasks/*/diff-file*');
        execSync(`rm -f ${join(worktreeDir, testFile)}`);
    });

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

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 8000 });

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo');
        }).toPass({ timeout: 5000 });

        writeFileSync(filePath, 'todo app');
        // Poll API until the file content reflects the modification
        await waitForDiffUpdate(page, taskInfo.projectId, taskInfo.taskId, (diff) => {
            if (!diff.files || !diff.files[testFile]) return false;
            return true;
        });

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo app');
        }).toPass({ timeout: 10000 });

        rmSync(filePath, { force: true });
    });

    test('diff panel shows correct content after multiple thread switches', async ({ page }) => {
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `thread-diff-${Date.now()}`);

        await ensureTaskStarted(page, projectId, taskId);

        await navigateToTask(page, taskId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

        let taskData = null;
        // Condition-based wait: poll until task has a branch and worktree exists
        await expect(async () => {
            taskData = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
                const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
                return res.ok ? res.json() : null;
            }, { pid: projectId, tid: taskId });
            const wtExists = worktreeExists(taskId);
            expect(!!(taskData?.branch && wtExists)).toBe(true);
        }).toPass({ timeout: 15000 });

        if (!worktreeExists(taskId)) {
            const projects = await page.evaluate(async (pid: string) => {
                const res = await fetch(`/api/projects`);
                const all = await res.json();
                return all.find((p: any) => p.id === pid);
            }, projectId);
            const projectPath = projects?.path;
            if (projectPath) {
                const branchName = taskData?.branch || `task/${taskId}`;
                const worktreePath = getWorktreePath(taskId);
                execSync(`cd ${projectPath} && git worktree add --detach ${worktreePath} HEAD 2>&1 || true`);
                execSync(`cd ${worktreePath} && git checkout -B ${branchName} 2>&1 || true`);
                await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
                    method: 'PUT',
                    body: { branch: branchName, base_ref: 'master' },
                });
            } else {
                test.skip(`Worktree not available for diff test (projectPath=${projectPath})`);
            }
        }

        const threadsResult = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/threads`);
            return res.json();
        }, { pid: projectId, tid: taskId });
        const mainThreadId = threadsResult[0]?.id;
        expect(mainThreadId).toBeTruthy();

        writeFileInWorktree(taskId, 'file-a.txt', 'hello friend');
        await waitForDiffFile(page, projectId, taskId, 'file-a.txt');

        const msgResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
            method: 'POST',
            body: { role: 'user', content: 'Change world to friend' },
        });
        expect(msgResult.ok).toBe(true);
        const msgId = msgResult.data?.id;

        const forkResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/fork`, {
            method: 'POST',
            body: { from_thread_id: mainThreadId, from_message_id: msgId },
        });
        expect(forkResult.ok).toBe(true);
        const forkThreadId = forkResult.data?.id;

        writeFileInWorktree(taskId, 'file-b.txt', 'hello choom');
        await waitForDiffFile(page, projectId, taskId, 'file-b.txt');

        // Round-trip switches (matches backend TestThreadSwitchWorktreeContent)
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
            method: 'POST',
            body: { thread_id: mainThreadId },
        });
        await waitForDiffFile(page, projectId, taskId, 'file-a.txt');

        await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
            method: 'POST',
            body: { thread_id: forkThreadId },
        });
        await waitForDiffFile(page, projectId, taskId, 'file-b.txt');

        await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
            method: 'POST',
            body: { thread_id: mainThreadId },
        });
        await waitForDiffFile(page, projectId, taskId, 'file-a.txt');

        await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
            method: 'POST',
            body: { thread_id: forkThreadId },
        });
        await waitForDiffFile(page, projectId, taskId, 'file-b.txt');

        await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
            method: 'POST',
            body: { thread_id: mainThreadId },
        });
        await waitForDiffFile(page, projectId, taskId, 'file-a.txt');
    });
});
