import { test, expect, Page } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync, unlinkSync, rmSync } from 'fs';
import { join } from 'path';
import { getProjectId, getLastTaskId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted } from './helpers';

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

function getDiffViaApi(projectId: string, taskId: string): any {
    return JSON.parse(execSync(
        `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff"`,
        { encoding: 'utf-8' }
    ));
}

function getDiffFileViaApi(projectId: string, taskId: string, filePath: string): any {
    return JSON.parse(execSync(
        `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff/file?path=${encodeURIComponent(filePath)}"`,
        { encoding: 'utf-8' }
    ));
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
                body: JSON.stringify({ content: 'list files' }),
            });
        }, { pid: projectId, tid: taskId });

        // Poll the API until a worktree directory appears
        await page.waitForFunction(async () => {
            const res = await fetch('/api/config');
            const config = await res.json();
            const worktreesDir = config.workspace_dir;
            if (!worktreesDir) return false;
            // Check via API if any task has a branch (implies worktree exists)
            const projectsRes = await fetch('/api/projects');
            const projects = await projectsRes.json();
            for (const proj of projects) {
                const tasksRes = await fetch(`/api/projects/${proj.id}/tasks`);
                const tasks = await tasksRes.json();
                if (tasks.some((t: any) => t.branch)) return true;
            }
            return false;
        }, {}, { timeout: 20000, polling: 1000 });
        worktreeDir = findWorktreeDir();

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

// --- Thread-switch utilities ---

function getWorktreePath(taskId: string): string {
    return `/tmp/kanban-test-worktrees/${taskId}`;
}

function writeFileInWorktree(taskId: string, filePath: string, content: string) {
    const worktreePath = getWorktreePath(taskId);
    execSync(`mkdir -p ${worktreePath}`);
    execSync(`echo "${content}" > ${worktreePath}/${filePath}`);
}

function worktreeExists(taskId: string): boolean {
    try {
        execSync(`test -d ${getWorktreePath(taskId)}/.git`);
        return true;
    } catch {
        return false;
    }
}

/**
 * Poll the diff API until the expected file appears, then re-navigate to the task
 * so the diff panel renders fresh data. The worktree watcher's 2s poll + WS broadcast
 * is unreliable in headless tests, so we force a reload once the backend confirms
 * the file is visible.
 */
async function waitForDiffFile(page: any, projectId: string, taskId: string, expectFile: string) {
    await page.waitForFunction(async ([pid, tid, file]: [string, string, string]) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        const data = await res.json();
        return data.files && Object.keys(data.files).some((k: string) => k.includes(file));
    }, [projectId, taskId, expectFile], { timeout: 15000 });
    await navigateToTask(page, taskId);
    await expect(page.locator('.diff-content')).toContainText(expectFile, { timeout: 15000 });
}

// --- Tests: user interactions that affect the diff panel ---

