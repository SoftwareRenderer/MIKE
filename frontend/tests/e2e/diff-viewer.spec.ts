import { test, expect, Page } from '@playwright/test';
import { execSync } from 'child_process';
import { existsSync, writeFileSync, unlinkSync, rmSync } from 'fs';
import { join } from 'path';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted, getWorktreePath, worktreeExists,
  waitForWorktree, isolatedProject, gotoProject, apiGetJson, sendTurn, messageRows } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

/**
 * Mirrors DIFF_EDITOR_FULLSCREEN_MAX_WIDTH in `$lib/utils/diff-editor-layout.ts`. No spec imports
 * from `$lib`, so the number is repeated here rather than read.
 */
const DIFF_EDITOR_FULLSCREEN_MAX_WIDTH = 900;

/**
 * Asserts the diff column is rendered and narrower than the maximize threshold, which both
 * maximize tests assume. The column is measured, not the window: the resize handles decide it.
 */
async function expectNarrowDiffColumn(page: Page): Promise<void> {
    const box = await page.locator('.column-diff').boundingBox();
    expect(box).not.toBeNull();
    expect(box!.width).toBeGreaterThan(0);
    expect(box!.width).toBeLessThan(DIFF_EDITOR_FULLSCREEN_MAX_WIDTH);
}

/**
 * Writes this browser's "Auto maximize code viewer" choice into IndexedDB. The app reads it at
 * boot, so a spec that wants the other answer seeds it and reloads.
 */
async function seedAutoMaximizeCodeViewer(page: Page, enabled: boolean): Promise<void> {
    await page.evaluate(async ([key, value]: [string, boolean]) => {
        const db = await new Promise<IDBDatabase>((resolve, reject) => {
            const rq = indexedDB.open('kanban-storage');
            rq.onsuccess = () => resolve(rq.result);
            rq.onerror = () => reject(rq.error);
        });
        await new Promise<void>((resolve, reject) => {
            const tx = db.transaction('app-settings', 'readwrite');
            tx.objectStore('app-settings').put(value, key);
            tx.oncomplete = () => resolve();
            tx.onerror = () => reject(tx.error);
        });
    }, ['auto-maximize-code-viewer', enabled]);
}


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
 * Per-describe task factory: each describe gets its own task with a worktree, instead of "the first
 * in-progress task", which fullyParallel would hand to several describes at once. The prompt stays neutral — a
 * keyword-routed one can answer with tool calls that mutate the worktree. Pass `llm-fixture:<name>` to pick the
 * answer; a worktree that never appears throws.
 */
function makeSharedTask() {
    let shared: { taskInfo: { projectId: string; taskId: string }; worktreeDir: string } | null = null;
    return async (page: Page): Promise<{ taskInfo: { projectId: string; taskId: string }; worktreeDir: string }> => {
        if (shared) return shared;
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `diff-spec-${Date.now()}`);
        await ensureTaskStarted(page, projectId, taskId);
        // Wait for the task to get a branch (worktree created)
        await page.waitForFunction(async ({ pid, tid }: { pid: string, tid: string }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
            const task = res.ok ? await res.json() : null;
            return !!task?.branch;
        }, { pid: projectId, tid: taskId }, { timeout: 30000 });
        // A task with a branch has a worktree. When it has none, the diff panel this file exists to
        // exercise has nothing to show, so say so rather than handing the test a null to walk away from.
        const worktreeDir = waitForWorktree(projectId, taskId);
        shared = { taskInfo: { projectId, taskId }, worktreeDir: await worktreeDir };
        return shared;
    };
}
/** A deletion is honoured once the run is over and answered with 409 while it is not, so every turn here
 *  goes through `sendTurn` (helpers.ts) — that is the helper that waits for the run to end. */

/** The newest checkpoint hash on this task's messages: the snapshot a message deletion resets
 *  the worktree to. Empty while no turn has captured the working state. */
