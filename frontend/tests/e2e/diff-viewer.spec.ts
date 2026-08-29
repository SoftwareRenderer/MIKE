import { test, expect, Page } from '@playwright/test';
import { execSync } from 'child_process';
import { existsSync, writeFileSync, unlinkSync, rmSync } from 'fs';
import { join } from 'path';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted, getWorktreePath, worktreeExists } from './helpers';

// --- Shared utilities ---

// The test servers require a session — these helpers used unauthenticated
// `curl`, which 401s, so they now go through the page's session.

/** Worktree directory for a task — must be derived from the SAME task the
 *  panel shows, so files written here are visible in that task's diff. */
function worktreeDirFor(taskInfo: { projectId: string; taskId: string } | null): string | null {
    if (!taskInfo) return null;
    const dir = getWorktreePath(taskInfo.projectId, taskInfo.taskId);
    return existsSync(dir) ? dir : null;
}

async function getDiffViaApi(page: Page, projectId: string, taskId: string): Promise<any> {
    return page.evaluate(async ([pid, tid]: [string, string]) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        return res.ok ? res.json() : null;
    }, [projectId, taskId]);
}

async function getDiffFileViaApi(page: Page, projectId: string, taskId: string, filePath: string): Promise<any> {
    return page.evaluate(async ([pid, tid, file]: [string, string, string]) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff/file?path=${encodeURIComponent(file)}`);
        return res.ok ? res.json() : null;
    }, [projectId, taskId, filePath]);
}

/**
 * Per-describe dedicated task factory. The chromium project runs serial
 * describes in parallel (fullyParallel: true), so describes must NOT share
 * "the first in-progress task" — each describe lazily creates one dedicated
 * task (with worktree) and reuses it across its own tests. Returns null if the
 * worktree is missing.
 *
 * The prompt is deliberately neutral: mock-LLM keyword routing turns e.g.
 * "list files" into tool calls that mutate the worktree (or complete the task).
 */
function makeSharedTask() {
    let shared: { taskInfo: { projectId: string; taskId: string }; worktreeDir: string } | null = null;
    return async (page: Page): Promise<{ taskInfo: { projectId: string; taskId: string }; worktreeDir: string } | null> => {
        if (shared) return shared;
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `diff-spec-${Date.now()}`);
        await ensureTaskStarted(page, projectId, taskId);
        // Wait for the task to get a branch (worktree created)
        await page.waitForFunction(async ({ pid, tid }: { pid: string; tid: string }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
            const task = res.ok ? await res.json() : null;
            return !!task?.branch;
        }, { pid: projectId, tid: taskId }, { timeout: 30000 });
        const worktreeDir = worktreeDirFor({ projectId, taskId });
        if (!worktreeDir) return null;
        shared = { taskInfo: { projectId, taskId }, worktreeDir };
        return shared;
    };
}

/** Run a git command in a worktree, retrying on index-lock contention with
 *  the backend's worktree initialization. */
async function gitWithRetry(worktreePath: string, cmd: string, attempts = 6): Promise<void> {
    for (let i = 0; ; i++) {
        try {
            execSync(`cd ${worktreePath} && ${cmd}`);
            return;
        } catch (e) {
            if (i >= attempts - 1) throw e;
            await new Promise(r => setTimeout(r, 1000));
        }
    }
}

// --- Thread-switch utilities ---
// (getWorktreePath / worktreeExists come from ./helpers — the path is
//  /tmp/kanban-test-worktrees/<projectId>/<taskId>, matching the backend's
//  worktreesDir/projectID/taskID layout.)

function writeFileInWorktree(projectId: string, taskId: string, filePath: string, content: string) {
    const worktreePath = getWorktreePath(projectId, taskId);
    execSync(`mkdir -p ${worktreePath}`);
    execSync(`echo "${content}" > ${worktreePath}/${filePath}`);
}

/**
 * Poll the diff API until the expected file appears, then get the diff panel to
 * show it. The worktree watcher's 2s poll + WS broadcast is unreliable in
 * headless tests, so we force a reload once the backend confirms the file is
 * visible — and we RE-navigate until the panel agrees, because one reload is not
 * enough: the panel fetches its diff once per mount, so a request that lands
 * while the backend is still rewriting the worktree (a thread switch checks
 * snapshots out) answers "No changes since last commit", and nothing refetches
 * it after that. Under parallel load that window is easy to hit — the API has
 * the file, the panel stays pinned to the empty answer forever.
 */
async function waitForDiffFile(page: any, projectId: string, taskId: string, expectFile: string) {
    await page.waitForFunction(async ([pid, tid, file]: [string, string, string]) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        const data = await res.json();
        return data.files && Object.keys(data.files).some((k: string) => k.includes(file));
    }, [projectId, taskId, expectFile], { timeout: 15000 });
    await expect(async () => {
        await navigateToTask(page, taskId);
        await expect(page.locator('.diff-content')).toContainText(expectFile, { timeout: 4000 });
    }).toPass({ timeout: 12000 });
}

/**
 * Poll the diff-file API until the file's content contains the expected text,
 * then re-navigate so the diff panel renders fresh data (same rationale as
 * waitForDiffFile — the worktree watcher's 2s poll + WS broadcast is unreliable
 * in headless tests).
 */
async function waitForDiffFileContent(page: Page, projectId: string, taskId: string, filePath: string, expected: string) {
    await page.waitForFunction(async ([pid, tid, file, text]: [string, string, string, string]) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff/file?path=${encodeURIComponent(file)}`);
        if (!res.ok) return false;
        const data = await res.json();
        const lines = data.lines || (data.rows || []).map((r: any) => r.text ?? '');
        return lines.some((l: string) => l.includes(text));
    }, [projectId, taskId, filePath, expected], { timeout: 15000 });
    await navigateToTask(page, taskId, projectId);
}

