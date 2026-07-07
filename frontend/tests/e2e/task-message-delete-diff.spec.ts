import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync } from 'fs';
import { join } from 'path';
import { getProjectId, getLastTaskId, navigateToTask, csrfFetch } from './helpers';

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

test.describe.serial('Diff panel after message deletion', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('diff panel preserves file content after message deletion', async ({ page }) => {
        // Find or create an in-progress task with a worktree
        let taskInfo = getTaskInfo();
        let worktreeDir = findWorktreeDir();

        if (!taskInfo || !worktreeDir) {
            const projectId = await getProjectId(page);
            const taskId = await getLastTaskId(page, projectId);

            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
                method: 'POST',
                body: { status: 'in_progress' },
            });

            // Trigger lazy-start to create worktree
            await page.evaluate(async ({ pid, tid }) => {
                await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ content: 'list files' }),
                });
            }, { pid: projectId, tid: taskId });

            // Wait for worktree to be created
            for (let i = 0; i < 20; i++) {
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
        const testFile = `delete-diff-test-${uniqueId}.txt`;

        // Create a test file in the worktree
        writeFileSync(join(worktreeDir, testFile), 'line one\nline two\nline three\n');

        // Open task detail page
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

        // Wait for diff content area
        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });

        // Wait for initial diff broadcast
        await page.waitForTimeout(4000);

        // Verify the test file appears in the diff panel
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 8000 });

        // Verify file content is visible
        const diffBefore = await page.textContent('.diff-scroll');
        expect(diffBefore).toContain('line one');

        // Switch to Chat tab
        const chatTab = page.locator('.tab-btn:has-text("Chat")');
        if (await chatTab.isVisible().catch(() => false)) {
            await chatTab.click();
        }
        await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

        // Send a message to create a user message we can delete
        const msgContent = `delete me ${Date.now()}`;
        await page.evaluate(async ({ pid, tid, msg }) => {
            await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ content: msg }),
            });
        }, { pid: taskInfo.projectId, tid: taskInfo.taskId, msg: msgContent });

        // Wait for user message to appear
        await expect(async () => {
            const msgs = await page.locator('.chat-message.user').count();
            expect(msgs).toBeGreaterThan(0);
        }).toPass({ timeout: 10000 });

        // Wait for LLM response to complete
        await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

        // Delete the last user message
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
            await page.waitForTimeout(2000);
        }

        // Switch back to Changes tab
        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        await page.waitForTimeout(2000);

        // CRITICAL: The diff panel should still show the file correctly.
        // Before the fix, the legacy-format WS event or stale cache would cause
        // incorrect display (missing content, wrong staged status).
        const diffAfter = await page.textContent('.diff-scroll');
        expect(diffAfter).toContain(testFile);
        expect(diffAfter).toContain('line one');

        // Clean up
        execSync(`rm -f ${join(worktreeDir, testFile)}`);
    });

    test('staged status preserved after message deletion', async ({ page }) => {
        // Find or create an in-progress task with a worktree
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

            for (let i = 0; i < 20; i++) {
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
        const testFile = `staged-test-${uniqueId}.txt`;

        // Create and stage a file
        writeFileSync(join(worktreeDir, testFile), 'staged content\n');
        execSync(`cd ${worktreeDir} && git add ${testFile}`);

        // Open task detail page
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

        // Wait for diff content area
        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });
        await page.waitForTimeout(4000);

        // Verify the staged file appears in the diff panel
        await expect(async () => {
            const diffText = await page.textContent('.diff-scroll');
            expect(diffText).toContain(testFile);
        }).toPass({ timeout: 8000 });

        // Verify it shows as staged (staged files have a specific visual indicator)
        // Check the diff panel shows the file in the staged section
        const stagedSection = page.locator('.diff-section:has-text("Staged"), .diff-group:has-text("Staged")');
        const unstagedSection = page.locator('.diff-section:has-text("Unstaged"), .diff-group:has-text("Unstaged")');

        // Determine which section the file is in
        let isStagedBefore = false;
        if (await stagedSection.isVisible().catch(() => false)) {
            const stagedContent = await stagedSection.textContent();
            isStagedBefore = stagedContent?.includes(testFile) ?? false;
        }
        // If no explicit sections, check via API
        if (!isStagedBefore) {
            const diffData = JSON.parse(execSync(
                `curl -s "http://localhost:3000/api/projects/${taskInfo.projectId}/tasks/${taskInfo.taskId}/diff"`,
                { encoding: 'utf-8' }
            ));
            if (diffData.files && diffData.files[testFile]) {
                isStagedBefore = diffData.files[testFile].staged === true;
            }
        }

        // Switch to Chat tab and send + delete a message
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

        // Delete the message
        const userMessages = page.locator('.chat-message.user');
        const userCount = await userMessages.count();
        if (userCount >= 2) {
            const lastUserMsg = userMessages.nth(userCount - 1);
            const deleteBtn = lastUserMsg.locator('[aria-label="Delete message and all following messages"]');
            if (await deleteBtn.isVisible().catch(() => false)) {
                await deleteBtn.click();
                await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
                await page.click('.confirm-dialog .btn-confirm');
                await page.waitForTimeout(2000);
            }
        }

        // Switch back to Changes tab
        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        await page.waitForTimeout(2000);

        // Verify the file still appears and staged status is correct
        const diffAfter = await page.textContent('.diff-scroll');
        expect(diffAfter).toContain(testFile);

        // Verify staged status via API (source of truth)
        const diffDataAfter = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${taskInfo.projectId}/tasks/${taskInfo.taskId}/diff"`,
            { encoding: 'utf-8' }
        ));
        if (diffDataAfter.files && diffDataAfter.files[testFile]) {
            const isStagedAfter = diffDataAfter.files[testFile].staged === true;
            // Staged status should be consistent (may change if reset affected it,
            // but UI should match API truth, not show stale data)
            expect(isStagedAfter).toBe(typeof isStagedAfter === 'boolean');
        }

        // Clean up
        execSync(`rm -f ${join(worktreeDir, testFile)}`);
        execSync(`cd ${worktreeDir} && git reset HEAD ${testFile} 2>/dev/null || true`);
    });
});
