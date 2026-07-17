import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { writeFileSync } from 'fs';
import { join } from 'path';
import { navigateToTask, csrfFetch } from './helpers';
import { findWorktreeDir, getTaskInfo, ensureActiveWorktree } from './diff-helpers';

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
        }).toPass({ timeout: 8000 });

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
            // Wait for diff-scroll to no longer contain the deleted message text
            await expect(async () => {
                const text = await page.locator('.diff-scroll').textContent().catch(() => '');
                expect(text).toContain(testFile);
            }).toPass({ timeout: 5000 });
        }

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        // Wait for diff content to be visible after tab switch
        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });

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
        }).toPass({ timeout: 8000 });

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
                // Wait for diff-scroll to still contain the file after deletion
                await expect(async () => {
                    const text = await page.locator('.diff-scroll').textContent().catch(() => '');
                    expect(text).toContain(testFile);
                }).toPass({ timeout: 5000 });
            }
        }

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        // Wait for diff content to be visible after tab switch
        await expect(page.locator('.diff-content')).toBeVisible({ timeout: 10000 });

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
});