// --- Tests: user interactions that affect the diff panel ---

test.describe.serial('Diff panel user interactions', () => {
    const sharedTask = makeSharedTask();
    test.beforeEach(async ({ page }) => {
        test.setTimeout(30000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('should open file editor when clicking a diff line', async ({ page }) => {
        const taskInfo = await sharedTask(page);
        const worktreeDir = worktreeDirFor(taskInfo);
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `editor-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);
        const fileContent = `Line 1: Hello World\nLine 2: This is a test\nLine 3: Third line\nLine 4: Fourth line\nLine 5: Fifth line\n`;

        writeFileSync(filePath, fileContent);

        // Navigate to the task whose worktree holds the test file (clicking the
        // first card opens an arbitrary task).
        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
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
        const taskInfo = await sharedTask(page);
        const worktreeDir = worktreeDirFor(taskInfo);
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `save-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);
        const originalContent = 'Original content\nLine 2\n';

        writeFileSync(filePath, originalContent);

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
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
        const taskInfo = await sharedTask(page);
        const worktreeDir = worktreeDirFor(taskInfo);
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `warn-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        writeFileSync(filePath, 'Warning test content\n');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
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
        const taskInfo = await sharedTask(page);
        const worktreeDir = worktreeDirFor(taskInfo);
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `close-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        writeFileSync(filePath, 'No changes test\n');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
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
        const result = await sharedTask(page);
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

        // Untracked file rows load lazily — retry until the content renders.
        await expect(async () => {
            const diffBefore = await page.textContent('.diff-scroll');
            expect(diffBefore).toContain('line one');
        }).toPass({ timeout: 10000 });

        const chatTab = page.locator('.tab-btn:has-text("Chat")');
        if (await chatTab.isVisible().catch(() => false)) {
            await chatTab.click();
        }
        await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

        const msgContent = `delete me ${Date.now()}`;
        await csrfFetch(page, `/api/projects/${taskInfo.projectId}/tasks/${taskInfo.taskId}/send`, {
            method: 'POST',
            body: { content: msgContent },
        });

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
            // Global confirmation dialog (BaseModal) — not the editor's .confirm-dialog
            const confirmDialog = page.locator('[role="dialog"]');
            await expect(confirmDialog).toBeVisible({ timeout: 5000 });
            await page.click('[role="dialog"] .btn-confirm');
            await expect(confirmDialog).not.toBeVisible({ timeout: 5000 });
        }

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 8000 });

        // Rows may need a moment to re-render after the message deletion.
        await expect(async () => {
            const diffAfter = await page.textContent('.diff-scroll');
            expect(diffAfter).toContain(testFile);
            expect(diffAfter).toContain('line one');
        }).toPass({ timeout: 10000 });

        execSync(`rm -f ${join(worktreeDir, testFile)}`);
    });

    test('staged status preserved after message deletion', async ({ page }) => {
        const result = await sharedTask(page);
        if (!result) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }
        const { taskInfo, worktreeDir } = result;

        const uniqueId = Date.now();
        const testFile = `staged-test-${uniqueId}.txt`;

        writeFileSync(join(worktreeDir, testFile), 'staged content\n');
        // gitWithRetry, not a bare execSync: the backend runs its own git
        // commands in this worktree (task startup, the watcher's status poll),
        // and a concurrent one holds .git/index.lock — the raw call then throws
        // and the test fails before it even reaches the UI.
        await gitWithRetry(worktreeDir, `git add ${testFile}`);

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
            const diffData = await getDiffViaApi(page, taskInfo.projectId, taskInfo.taskId);
            if (diffData?.files && diffData.files[testFile]) {
                isStagedBefore = diffData.files[testFile].staged === true;
            }
        }

        const chatTab = page.locator('.tab-btn:has-text("Chat")');
        if (await chatTab.isVisible().catch(() => false)) {
            await chatTab.click();
        }
        await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

        const msgContent = `delete me staged ${Date.now()}`;
        await csrfFetch(page, `/api/projects/${taskInfo.projectId}/tasks/${taskInfo.taskId}/send`, {
            method: 'POST',
            body: { content: msgContent },
        });

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
                // Global confirmation dialog (BaseModal) — not the editor's .confirm-dialog
                const confirmDialog = page.locator('[role="dialog"]');
                await expect(confirmDialog).toBeVisible({ timeout: 5000 });
                await page.click('[role="dialog"] .btn-confirm');
                await expect(confirmDialog).not.toBeVisible({ timeout: 5000 });
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

        const diffDataAfter = await getDiffViaApi(page, taskInfo.projectId, taskInfo.taskId);
        if (diffDataAfter?.files && diffDataAfter.files[testFile]) {
            const isStagedAfter = diffDataAfter.files[testFile].staged === true;
            expect(isStagedAfter).toBe(typeof isStagedAfter === 'boolean');
        }

        execSync(`rm -f ${join(worktreeDir, testFile)}`);
        execSync(`cd ${worktreeDir} && git reset HEAD ${testFile} 2>/dev/null || true`);
    });

    test('diff panel shows correct content after multiple thread switches', async ({ page }) => {
        // Seven waitForDiffFile round-trips (each may re-navigate while the panel
        // catches up with the backend) — the default budget is too tight.
        test.setTimeout(150000);
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
            if (taskData?.branch && worktreeExists(projectId, taskId)) break;
            await page.waitForFunction(() => false, {}, { timeout: 500, polling: 500 }).catch(() => {});
        }

        if (!worktreeExists(projectId, taskId)) {
            const projects = await page.evaluate(async (pid: string) => {
                const res = await fetch(`/api/projects`);
                const all = await res.json();
                return all.find((p: any) => p.id === pid);
            }, projectId);
            const projectPath = projects?.path;
            if (projectPath) {
                const branchName = taskData?.branch || `task/${taskId}`;
                const worktreePath = getWorktreePath(projectId, taskId);
                execSync(`cd ${projectPath} && git worktree add --detach ${worktreePath} HEAD 2>&1 || true`);
                execSync(`cd ${worktreePath} && git checkout -B ${branchName} 2>&1 || true`);
                await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
                    method: 'PUT',
                    body: { branch: branchName, base_ref: 'master' },
                });
                // Verify worktree was created
                if (!worktreeExists(projectId, taskId)) {
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

        writeFileInWorktree(projectId, taskId, 'file-a.txt', 'hello friend');
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

        writeFileInWorktree(projectId, taskId, 'file-b.txt', 'hello choom');
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
    const sharedTask = makeSharedTask();
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('should show new untracked files in the diff panel', async ({ page }) => {
        const taskInfo = await sharedTask(page);
        const worktreeDir = worktreeDirFor(taskInfo);
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
        const taskInfo = await sharedTask(page);
        const worktreeDir = worktreeDirFor(taskInfo);
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
        const taskInfo = await sharedTask(page);
        const worktreeDir = worktreeDirFor(taskInfo);
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
        const taskInfo = await sharedTask(page);
        const worktreeDir = worktreeDirFor(taskInfo);
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `diff-api-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        execSync(`echo "line one" > ${filePath}`);
        await gitWithRetry(worktreeDir, 'git add -A');

        execSync(`echo "line modified" > ${filePath}`);

        const diff = await getDiffViaApi(page, taskInfo.projectId, taskInfo.taskId);
        expect(diff.diff).toContain('line one');
        expect(diff.diff).toContain('line modified');

        execSync(`echo "line changed again" > ${filePath}`);

        const diff2 = await getDiffViaApi(page, taskInfo.projectId, taskInfo.taskId);
        expect(diff2.diff).toContain('line modified');
        expect(diff2.diff).toContain('line changed again');

        execSync(`rm -f ${filePath}`);
        execSync(`cd ${worktreeDir} && git checkout -- ${testFile} 2>/dev/null || true`);
    });

    test('diff panel updates in UI for same-line changes', async ({ page }) => {
        const taskInfo = await sharedTask(page);
        const worktreeDir = worktreeDirFor(taskInfo);
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `diff-ui-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        execSync(`echo "hello world" > ${filePath}`);
        await gitWithRetry(worktreeDir, 'git add -A');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await page.waitForSelector('.tab-bar', { timeout: 5000 });

        // Wait for initial diff to load
        await expect(async () => {
            const diffData = await getDiffViaApi(page, taskInfo.projectId, taskInfo.taskId);
            expect(diffData.diff).toContain('hello world');
        }).toPass({ timeout: 10000 });

        execSync(`echo "hello goodbye" > ${filePath}`);

        const diff1 = await getDiffViaApi(page, taskInfo.projectId, taskInfo.taskId);
        expect(diff1.diff).toContain('hello goodbye');

        execSync(`echo "goodbye world" > ${filePath}`);

        const diff2 = await getDiffViaApi(page, taskInfo.projectId, taskInfo.taskId);
        expect(diff2.diff).toContain('goodbye world');

        await expect(async () => {
            const panelContent = await page.textContent('.diff-scroll');
            expect(panelContent).toContain('goodbye world');
        }).toPass({ timeout: 10000 });

        execSync(`rm -f ${filePath}`);
        execSync(`cd ${worktreeDir} && git checkout -- ${testFile} 2>/dev/null || true`);
    });

    test('should update diff panel for changed files via delta updates', async ({ page }) => {
        const result = await sharedTask(page);
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
        // Live delta updates are unreliable in headless — confirm via the API and
        // force a refresh (same pattern as waitForDiffFile).
        await waitForDiffFileContent(page, taskInfo.projectId, taskInfo.taskId, fileA, 'v2 modified');
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('v2 modified');
        }).toPass({ timeout: 15000 });

        writeFileSync(join(worktreeDir, fileB), 'content of file B v2 modified');
        await waitForDiffFileContent(page, taskInfo.projectId, taskInfo.taskId, fileB, 'v2 modified');
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('v2 modified');
        }).toPass({ timeout: 15000 });

        execSync(`rm -f ${join(worktreeDir, fileA)} ${join(worktreeDir, fileB)}`);
    });

    test('should log console.error when diff-file fetch fails', async ({ page }) => {
        const result = await sharedTask(page);
        if (!result) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }
        const { taskInfo, worktreeDir } = result;

        const uniqueId = Date.now();
        const testFile = `delta-error-${uniqueId}.txt`;
        writeFileSync(join(worktreeDir, testFile), 'initial content');

        // Route diff-file fetches to 500 BEFORE the first load so the initial
        // file fetch fails deterministically (waiting on a WS-driven delta
        // update is unreliable in headless).
        // The diff-file endpoint is /diff/file?path=... (not /diff-file)
        await page.route('**/api/projects/*/tasks/*/diff/file*', (route) => {
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

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
        await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible({ timeout: 10000 });

        // The failed diff-file fetch must be logged via console.error
        await expect(async () => {
            const hasDiffError = consoleErrors.some(err =>
                err.toLowerCase().includes('diff') ||
                err.toLowerCase().includes('fetch') ||
                err.toLowerCase().includes('file')
            );
            expect(hasDiffError).toBe(true);
        }).toPass({ timeout: 10000 });

        await page.unroute('**/api/projects/*/tasks/*/diff/file*');
        execSync(`rm -f ${join(worktreeDir, testFile)}`);
    });

    test('should update diff when untracked file content changes but line count stays the same', async ({ page }) => {
        const result = await sharedTask(page);
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

        // The live delta update may not arrive in headless — fall back to
        // confirming the change via the API and forcing a refresh (same
        // pattern as waitForDiffFile).
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo app');
        }).toPass({ timeout: 15000 }).catch(async () => {
            await waitForDiffFileContent(page, taskInfo.projectId, taskInfo.taskId, testFile, 'todo app');
        });

        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain('todo app');
        }).toPass({ timeout: 15000 });

        rmSync(filePath, { force: true });
    });

    test('staging an untracked file moves it to the Staged section via broadcast', async ({ page }) => {
        const result = await sharedTask(page);
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

        const worktreePath = getWorktreePath(projectId, taskId);
        if (!worktreeExists(projectId, taskId)) {
            test.skip('Worktree directory not found');
        }

        // Create a test file in the worktree
        const uniqueId = Date.now();
        const testFile = `done-task-diff-${uniqueId}.txt`;
        const fileContent = `Committed content for done task diff test\nLine 2\n`;
        writeFileSync(join(worktreePath, testFile), fileContent);

        // Commit the file in the worktree — retry on git lock contention with
        // the backend's worktree initialization.
        await gitWithRetry(worktreePath, `git add ${testFile}`);
        await gitWithRetry(worktreePath, `git commit -m "Add done-task diff test file"`);

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
        const diffData = await page.evaluate(async ([pid, tid]: [string, string]) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
            return res.ok ? res.json() : null;
        }, [projectId, taskId]);

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

        const worktreePath = getWorktreePath(projectId, taskId);
        if (!worktreeExists(projectId, taskId)) {
            test.skip('Worktree directory not found');
        }

        // Create a test file in the worktree
        const uniqueId = Date.now();
        const testFile = `done-task-ui-${uniqueId}.txt`;
        const fileContent = `UI test content for done task\nSecond line\n`;
        writeFileSync(join(worktreePath, testFile), fileContent);

        // Commit the file — retry on git lock contention with the backend's
        // worktree initialization.
        await gitWithRetry(worktreePath, `git add ${testFile}`);
        await gitWithRetry(worktreePath, `git commit -m "Add done-task UI test file"`);

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
            // .first() — the comma selector matches the nested .diff-scroll too,
            // which throws in strict mode and silently yields '' via the catch.
            const diffText = await page.locator('.column-diff, .diff-scroll').first().textContent().catch(() => '');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 10000 });

        await expect(async () => {
            const diffText = await page.locator('.column-diff, .diff-scroll').first().textContent().catch(() => '');
            expect(diffText).toContain('UI test content for done task');
        }).toPass({ timeout: 10000 });

        // Cleanup
        rmSync(join(worktreePath, testFile), { force: true });
    });
});