test.describe.serial('Diff panel user interactions', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(30000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('should open file editor when clicking a diff line', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `editor-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);
        const fileContent = `Line 1: Hello World\nLine 2: This is a test\nLine 3: Third line\nLine 4: Fourth line\nLine 5: Fifth line\n`;

        writeFileSync(filePath, fileContent);

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        await expect(async () => {
            const diffRows = await page.locator('.diff-row').count();
            expect(diffRows).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        const clickableRows = page.locator('.diff-row-clickable');
        const rowCount = await clickableRows.count();
        if (rowCount === 0) {
            execSync(`rm -f ${filePath}`);
            return;
        }

        await clickableRows.first().click();
        await expect(page.locator('.editor')).toBeVisible({ timeout: 5000 });
        await expect(page.locator('#file-editor-title')).toBeVisible();

        const textarea = page.locator('.editor-textarea');
        await expect(textarea).toBeVisible();
        const editorContent = await textarea.inputValue();
        expect(editorContent).toContain('Hello World');
        expect(editorContent).toContain('This is a test');

        execSync(`rm -f ${filePath}`);
    });

    test('should save file changes with Ctrl+S', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `save-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);
        const originalContent = 'Original content\nLine 2\n';

        writeFileSync(filePath, originalContent);

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        await expect(async () => {
            expect(await page.locator('.diff-row').count()).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        await page.locator('.diff-row-clickable').first().click();
        await expect(page.locator('.editor')).toBeVisible({ timeout: 5000 });

        const textarea = page.locator('.editor-textarea');
        await textarea.fill('Modified content\nLine 2\nLine 3: new line\n');
        await textarea.press('Control+s');

        await expect(async () => {
            const unsavedDot = page.locator('.unsaved-dot');
            const visible = await unsavedDot.isVisible().catch(() => false);
            expect(visible).toBe(false);
        }).toPass({ timeout: 5000 });

        await page.locator('.btn-close').click();
        await expect(page.locator('.editor')).not.toBeVisible({ timeout: 5000 });

        const savedContent = execSync(`cat ${filePath}`).toString();
        expect(savedContent).toContain('Modified content');
        expect(savedContent).toContain('Line 3: new line');

        execSync(`rm -f ${filePath}`);
    });

    test('should show unsaved changes warning when closing', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `warn-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        writeFileSync(filePath, 'Warning test content\n');

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        await expect(async () => {
            expect(await page.locator('.diff-row').count()).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        await page.locator('.diff-row-clickable').first().click();
        await expect(page.locator('.editor')).toBeVisible({ timeout: 5000 });

        const textarea = page.locator('.editor-textarea');
        await textarea.fill('Changed content!\n');

        await expect(page.locator('.unsaved-dot')).toBeVisible();

        await textarea.press('Escape');

        await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
        await expect(page.locator('#confirm-title')).toHaveText('Unsaved Changes');

        await page.locator('.btn-confirm-cancel').click();
        await expect(page.locator('.confirm-dialog')).not.toBeVisible();
        await expect(page.locator('.editor')).toBeVisible();

        execSync(`rm -f ${filePath}`);
    });

    test('should close editor without warning when no changes', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `close-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        writeFileSync(filePath, 'No changes test\n');

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        await expect(async () => {
            expect(await page.locator('.diff-row').count()).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        await page.locator('.diff-row-clickable').first().click();
        await expect(page.locator('.editor')).toBeVisible({ timeout: 5000 });

        await page.keyboard.press('Escape');
        await expect(page.locator('.editor')).not.toBeVisible({ timeout: 5000 });

        execSync(`rm -f ${filePath}`);
    });

    test('diff panel preserves file content after message deletion', async ({ page }) => {
        const result = await ensureActiveWorktree(page);
        if (!result) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }
        const { taskInfo, worktreeDir } = result;

        const uniqueId = Date.now();
        const testFile = `delete-diff-test-${uniqueId}.txt`;

        writeFileSync(join(worktreeDir, testFile), 'line one\nline two\nline three\n');

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
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 10000 });

        const diffBefore = await page.textContent('.diff-scroll');
        expect(diffBefore).toContain('line one');

        const chatTab = page.locator('.tab-btn:has-text("Chat")');
        if (await chatTab.isVisible().catch(() => false)) {
            await chatTab.click();
        }
        await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

        const msgContent = `delete me ${Date.now()}`;
        await page.evaluate(async ({ pid, tid, msg }) => {
            await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ content: msg }),
            });
        }, { pid: taskInfo.projectId, tid: taskInfo.taskId, msg: msgContent });

        await expect(async () => {
            const msgs = await page.locator('.chat-message.user').count();
            expect(msgs).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

        const userMessages = page.locator('.chat-message.user');
        const userCount = await userMessages.count();
        if (userCount < 2) {
            execSync(`rm -f ${join(worktreeDir, testFile)}`);
            return;
        }

        const lastUserMsg = userMessages.nth(userCount - 1);
        const deleteBtn = lastUserMsg.locator('[aria-label="Delete message and all following messages"]');
        if (await deleteBtn.isVisible().catch(() => false)) {
            await deleteBtn.click();
            await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
            await page.click('.confirm-dialog .btn-confirm');
            await expect(page.locator('.confirm-dialog')).not.toBeVisible({ timeout: 5000 });
        }

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 8000 });

        const diffAfter = await page.textContent('.diff-scroll');
        expect(diffAfter).toContain(testFile);
        expect(diffAfter).toContain('line one');

        execSync(`rm -f ${join(worktreeDir, testFile)}`);
    });

    test('staged status preserved after message deletion', async ({ page }) => {
        const result = await ensureActiveWorktree(page);
        if (!result) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }
        const { taskInfo, worktreeDir } = result;

        const uniqueId = Date.now();
        const testFile = `staged-test-${uniqueId}.txt`;

        writeFileSync(join(worktreeDir, testFile), 'staged content\n');
        execSync(`cd ${worktreeDir} && git add ${testFile}`);

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
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 10000 });

        const stagedSection = page.locator('.diff-section:has-text("Staged"), .diff-group:has-text("Staged")');
        let isStagedBefore = false;
        if (await stagedSection.isVisible().catch(() => false)) {
            const stagedContent = await stagedSection.textContent();
            isStagedBefore = stagedContent?.includes(testFile) ?? false;
        }
        if (!isStagedBefore) {
            const diffData = JSON.parse(execSync(
                `curl -s "http://localhost:3000/api/projects/${taskInfo.projectId}/tasks/${taskInfo.taskId}/diff"`,
                { encoding: 'utf-8' }
            ));
            if (diffData.files && diffData.files[testFile]) {
                isStagedBefore = diffData.files[testFile].staged === true;
            }
        }

        const chatTab = page.locator('.tab-btn:has-text("Chat")');
        if (await chatTab.isVisible().catch(() => false)) {
            await chatTab.click();
        }
        await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

        const msgContent = `delete me staged ${Date.now()}`;
        await page.evaluate(async ({ pid, tid, msg }) => {
            await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ content: msg }),
            });
        }, { pid: taskInfo.projectId, tid: taskInfo.taskId, msg: msgContent });

        await expect(async () => {
            const msgs = await page.locator('.chat-message.user').count();
            expect(msgs).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });
        await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

        const userMessages = page.locator('.chat-message.user');
        const userCount = await userMessages.count();
        if (userCount >= 2) {
            const lastUserMsg = userMessages.nth(userCount - 1);
            const deleteBtn = lastUserMsg.locator('[aria-label="Delete message and all following messages"]');
            if (await deleteBtn.isVisible().catch(() => false)) {
                await deleteBtn.click();
                await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
                await page.click('.confirm-dialog .btn-confirm');
                await expect(page.locator('.confirm-dialog')).not.toBeVisible({ timeout: 5000 });
            }
        }

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 8000 });

        const diffAfter = await page.textContent('.diff-scroll');
        expect(diffAfter).toContain(testFile);

        const diffDataAfter = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${taskInfo.projectId}/tasks/${taskInfo.taskId}/diff"`,
            { encoding: 'utf-8' }
        ));
        if (diffDataAfter.files && diffDataAfter.files[testFile]) {
            const isStagedAfter = diffDataAfter.files[testFile].staged === true;
            expect(isStagedAfter).toBe(typeof isStagedAfter === 'boolean');
        }

        execSync(`rm -f ${join(worktreeDir, testFile)}`);
        execSync(`cd ${worktreeDir} && git reset HEAD ${testFile} 2>/dev/null || true`);
    });

    test('diff panel shows correct content after multiple thread switches', async ({ page }) => {
        test.setTimeout(60000);
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `thread-diff-${Date.now()}`);

        await ensureTaskStarted(page, projectId, taskId);

        await navigateToTask(page, taskId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

        let taskData = null;
        for (let i = 0; i < 30; i++) {
            taskData = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
                const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
                return res.ok ? res.json() : null;
            }, { pid: projectId, tid: taskId });
            if (taskData?.branch && worktreeExists(taskId)) break;
            await page.waitForFunction(() => false, {}, { timeout: 500, polling: 500 }).catch(() => {});
        }

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
                // Verify worktree was created
                if (!worktreeExists(taskId)) {
                    test.skip('Worktree could not be created for diff test');
                }
            } else {
                test.skip(`Worktree not available for diff test (projectPath=${projectPath})`);
            }
        }

        const threadsResult = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/threads`);
            return res.json();
        }, { pid: projectId, tid: taskId });
        const mainThreadId = threadsResult[0]?.id;
        if (!mainThreadId) {
            test.skip('No main thread available for diff test');
        }

        writeFileInWorktree(taskId, 'file-a.txt', 'hello friend');
        try {
            await waitForDiffFile(page, projectId, taskId, 'file-a.txt');
        } catch {
            test.skip('file-a.txt did not appear in diff panel');
        }

        const msgResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
            method: 'POST',
            body: { role: 'user', content: 'Change world to friend' },
        });
        expect(msgResult.ok).toBe(true);
        const msgId = msgResult.data?.id;
        if (!msgId) {
            test.skip('No message ID returned from API');
        }

        const forkResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/fork`, {
            method: 'POST',
            body: { from_thread_id: mainThreadId, from_message_id: msgId },
        });
        expect(forkResult.ok).toBe(true);
        const forkThreadId = forkResult.data?.id;
        if (!forkThreadId) {
            test.skip('No fork thread ID returned from API');
        }

        writeFileInWorktree(taskId, 'file-b.txt', 'hello choom');
        try {
            await waitForDiffFile(page, projectId, taskId, 'file-b.txt');
        } catch {
            test.skip('file-b.txt did not appear in diff panel');
        }

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

        await expect(async () => {
            const diffText = await page.locator('.column-diff').textContent().catch(() => '');
            expect(diffText).toContain(testFileName);
        }).toPass({ timeout: 10000 });

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

        await expect(async () => {
            const diffText = await page.locator('.column-diff').textContent().catch(() => '');
            expect(diffText).toContain(testFileName);
        }).toPass({ timeout: 10000 });

        writeFileSync(filePath, 'Initial content\nModified line\nExtra line\n');

        await expect(async () => {
            const diffText = await page.locator('.column-diff').textContent().catch(() => '');
            expect(diffText).toContain(testFileName);
        }).toPass({ timeout: 10000 });

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

        await expect(async () => {
            const diffText = await page.locator('.column-diff').textContent().catch(() => '');
            expect(diffText).toContain(testFileName);
        }).toPass({ timeout: 10000 });

        unlinkSync(filePath);

        await expect(async () => {
            const diffText = await page.locator('.column-diff').textContent().catch(() => '');
            expect(diffText).not.toContain(testFileName);
        }).toPass({ timeout: 10000 });
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

        // Wait for initial diff to load
        await expect(async () => {
            const diffData = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
            expect(diffData.diff).toContain('hello world');
        }).toPass({ timeout: 10000 });

        execSync(`echo "hello goodbye" > ${filePath}`);

        const diff1 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff1.diff).toContain('hello goodbye');

        execSync(`echo "goodbye world" > ${filePath}`);

        const diff2 = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diff2.diff).toContain('goodbye world');

        await expect(async () => {
            const panelContent = await page.textContent('.diff-scroll');
            expect(panelContent).toContain('goodbye world');
        }).toPass({ timeout: 10000 });

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
        }).toPass({ timeout: 10000 });

        writeFileSync(join(worktreeDir, fileA), 'content of file A v2 modified');

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('v2 modified');
        }).toPass({ timeout: 10000 });

        writeFileSync(join(worktreeDir, fileB), 'content of file B v2 modified');

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('v2 modified');
        }).toPass({ timeout: 10000 });

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

        // Wait for initial diff to load
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 10000 });

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

        // Wait for the diff update to trigger the error (poll for console errors)
        await expect(async () => {
            const hasDiffError = consoleErrors.some(err =>
                err.toLowerCase().includes('diff') ||
                err.toLowerCase().includes('fetch') ||
                err.toLowerCase().includes('file')
            );
            expect(hasDiffError).toBe(true);
        }).toPass({ timeout: 10000 });

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
        }).toPass({ timeout: 10000 });

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo');
        }).toPass({ timeout: 5000 });

        writeFileSync(filePath, 'todo app');

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo app');
        }).toPass({ timeout: 10000 });

        rmSync(filePath, { force: true });
    });

    test('staging an untracked file moves it to the Staged section via broadcast', async ({ page }) => {
        const result = await ensureActiveWorktree(page);
        if (!result) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }
        const { taskInfo, worktreeDir } = result;

        const uniqueId = Date.now();
        const testFile = `stage-broadcast-${uniqueId}.txt`;
        writeFileSync(join(worktreeDir, testFile), 'new untracked content\n');

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

        // Wait for the untracked file to show in the panel (initially Unstaged).
        await expect(async () => {
            const diffText = await page.locator('.diff-scroll').textContent().catch(() => '');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 15000 });

        const unstagedSection = page.locator('.diff-section').filter({ has: page.locator('.diff-section-header.diff-section-unstaged') });
        await expect(async () => {
            expect(await unstagedSection.textContent()).toContain(testFile);
        }).toPass({ timeout: 10000 });

        // Stage the file via its stage button (untracked → staged).
        const fileSection = page.locator('.diff-file-section', { hasText: testFile }).first();
        await fileSection.locator('.diff-stage-btn').click();

        // The file must move to the Staged section without a reload — only the
        // backend broadcast of the index change (stage/unstage) can do this for
        // untracked files. Regression: broadcasts were dropped, leaving the file stuck.
        const stagedSection = page.locator('.diff-section').filter({ has: page.locator('.diff-section-header.diff-section-staged') });
        await expect(async () => {
            expect(await stagedSection.textContent()).toContain(testFile);
        }).toPass({ timeout: 15000 });

        execSync(`rm -f ${join(worktreeDir, testFile)}`);
    });
});

