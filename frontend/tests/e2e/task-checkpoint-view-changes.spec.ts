import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync } from 'fs';
import { join } from 'path';

function getTestRepoPath(): string {
    const config = JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
    return config.workspace_dir;
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
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const projects = JSON.parse(execSync('curl -s http://localhost:3000/api/projects', { encoding: 'utf-8' }));
        projectId = projects[0].id;

        const tasks = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${projectId}/tasks"`,
            { encoding: 'utf-8' }
        ));
        const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
        if (task) {
            taskId = task.id;
            worktreeDir = findWorktreeDir(taskId);
        }
    });

    test('View Changes button shows and overlay renders correctly', async ({ page }) => {
        if (!worktreeDir || !taskId) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

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

        // Set hash on last assistant message
        const messages = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages"`,
            { encoding: 'utf-8' }
        ));
        const assistantMsgs = messages.filter((m: any) => m.role === 'assistant');
        if (assistantMsgs.length > 0) {
            execSync(
                `curl -s -X PATCH "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages/${assistantMsgs[assistantMsgs.length - 1].id}" -H "Content-Type: application/json" -d '{"git_commit_hash":"${stash2}"}'`,
                { encoding: 'utf-8' }
            );
            console.log(`Set hash ${stash2.slice(0, 7)} on assistant ${assistantMsgs[assistantMsgs.length - 1].id.slice(0, 8)}`);
        }

        await page.waitForTimeout(500);

        // Open the task
        await page.goto(`/?project=${projectId}&task=${taskId}`);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        await page.waitForTimeout(1000);

        // Check View Changes buttons
        const viewChangesBtns = page.locator('.view-changes-btn');
        const btnCount = await viewChangesBtns.count();
        console.log('View Changes buttons:', btnCount);

        expect(btnCount).toBeGreaterThan(0);

        // Click "View Changes"
        await viewChangesBtns.first().click();
        await page.waitForTimeout(1500);

        // Verify the overlay appears
        const overlay = page.locator('.checkpoint-diff-overlay');
        await expect(overlay).toBeVisible();

        // Verify the overlay header
        await expect(page.locator('.checkpoint-diff-header')).toBeVisible();
        await expect(page.locator('.checkpoint-diff-title')).toContainText('Changes before this checkpoint');

        // Verify close button
        await expect(page.locator('.btn-close-checkpoint')).toBeVisible();

        // Close
        await page.locator('.btn-close-checkpoint').click();
        await page.waitForTimeout(500);
        await expect(overlay).not.toBeVisible();

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
    });

    test('View Changes shows state BEFORE checkpoint (not including checkpoint)', async ({ page }) => {
        if (!worktreeDir || !taskId) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

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

        // Set hash on last assistant message to stashB (newest)
        const messages = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages"`,
            { encoding: 'utf-8' }
        ));
        const assistantMsgs = messages.filter((m: any) => m.role === 'assistant');
        if (assistantMsgs.length > 0) {
            execSync(
                `curl -s -X PATCH "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages/${assistantMsgs[assistantMsgs.length - 1].id}" -H "Content-Type: application/json" -d '{"git_commit_hash":"${stashB}"}'`,
                { encoding: 'utf-8' }
            );
        }

        await page.waitForTimeout(500);

        // Open the task
        await page.goto(`/?project=${projectId}&task=${taskId}`);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        await page.waitForTimeout(1000);

        const viewChangesBtns = page.locator('.view-changes-btn');
        if (await viewChangesBtns.count() === 0) {
            await expect(page.locator('.diff-content')).toBeVisible();
            return;
        }

        // Click "View Changes" — should show state BEFORE checkpoint B
        // State before B = stash A (which has file-a only, not file-b)
        await viewChangesBtns.first().click();
        await page.waitForTimeout(1500);

        const overlay = page.locator('.checkpoint-diff-overlay');
        await expect(overlay).toBeVisible();

        // The diff should show file-a (from checkpoint A) but NOT file-b (from checkpoint B)
        const overlayText = await overlay.textContent();
        expect(overlayText).toContain('file-a');
        expect(overlayText).not.toContain('file-b');

        // Close
        await page.locator('.btn-close-checkpoint').click();
        await page.waitForTimeout(500);

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
    });

    test('First checkpoint shows No Changes (state before = origin)', async ({ page }) => {
        if (!worktreeDir || !taskId) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        // Clear existing stashes
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);

        const branch = execSync(`cd ${worktreeDir} && git branch --show-current`, { encoding: 'utf-8' }).trim();

        // Create only 1 stash checkpoint
        execSync(`echo "only-file" > ${worktreeDir}/only-file.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "On ${branch}: first checkpoint"`);
        const stashFirst = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        // Set hash on last assistant message
        const messages = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages"`,
            { encoding: 'utf-8' }
        ));
        const assistantMsgs = messages.filter((m: any) => m.role === 'assistant');
        if (assistantMsgs.length > 0) {
            execSync(
                `curl -s -X PATCH "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages/${assistantMsgs[assistantMsgs.length - 1].id}" -H "Content-Type: application/json" -d '{"git_commit_hash":"${stashFirst}"}'`,
                { encoding: 'utf-8' }
            );
        }

        await page.waitForTimeout(500);

        // Open the task
        await page.goto(`/?project=${projectId}&task=${taskId}`);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        await page.waitForTimeout(1000);

        const viewChangesBtns = page.locator('.view-changes-btn');
        if (await viewChangesBtns.count() === 0) {
            await expect(page.locator('.diff-content')).toBeVisible();
            return;
        }

        // Click "View Changes" — first checkpoint, state before = origin = no changes
        await viewChangesBtns.first().click();
        await page.waitForTimeout(1500);

        const overlay = page.locator('.checkpoint-diff-overlay');
        await expect(overlay).toBeVisible();

        // Should show "No changes" or empty diff
        const diffEmpty = overlay.locator('.diff-empty');
        const diffFiles = overlay.locator('.diff-file-section');
        const isEmpty = await diffEmpty.isVisible().catch(() => false);
        const fileCount = await diffFiles.count().catch(() => 0);
        expect(isEmpty || fileCount === 0).toBe(true);

        // Close
        await page.locator('.btn-close-checkpoint').click();
        await page.waitForTimeout(500);

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
    });

    test('Escape key closes the overlay', async ({ page }) => {
        if (!worktreeDir || !taskId) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const branch = execSync(`cd ${worktreeDir} && git branch --show-current`, { encoding: 'utf-8' }).trim();
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
        execSync(`echo "esc-test" > ${worktreeDir}/esc-test.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "On ${branch}: esc test"`);
        const stashHash = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        const messages = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages"`,
            { encoding: 'utf-8' }
        ));
        const assistantMsgs = messages.filter((m: any) => m.role === 'assistant');
        if (assistantMsgs.length > 0) {
            execSync(
                `curl -s -X PATCH "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages/${assistantMsgs[assistantMsgs.length - 1].id}" -H "Content-Type: application/json" -d '{"git_commit_hash":"${stashHash}"}'`,
                { encoding: 'utf-8' }
            );
        }

        await page.waitForTimeout(500);

        await page.goto(`/?project=${projectId}&task=${taskId}`);
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        const changesTab = page.locator('.tab-btn:has-text("Changes")');
        if (await changesTab.isVisible().catch(() => false)) {
            await changesTab.click();
        }
        await page.waitForTimeout(1000);

        const viewChangesBtns = page.locator('.view-changes-btn');
        if (await viewChangesBtns.count() === 0) {
            await expect(page.locator('.diff-content')).toBeVisible();
            return;
        }

        await viewChangesBtns.first().click();
        await page.waitForTimeout(1000);

        const overlay = page.locator('.checkpoint-diff-overlay');
        await expect(overlay).toBeVisible();

        // Press Escape — should close overlay, not the task detail
        await page.press('Escape');
        await page.waitForTimeout(500);
        await expect(overlay).not.toBeVisible();
        await expect(page.locator('.task-detail-page')).toBeVisible();

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear 2>/dev/null || true`);
    });
});