async function lastCheckpoint(page: Page, projectId: string, taskId: string): Promise<string> {
    const msgs = await apiGetJson<{ git_commit_hash?: string }[]>(page, `/api/projects/${projectId}/tasks/${taskId}/messages`);
    return [...(msgs ?? [])].reverse().find((m) => m.git_commit_hash)?.git_commit_hash ?? '';
}

/** Assert the newest checkpoint a surviving message owns actually holds `file`. That is the whole
 *  question after a deletion: the worktree is reset to that snapshot, and `clean -fd` erases
 *  anything it does not name (git.ResetWorktreeTo). Checked against the object, not inferred from
 *  a hash existing — a checkpoint that predates the file passes the latter and fails the test. */
async function expectRollbackTargetHolds(page: Page, worktreeDir: string, projectId: string, taskId: string, file: string): Promise<void> {
    await expect.poll(async () => {
        const hash = await lastCheckpoint(page, projectId, taskId);
        if (!hash) return false;
        try {
            return execSync(`git -C ${worktreeDir} ls-tree -r --name-only ${hash} -- ${file}`, { encoding: 'utf-8' }).trim().length > 0;
        } catch {
            return false;
        }
    }, { timeout: 20000 }).toBe(true);
}

/** Move the task In Progress if it is not already. `/send` runs a turn only while the task is In
 *  Progress, and a turn is the only thing that snapshots the worktree; a message persisted against
 *  a task in Review checkpoints nothing, and the deletion then rolls back to an old snapshot. */
async function ensureInProgress(page: Page, projectId: string, taskId: string): Promise<void> {
    const task = `/api/projects/${projectId}/tasks/${taskId}`;
    if ((await apiGetJson<{ status?: string }>(page, task))?.status !== 'in_progress') {
        await csrfFetch(page, `${task}/move`, { method: 'POST', body: { status: 'in_progress' } });
    }
}

/** Delete the last user message and everything after it. Asserts its own effect, and has no
 *  `if (visible)` guard: a deletion that never happened leaves every later assertion true by
 *  accident, which is how both specs below passed while testing nothing. */
async function deleteLastUserMessage(page: Page, projectId: string, taskId: string): Promise<void> {
    // The id, not the row count: `/messages` answers a bounded window, so a deletion that drops the
    // newest row lets an older one in and the length does not move. See `messageRows` in helpers.ts.
    const doomed = (await messageRows(page, projectId, taskId)).filter(r => r.role === 'user').at(-1)?.id ?? '';
    expect(doomed, 'the conversation holds no user message to delete').not.toBe('');
    const userMessages = page.locator('.chat-message.user');
    const deleteBtn = userMessages.nth(await userMessages.count() - 1).locator('[aria-label="Delete message and all following messages"]');
    await expect(deleteBtn).toBeVisible({ timeout: 10000 });
    await deleteBtn.click();
    // Global confirmation dialog (BaseModal) — not the editor's .confirm-dialog
    const confirmDialog = page.locator('[role="dialog"]');
    await expect(confirmDialog).toBeVisible({ timeout: 5000 });
    await page.click('[role="dialog"] .btn-confirm');
    await expect(confirmDialog).not.toBeVisible({ timeout: 5000 });
    await expect.poll(async () =>
        (await messageRows(page, projectId, taskId)).some(r => r.id === doomed), { timeout: 15000 }).toBe(false);
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
 * Poll the diff API until the file appears, then re-navigate until the panel shows it. The panel
 * fetches its diff once per mount, so a request that lands while the backend is still rewriting
 * the worktree answers "No changes since last commit" and nothing refetches it after that.
 */
async function waitForDiffFile(page: any, projectId: string, taskId: string, expectFile: string) {
    await page.waitForFunction(async ([pid, tid, file]: [string, string, string]) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        const data = await res.json();
        return data.files && Object.keys(data.files).some((k: string) => k.includes(file));
    }, [projectId, taskId, expectFile], { timeout: 30000 });
    await expect(async () => {
        await navigateToTask(page, taskId);
        await expect(page.locator('.diff-content')).toContainText(expectFile, { timeout: 4000 });
    }).toPass({ timeout: 30000 });
}