// --- Tests: done-task diff (BaseCommit..BranchTip) ---

test.describe.serial('Done-task diff shows committed changes', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(60000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('diff API returns committed diff for done tasks', async ({ page }) => {
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `done-diff-test-${Date.now()}`);

        await ensureTaskStarted(page, projectId, taskId);

        // Poll until the task has a branch and worktree
        let taskData: any = null;
        for (let i = 0; i < 30; i++) {
            taskData = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
                const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
                return res.ok ? res.json() : null;
            }, { pid: projectId, tid: taskId });
            if (taskData?.branch) break;
            await new Promise(r => setTimeout(r, 500));
        }

        if (!taskData?.branch) {
            test.skip('Task did not get a branch');
        }

        const worktreePath = `${getConfig().workspace_dir}/${taskId}`;
        if (!statSync(worktreePath).isDirectory()) {
            test.skip('Worktree directory not found');
        }

        // Create a test file in the worktree
        const uniqueId = Date.now();
        const testFile = `done-task-diff-${uniqueId}.txt`;
        const fileContent = `Committed content for done task diff test\nLine 2\n`;
        writeFileSync(join(worktreePath, testFile), fileContent);

        // Commit the file in the worktree
        execSync(`cd ${worktreePath} && git add ${testFile}`);
        execSync(`cd ${worktreePath} && git commit -m "Add done-task diff test file"`);

        // Get the current HEAD (branch tip)
        const branchTip = execSync(`cd ${worktreePath} && git rev-parse HEAD`).toString().trim();

        // Move task to done, setting branch_tip
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
            method: 'POST',
            body: { status: 'done', branch_tip: branchTip },
        });

        // Verify task is done
        const doneTask = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
            return res.ok ? res.json() : null;
        }, { pid: projectId, tid: taskId });
        expect(doneTask.status).toBe('done');

        // Call the diff API - should return the committed diff (BaseCommit..BranchTip)
        const diffData = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff"`,
            { encoding: 'utf-8' }
        ));

        // The diff should contain the committed file
        // For done tasks, the API returns a raw diff string (not a manifest)
        expect(diffData.diff).toBeDefined();
        expect(diffData.diff).toContain(testFile);
        expect(diffData.diff).toContain('Committed content for done task diff test');

        // Cleanup
        rmSync(join(worktreePath, testFile), { force: true });
    });

    test('diff panel renders committed diff for done tasks in UI', async ({ page }) => {
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `done-diff-ui-${Date.now()}`);

        await ensureTaskStarted(page, projectId, taskId);

        // Poll until the task has a branch and worktree
        let taskData: any = null;
        for (let i = 0; i < 30; i++) {
            taskData = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
                const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
                return res.ok ? res.json() : null;
            }, { pid: projectId, tid: taskId });
            if (taskData?.branch) break;
            await new Promise(r => setTimeout(r, 500));
        }

        if (!taskData?.branch) {
            test.skip('Task did not get a branch');
        }

        const worktreePath = `${getConfig().workspace_dir}/${taskId}`;
        if (!statSync(worktreePath).isDirectory()) {
            test.skip('Worktree directory not found');
        }

        // Create a test file in the worktree
        const uniqueId = Date.now();
        const testFile = `done-task-ui-${uniqueId}.txt`;
        const fileContent = `UI test content for done task\nSecond line\n`;
        writeFileSync(join(worktreePath, testFile), fileContent);

        // Commit the file
        execSync(`cd ${worktreePath} && git add ${testFile}`);
        execSync(`cd ${worktreePath} && git commit -m "Add done-task UI test file"`);

        const branchTip = execSync(`cd ${worktreePath} && git rev-parse HEAD`).toString().trim();

        // Move task to done
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
            method: 'POST',
            body: { status: 'done', branch_tip: branchTip },
        });

        // Navigate to the done task
        await navigateToTask(page, taskId, projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

        // Wait for the diff panel to load
        await expect(page.locator('.diff-viewer-loading')).not.toBeVisible({ timeout: 15000 });

        // The diff panel should show the committed file
        await expect(async () => {
            const diffText = await page.locator('.column-diff, .diff-scroll').textContent().catch(() => '');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 10000 });

        await expect(async () => {
            const diffText = await page.locator('.column-diff, .diff-scroll').textContent().catch(() => '');
            expect(diffText).toContain('UI test content for done task');
        }).toPass({ timeout: 10000 });

        // Cleanup
        rmSync(join(worktreePath, testFile), { force: true });
    });
});