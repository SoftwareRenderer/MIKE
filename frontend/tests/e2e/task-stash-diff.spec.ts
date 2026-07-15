import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync } from 'fs';
import { join } from 'path';

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
        const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
        if (!task) return null;
        return { projectId, taskId: task.id };
    } catch {
        return null;
    }
}

test.describe.serial('Stash-based checkpoint diff', () => {
    test('first checkpoint shows empty diff, second shows first checkpoint changes', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();

        // Create a real commit on the branch
        execSync(`echo "real commit ${uniqueId}" > ${worktreeDir}/real-file.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git commit -m "real commit ${uniqueId}"`);

        // Create checkpoint 1 (stash)
        execSync(`echo "checkpoint1 ${uniqueId}" > ${worktreeDir}/check1-file.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "checkpoint 1"`);
        const stash1Hash = execSync(`cd ${worktreeDir} && git rev-parse stash@{1}`, { encoding: 'utf-8' }).trim();

        // Create checkpoint 2 (stash)
        execSync(`echo "checkpoint2 ${uniqueId}" > ${worktreeDir}/check2-file.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "checkpoint 2"`);
        const stash2Hash = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        await page.waitForTimeout(500);

        // Checkpoint 1 (oldest, stash@{1}): no previous stash → falls back to HEAD
        // HEAD = real commit, diff(origin..HEAD) shows real-file.txt
        const diff1 = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${taskInfo.projectId}/tasks/${taskInfo.taskId}/diff?head=${stash1Hash}"`,
            { encoding: 'utf-8' }
        ));
        expect(diff1.diff).toContain('real-file');
        expect(diff1.diff).not.toContain('check1');

        // Checkpoint 2 (newest, stash@{0}): previous stash is stash@{1}
        // diff(origin..stash@{1}) shows real-file.txt + check1-file.txt
        const diff2 = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${taskInfo.projectId}/tasks/${taskInfo.taskId}/diff?head=${stash2Hash}"`,
            { encoding: 'utf-8' }
        ));
        expect(diff2.diff).toContain('real-file');
        expect(diff2.diff).toContain('check1');
        expect(diff2.diff).not.toContain('check2'); // should NOT include checkpoint2's own changes

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear`);
        const baseRef = execSync(`cd ${worktreeDir} && git rev-parse HEAD~1`, { encoding: 'utf-8' }).trim();
        execSync(`cd ${worktreeDir} && git reset --hard ${baseRef}`);
    });

    test('single checkpoint with no prior commits shows empty diff', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();

        // Create a stash checkpoint WITHOUT any prior commits
        execSync(`echo "only checkpoint ${uniqueId}" > ${worktreeDir}/only-check.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "only checkpoint"`);
        const stashHash = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        // No previous stash, HEAD = origin → diff(origin..origin) = empty
        const diff = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${taskInfo.projectId}/tasks/${taskInfo.taskId}/diff?head=${stashHash}"`,
            { encoding: 'utf-8' }
        ));
        expect(diff.diff.trim()).toBe('');

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear`);
    });
});