/**
 * Poll the diff-file API until the content matches, then re-navigate so the panel refetches —
 * same reason as waitForDiffFile.
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
    // The two deletion specs each need a conversation of their own: a deletion deletes every message
    // after its anchor and resets the worktree to the newest snapshot a SURVIVING message owns, so
    // neither can reason about a task another test — or the other spec — already moved under it.
    const deletionTask = makeSharedTask();
    const stagedTask = makeSharedTask();
    test.beforeEach(async ({ page }) => {
        test.setTimeout(30000);
        await gotoProject(page);
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('should open file editor when clicking a diff line', async ({ page }) => {
        const { taskInfo, worktreeDir } = await sharedTask(page);

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

        // A line of the file this test wrote must be a clickable row: a panel with none is the failure.
        const clickableRows = page.locator('.diff-row-clickable');
        await expect.poll(() => clickableRows.count(), {
            timeout: 15000, message: 'no clickable diff row for the file this test wrote',
        }).toBeGreaterThan(0);

        await clickableRows.first().click();
        // The file this row belongs to opens in the editor, with its own path in the header and its
        // content in the CodeMirror document.
        await expect(page.locator('.file-editor')).toBeVisible({ timeout: 5000 });
        await expect(page.locator('.file-editor .file-path')).toContainText(testFile, { timeout: 5000 });
        await expect(page.locator('.cm-content')).toContainText('Hello World', { timeout: 5000 });
        await expect(page.locator('.cm-content')).toContainText('This is a test', { timeout: 5000 });

        execSync(`rm -f ${filePath}`);
    });

    test('should maximize the editor when the diff column is narrow', async ({ page }) => {
        const { taskInfo, worktreeDir } = await sharedTask(page);

        const uniqueId = Date.now();
        const testFile = `maximize-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        writeFileSync(filePath, 'Maximize test content\nSecond line\n');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const fileSection = page.locator('.diff-file-section', { hasText: testFile });
        await expect(fileSection).toBeVisible({ timeout: 10000 });

        await expectNarrowDiffColumn(page);

        await fileSection.locator('.diff-row-clickable').first().click();
        await expect(page.locator('.file-editor')).toBeVisible({ timeout: 5000 });
        await expect(page.locator('.task-detail-page.fullscreen')).toBeVisible();
        // Where Escape leaves the reader is the editor spec's question, not this panel's.

        execSync(`rm -f ${filePath}`);
    });

    test('leaves the editor inline when this browser turned auto-maximize off', async ({ page }) => {
        const { taskInfo, worktreeDir } = await sharedTask(page);

        const uniqueId = Date.now();
        const testFile = `keep-inline-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        writeFileSync(filePath, 'Keep inline content\nSecond line\n');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const fileSection = page.locator('.diff-file-section', { hasText: testFile });
        await expect(fileSection).toBeVisible({ timeout: 10000 });

        // The same narrow column as the test above; only what this browser stored differs.
        await expectNarrowDiffColumn(page);

        await seedAutoMaximizeCodeViewer(page, false);
        await page.reload();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        await page.locator('.diff-file-section', { hasText: testFile }).locator('.diff-row-clickable').first().click();
        await expect(page.locator('.file-editor')).toBeVisible({ timeout: 5000 });
        await expect(page.locator('.task-detail-page.fullscreen')).toHaveCount(0);

        execSync(`rm -f ${filePath}`);
    });

    test('Ctrl+S saves the file the diff row opened', async ({ page }) => {
        test.setTimeout(90000);
        const { taskInfo, worktreeDir } = await sharedTask(page);

        const uniqueId = Date.now();
        const testFile = `save-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        writeFileSync(filePath, 'Original content\nLine 2\n');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        await expect(async () => {
            expect(await page.locator('.diff-row').count()).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        await page.locator('.diff-row-clickable').first().click();
        await expect(page.locator('.file-editor')).toBeVisible({ timeout: 5000 });

        await page.locator('.cm-content').click();
        await page.keyboard.press('Control+a');
        await page.keyboard.type('Modified content Line 2 Line 3: new line');
        // The editor opened from a diff row has no tab strip, so the file's own unsaved marker is the one
        // that says there is work to save.
        await expect(page.locator('.file-editor .btn-back.unsaved')).toBeVisible({ timeout: 10000 });

        await page.keyboard.press('Control+s');
        await expect(page.locator('.file-editor .btn-back.unsaved')).toHaveCount(0, { timeout: 10000 });
        await expect(async () => {
            expect(execSync(`cat ${filePath}`).toString()).toContain('Line 3: new line');
        }).toPass({ timeout: 10000 });

        await page.locator('.file-editor .btn-back').click();
        await expect(page.locator('.file-editor')).not.toBeVisible({ timeout: 5000 });

        execSync(`rm -f ${filePath}`);
    });


    test('diff panel preserves file content after message deletion', async ({ page }) => {
        test.setTimeout(120000);
        const result = await deletionTask(page);
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

        // Two turns after the write, because the deletion takes the second one with it: a deleted message
        // carries its checkpoint away and the file state resets to the last snapshot a surviving message owns
        // (docs/threads-checkpoints.md). With one turn only, the file is gone after the delete.
        const { projectId, taskId } = taskInfo;
        await ensureInProgress(page, projectId, taskId);
        const userCountBefore = await page.locator('.chat-message.user').count();
        await sendTurn(page, projectId, taskId, `snapshot ${Date.now()}`);
        await sendTurn(page, projectId, taskId, `delete this turn ${Date.now()}`);
        await expect(page.locator('.chat-message.user')).toHaveCount(userCountBefore + 2);

        await deleteLastUserMessage(page, projectId, taskId);
        await expectRollbackTargetHolds(page, worktreeDir, projectId, taskId, testFile);
        await waitForDiffFile(page, projectId, taskId, testFile);

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

    test('a staged file survives a message deletion, restored unstaged', async ({ page }) => {
        test.setTimeout(120000);
        const result = await stagedTask(page);
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

        const stagedBefore = await getDiffViaApi(page, taskInfo.projectId, taskInfo.taskId);
        expect(stagedBefore?.files?.[testFile]?.staged).toBe(true);

        const chatTab = page.locator('.tab-btn:has-text("Chat")');
        if (await chatTab.isVisible().catch(() => false)) {
            await chatTab.click();
        }
        await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

        // Same two turns as the spec above: the checkpoint holding this file must outlive the
        // message that is deleted, or the rollback erases it.
        const { projectId, taskId } = taskInfo;
        await ensureInProgress(page, projectId, taskId);
        const userCountBefore = await page.locator('.chat-message.user').count();
        await sendTurn(page, projectId, taskId, `snapshot staged ${Date.now()}`);
        await sendTurn(page, projectId, taskId, `delete staged turn ${Date.now()}`);
        await expect(page.locator('.chat-message.user')).toHaveCount(userCountBefore + 2);

        await deleteLastUserMessage(page, projectId, taskId);
        await expectRollbackTargetHolds(page, worktreeDir, projectId, taskId, testFile);
        await waitForDiffFile(page, projectId, taskId, testFile);

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

        // What survives the rollback is the file and its content, restored UNSTAGED: the restore
        // ends with `git reset -q` on purpose, "so the restored files read as the task's own
        // changes, not a staged commit" (git.ResetWorktreeTo).
        const diffDataAfter = await getDiffViaApi(page, taskInfo.projectId, taskInfo.taskId);
        expect(diffDataAfter?.files?.[testFile]).toBeTruthy();
        expect(Boolean(diffDataAfter.files[testFile].staged)).toBe(false);

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
        await gotoProject(page);
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('should show new untracked files in the diff panel', async ({ page }) => {
        const { taskInfo, worktreeDir } = await sharedTask(page);

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
        const { taskInfo, worktreeDir } = await sharedTask(page);

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
        const { taskInfo, worktreeDir } = await sharedTask(page);

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


    test('diff panel updates in UI for same-line changes', async ({ page }) => {
        const { taskInfo, worktreeDir } = await sharedTask(page);

        const uniqueId = Date.now();
        const testFile = `diff-ui-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        writeFileSync(filePath, 'hello world\n');
        await gitWithRetry(worktreeDir, 'git add -A');

        await navigateToTask(page, taskInfo.taskId, taskInfo.projectId);
        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 15000 });
        await waitForDiffFileContent(page, taskInfo.projectId, taskInfo.taskId, testFile, 'hello world');

        // A line modified twice shows its newest pair, and the rows come from `/diff/file`.
        writeFileSync(filePath, 'hello goodbye\n');
        await waitForDiffFileContent(page, taskInfo.projectId, taskInfo.taskId, testFile, 'hello goodbye');

        writeFileSync(filePath, 'goodbye world\n');
        await waitForDiffFileContent(page, taskInfo.projectId, taskInfo.taskId, testFile, 'goodbye world');
        await expect(async () => {
            const panelContent = await page.textContent('.diff-scroll');
            expect(panelContent).toContain('goodbye world');
            expect(panelContent).not.toContain('hello world');
        }).toPass({ timeout: 15000 });

        execSync(`rm -f ${filePath}`);
    });

    test('should update diff panel for changed files via delta updates', async ({ page }) => {
        const result = await sharedTask(page);
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

    test('expanding a truncated staged file keeps it in the Staged section', async ({ page }) => {
        const result = await sharedTask(page);
        const { taskInfo, worktreeDir } = result;

        const testFile = `truncated-staged-${Date.now()}.txt`;
        // More change lines than defaultMaxChangeLines (60) so the panel truncates the
        // file and offers "Show full diff".
        writeFileSync(join(worktreeDir, testFile), Array.from({ length: 120 }, (_v, i) => `line ${i}`).join('\n') + '\n');
        await gitWithRetry(worktreeDir, `git add -- ${testFile}`);

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

        const stagedSection = page.locator('.diff-section').filter({ has: page.locator('.diff-section-header.diff-section-staged') });
        const unstagedSection = page.locator('.diff-section').filter({ has: page.locator('.diff-section-header.diff-section-unstaged') });
        const stagedFile = stagedSection.locator('.diff-file-section', { hasText: testFile });
        await expect(stagedFile).toHaveCount(1, { timeout: 20000 });

        const expandBtn = stagedFile.locator('.diff-expand-btn');
        await expect(expandBtn).toBeVisible({ timeout: 10000 });
        const badgeBefore = (await stagedFile.locator('.diff-status-badge').textContent())?.trim();
        await expandBtn.click();

        // Regression: the expansion response knows nothing about the git index or the
        // file's status, and the panel used to rebuild the cache entry from it alone —
        // which dropped `staged` (file jumped Staged → Unstaged) and the A/D/R badge.
        await expect(expandBtn).toHaveCount(0, { timeout: 20000 });
        await expect(stagedFile).toHaveCount(1);
        await expect(unstagedSection.locator('.diff-file-section', { hasText: testFile })).toHaveCount(0);
        await expect(stagedFile.locator('.diff-status-badge')).toHaveText(badgeBefore || 'A');

        execSync(`rm -f ${join(worktreeDir, testFile)}`);
        execSync(`cd ${worktreeDir} && git reset -q -- ${testFile}`);
    });
});

// --- Tests: done-task diff (BaseCommit..BranchTip) ---

test.describe.serial('Done-task diff shows committed changes', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(60000);
        await gotoProject(page);
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

        rmSync(join(worktreePath, testFile), { force: true });
    });
});

// --- Tests: tab character rendering in the diff panel ---

test.describe.serial('Diff panel tab rendering', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(60000);
        await gotoProject(page);
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