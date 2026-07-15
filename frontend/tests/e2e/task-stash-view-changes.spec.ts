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

function getTaskInfo(): { projectId: string; taskId: string; worktreeDir: string } | null {
    try {
        const projects = JSON.parse(execSync('curl -s http://localhost:3000/api/projects', { encoding: 'utf-8' }));
        if (!projects.length) return null;
        const projectId = projects[0].id;
        const tasks = JSON.parse(execSync(
            `curl -s http://localhost:3000/api/projects/${projectId}/tasks`, { encoding: 'utf-8' }
        ));
        const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
        if (!task) return null;
        const wtDir = join(getConfig().workspace_dir, projectId, task.id);
        try { statSync(wtDir); } catch { return null; }
        return { projectId, taskId: task.id, worktreeDir: wtDir };
    } catch {
        return null;
    }
}

test.describe.serial('Stash View Changes integration', () => {
    test('View Changes API resolves previous stash correctly', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const info = getTaskInfo();
        if (!info) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const { projectId, taskId, worktreeDir } = info;
        const uid = Date.now();

        // Simulate: user commit → checkpoint 1 → checkpoint 2
        execSync(`echo "user-work-${uid}" > ${worktreeDir}/user-file.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git commit -m "user work ${uid}"`);

        execSync(`echo "check1-${uid}" > ${worktreeDir}/check1.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "checkpoint 1"`);
        const stash1 = execSync(`cd ${worktreeDir} && git rev-parse stash@{1}`, { encoding: 'utf-8' }).trim();

        execSync(`echo "check2-${uid}" > ${worktreeDir}/check2.txt`);
        execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "checkpoint 2"`);
        const stash2 = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

        await page.waitForTimeout(300);

        // API: View Changes for checkpoint 1 (oldest) → should show user-work only
        const diff1 = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff?head=${stash1}"`,
            { encoding: 'utf-8' }
        ));
        console.log('Checkpoint 1 diff files:', diff1.diff.split('\n').filter(l => l.startsWith('+++') || l.startsWith('---')).join(', '));
        expect(diff1.diff).toContain('user-file');
        expect(diff1.diff).not.toContain('check1');
        expect(diff1.diff).not.toContain('check2');

        // API: View Changes for checkpoint 2 (newest) → should show user-work + check1
        const diff2 = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff?head=${stash2}"`,
            { encoding: 'utf-8' }
        ));
        console.log('Checkpoint 2 diff files:', diff2.diff.split('\n').filter(l => l.startsWith('+++') || l.startsWith('---')).join(', '));
        expect(diff2.diff).toContain('user-file');
        expect(diff2.diff).toContain('check1');
        expect(diff2.diff).not.toContain('check2');

        // Clean up
        execSync(`cd ${worktreeDir} && git stash clear`);
        const baseRef = execSync(`cd ${worktreeDir} && git rev-parse HEAD~1`, { encoding: 'utf-8' }).trim();
        execSync(`cd ${worktreeDir} && git reset --hard ${baseRef}`);
        execSync(`cd ${worktreeDir} && git clean -fd`);
    });
});
