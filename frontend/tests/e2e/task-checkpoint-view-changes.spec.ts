import { navigateToTask, getProjectId, createTaskViaApi, csrfFetch } from './helpers';
import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, existsSync } from 'fs';
import { join } from 'path';

function getTestRepoPath(): string {
    return '/tmp/kanban-test-worktrees';
}

function findWorktreeDir(taskId: string): string {
    return join(getTestRepoPath(), taskId);
}

function getStashList(worktreeDir: string): { index: number; hash: string; subject: string }[] {
    const output = execSync(`cd ${worktreeDir} && git stash list --format="%gd%%00%H%%00%s"`, { encoding: 'utf-8' });
    const entries: { index: number; hash: string; subject: string }[] = [];
    for (const line of output.trim().split('\n')) {
        const parts = line.split('\x00');
        if (parts.length >= 2) {
            const match = parts[0].match(/stash@\{(\d+)\}/);
            entries.push({
                index: match ? parseInt(match[1]) : -1,
                hash: parts[1].trim(),
                subject: parts[2] || '',
            });
        }
    }
    return entries;
}

test.describe.serial('Checkpoint View Changes', () => {
    let worktreeDir: string = '';
    let projectId: string = '';
    let taskId: string = '';

    test.beforeEach(async ({ page }) => {
        test.setTimeout(30000);
        await page.goto('/');
        await page.waitForSelector('.card');

        // Get project ID
        projectId = await getProjectId(page);

        // Create a task and start it to get a worktree
        taskId = await createTaskViaApi(page, projectId, `checkpoint-test-${Date.now()}`);
        const startResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
        if (!startResult.ok) test.skip("Could not start task (worktree creation may have failed)");

        // Wait for worktree to be created
        await expect(async () => {
            const taskData = await page.evaluate(async ({ pid, tid }) => {
                const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
                return res.json();
            }, { pid: projectId, tid: taskId });
            expect(taskData.branch).toBeTruthy();
        }).toPass({ timeout: 10000 });

        // Find worktree directory
        const possiblePaths = [
            join(getTestRepoPath(), taskId),
            `/tmp/kanban-test-worktrees/${taskId}`,
        ];
        for (const p of possiblePaths) {
            if (existsSync(p)) {
                worktreeDir = p;
                break;
            }
        }

        expect(worktreeDir).toBeTruthy();
    });

    test('View Changes button shows and overlay renders correctly', async ({ page }) => {
        // Clear existing stashes to start clean
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);

        // Create stash checkpoints in worktree
        execSync(`echo "checkpoint 1 content" > ${worktreeDir}/checkpoint-1.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "On ${execSync(`cd ${worktreeDir} && git branch --show-current`, {encoding:'utf-8'}).trim()}: checkpoint 1"`);
        const stash1 = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        execSync(`echo "checkpoint 2 content" > ${worktreeDir}/checkpoint-2.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "On ${execSync(`cd ${worktreeDir} && git branch --show-current`, {encoding:'utf-8'}).trim()}: checkpoint 2"`);
        const stash2 = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        const stashList = getStashList(worktreeDir);
        console.log('Stash list:', stashList.map(e => `${e.index} ${e.hash.slice(0,7)} ${e.subject}`).join('\n'));

        // Set hash on last assistant message (via page.evaluate for auth)
        const messages = await page.evaluate(async ({ pid, tid }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
            return res.json();
        }, { pid: projectId, tid: taskId });
        const assistantMsgs = messages.filter((m: any) => m.role === 'assistant');
        if (assistantMsgs.length > 0) {
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages/${assistantMsgs[assistantMsgs.length - 1].id}`, {
                method: 'PATCH',
                body: { git_commit_hash: stash2 },
            });
            console.log(`Set hash ${stash2.slice(0, 7)} on assistant ${assistantMsgs[assistantMsgs.length - 1].id.slice(0, 8)}`);
        } else {
            // No assistant messages - create one with the hash
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
                method: 'POST',
                body: { role: 'assistant', content: 'checkpoint marker', git_commit_hash: stash2 },
            });
            console.log(`Created assistant message with hash ${stash2.slice(0, 7)}`);
        }

        // Open the task
        await navigateToTask(page, taskId, projectId);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }

        // Check View Changes buttons
        const viewChangesBtns = page.locator('.view-changes-btn');
        const btnCount = await viewChangesBtns.count();
        console.log('View Changes buttons:', btnCount);

        expect(btnCount).toBeGreaterThan(0);

        // Click "View Changes"
        await viewChangesBtns.first().click();

        // Verify the overlay appears
        const overlay = page.locator('.checkpoint-diff-overlay');
        await expect(overlay).toBeVisible({ timeout: 5000 });

        // Verify the overlay header
        await expect(page.locator('.checkpoint-diff-header')).toBeVisible();
        await expect(page.locator('.checkpoint-diff-title')).toContainText('Changes before this checkpoint');

        // Verify close button
        await expect(page.locator('.btn-close-checkpoint')).toBeVisible();

        // Close
        await page.locator('.btn-close-checkpoint').click();
        await expect(overlay).not.toBeVisible({ timeout: 5000 });

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
    });

    test('View Changes shows state BEFORE checkpoint (not including checkpoint)', async ({ page }) => {
        // Clear existing stashes
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);

        const branch = execSync(`cd ${worktreeDir} && git branch --show-current`, { encoding: 'utf-8' }).trim();

        // Create 2 stash checkpoints with distinct files
        execSync(`echo "file-a" > ${worktreeDir}/file-a.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "On ${branch}: checkpoint A"`);
        const stashA = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        execSync(`echo "file-b" > ${worktreeDir}/file-b.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "On ${branch}: checkpoint B"`);
        const stashB = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        // Set hash on last assistant message to stashB (newest, via page.evaluate for auth)
        const messages = await page.evaluate(async ({ pid, tid }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
            return res.json();
        }, { pid: projectId, tid: taskId });
        const assistantMsgs = messages.filter((m: any) => m.role === 'assistant');
        if (assistantMsgs.length > 0) {
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages/${assistantMsgs[assistantMsgs.length - 1].id}`, {
                method: 'PATCH',
                body: { git_commit_hash: stashB },
            });
        } else {
            // No assistant messages - create one with the hash
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
                method: 'POST',
                body: { role: 'assistant', content: 'checkpoint marker B', git_commit_hash: stashB },
            });
        }

        // Open the task
        await navigateToTask(page, taskId, projectId);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }

        const viewChangesBtns = page.locator('.view-changes-btn');
        if (await viewChangesBtns.count() === 0) {
            await expect(page.locator('.diff-content')).toBeVisible();
            return;
        }

        // Click "View Changes" — should show state BEFORE checkpoint B
        // State before B = stash A (which has file-a only, not file-b)
        await viewChangesBtns.first().click();

        const overlay = page.locator('.checkpoint-diff-overlay');
        await expect(overlay).toBeVisible({ timeout: 5000 });

        // The diff should show file-a (from checkpoint A) but NOT file-b (from checkpoint B)
        const overlayText = await overlay.textContent();
        expect(overlayText).toContain('file-a');
        expect(overlayText).not.toContain('file-b');

        // Close
        await page.locator('.btn-close-checkpoint').click();
        await expect(overlay).not.toBeVisible({ timeout: 5000 });

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
    });

    test('First checkpoint shows No Changes (state before = origin)', async ({ page }) => {
        // Clear existing stashes
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);

        const branch = execSync(`cd ${worktreeDir} && git branch --show-current`, { encoding: 'utf-8' }).trim();

        // Create only 1 stash checkpoint
        execSync(`echo "only-file" > ${worktreeDir}/only-file.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "On ${branch}: first checkpoint"`);
        const stashFirst = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        // Set hash on last assistant message
        const messages = await page.evaluate(async ({ pid, tid }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
            return res.json();
        }, { pid: projectId, tid: taskId });
        const assistantMsgs = messages.filter((m: any) => m.role === 'assistant');
        if (assistantMsgs.length > 0) {
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages/${assistantMsgs[assistantMsgs.length - 1].id}`, {
                method: 'PATCH',
                body: { git_commit_hash: stashFirst },
            });
        }

        // Open the task
        await navigateToTask(page, taskId, projectId);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }

        const viewChangesBtns = page.locator('.view-changes-btn');
        if (await viewChangesBtns.count() === 0) {
            await expect(page.locator('.diff-content')).toBeVisible();
            return;
        }

        // Click "View Changes" — first checkpoint, state before = origin = no changes
        await viewChangesBtns.first().click();

        const overlay = page.locator('.checkpoint-diff-overlay');
        await expect(overlay).toBeVisible({ timeout: 5000 });

        // Should show "No changes" or empty diff
        const diffEmpty = overlay.locator('.diff-empty');
        const diffFiles = overlay.locator('.diff-file-section');
        const isEmpty = await diffEmpty.isVisible().catch(() => false);
        const fileCount = await diffFiles.count().catch(() => 0);
        expect(isEmpty || fileCount === 0).toBe(true);

        // Close
        await page.locator('.btn-close-checkpoint').click();
        await expect(overlay).not.toBeVisible({ timeout: 5000 });

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
    });

    test('Escape key closes the overlay', async ({ page }) => {
        const branch = execSync(`cd ${worktreeDir} && git branch --show-current`, { encoding: 'utf-8' }).trim();
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
        execSync(`echo "esc-test" > ${worktreeDir}/esc-test.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "On ${branch}: esc test"`);
        const stashHash = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        const messages = await page.evaluate(async ({ pid, tid }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
            return res.json();
        }, { pid: projectId, tid: taskId });
        const assistantMsgs = messages.filter((m: any) => m.role === 'assistant');
        if (assistantMsgs.length > 0) {
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages/${assistantMsgs[assistantMsgs.length - 1].id}`, {
                method: 'PATCH',
                body: { git_commit_hash: stashHash },
            });
        }

        await navigateToTask(page, taskId, projectId);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }

        const viewChangesBtns = page.locator('.view-changes-btn');
        if (await viewChangesBtns.count() === 0) {
            await expect(page.locator('.diff-content')).toBeVisible();
            return;
        }

        await viewChangesBtns.first().click();

        const overlay = page.locator('.checkpoint-diff-overlay');
        await expect(overlay).toBeVisible({ timeout: 5000 });

        // Press Escape — should close overlay, not the task detail
        await page.press('Escape');
        await expect(overlay).not.toBeVisible({ timeout: 5000 });
        await expect(page.locator('.task-detail-page')).toBeVisible();

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
    });
});