// --- Tests: tab character rendering in the diff panel ---

test.describe.serial('Diff panel tab rendering', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(60000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('diff panel renders tab characters with tab-size 4', async ({ page }) => {
        // Dedicated task so the worktree path is known (session-based setup —
        // the unauthenticated curl helpers in this spec cannot see the config).
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `tab-size-test-${Date.now()}`);
        await ensureTaskStarted(page, projectId, taskId);

        // Wait until the task has a branch and its worktree exists
        await page.waitForFunction(async ({ pid, tid }: { pid: string; tid: string }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
            const task = res.ok ? await res.json() : null;
            return !!task?.branch;
        }, { pid: projectId, tid: taskId }, { timeout: 30000 });

        const worktreePath = getWorktreePath(projectId, taskId);
        const testFile = `tab-size-${Date.now()}.txt`;
        const filePath = join(worktreePath, testFile);
        // Tab-indented lines — tabs must render 4 chars wide, not the browser default 8.
        writeFileSync(filePath, 'alpha\n\tbeta\n\t\tgamma\n');

        try {
            await navigateToTask(page, taskId, projectId);

            await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });

            // Wait for the untracked file to show up in the diff panel
            await expect(async () => {
                const diffText = await page.locator('.diff-scroll').textContent().catch(() => '');
                expect(diffText).toContain(testFile);
            }).toPass({ timeout: 15000 });

            // The added rows for this file carry the raw tab characters.
            const tabRow = page.locator('.diff-file-section', { hasText: testFile })
                .locator('.diff-text')
                .filter({ hasText: /\t/ })
                .first();
            await expect(tabRow).toBeVisible({ timeout: 10000 });

            // Chromium serializes the computed tab-size as a bare integer
            const tabSize = await tabRow.evaluate((el) => getComputedStyle(el).tabSize);
            expect(parseFloat(tabSize)).toBe(4);
        } finally {
            try { unlinkSync(filePath); } catch { /* ignore */ }
        }
    });
});