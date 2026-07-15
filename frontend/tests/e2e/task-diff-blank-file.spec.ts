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

function getDiffViaApi(projectId: string, taskId: string): any {
    return JSON.parse(execSync(
        `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff"`,
        { encoding: 'utf-8' }
    ));
}

test.describe.serial('Blank (empty) files appear in diff panel', () => {
    test('unstaged blank file appears in diff panel', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `blank-untracked-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        // Open task detail
        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 5000 });

        // Wait for initial diff to load
        await page.waitForTimeout(3000);

        // Create a blank (empty) untracked file
        execSync(`touch ${filePath}`);

        // Wait for broadcast update (poller runs every 2s)
        await page.waitForTimeout(5000);

        // Verify the blank file appears via API
        const diff = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        // Check manifest format (files map)
        if (diff.files) {
            expect(diff.files[testFile]).toBeDefined();
            expect(diff.files[testFile].type).toBe('untracked');
            expect(diff.files[testFile].line_count).toBe(0);
        } else {
            // Legacy format
            expect(diff.untracked.some((u: any) => u.path === testFile)).toBe(true);
        }

        // Verify it's visible in the UI as an untracked file section
        await expect(page.locator('.diff-file-section-untracked')).toBeVisible({ timeout: 5000 });
        const panelContent = await page.textContent('.diff-content');
        expect(panelContent).toContain(testFile);

        // Cleanup
        execSync(`rm -f ${filePath}`);
        await page.waitForTimeout(3000);
    });

    test('staged blank file appears in diff panel', async ({ page }) => {
        // Navigate to board if not already there
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `blank-staged-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        // Open task detail
        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 5000 });

        // Wait for initial diff to load
        await page.waitForTimeout(3000);

        // Create a blank file and stage it
        execSync(`touch ${filePath}`);
        execSync(`cd ${worktreeDir} && git add ${testFile}`);

        // Wait for broadcast update (poller runs every 2s)
        await page.waitForTimeout(5000);

        // Verify the staged blank file appears via API
        const diff = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        // Check manifest format (files map)
        if (diff.files) {
            expect(diff.files[testFile]).toBeDefined();
            expect(diff.files[testFile].type).toBe('tracked');
            expect(diff.files[testFile].staged).toBe(true);
        }

        // Verify it's visible in the UI
        const panelContent = await page.textContent('.diff-content');
        expect(panelContent).toContain(testFile);

        // Cleanup
        execSync(`cd ${worktreeDir} && git reset HEAD -- ${testFile}`);
        execSync(`rm -f ${filePath}`);
        await page.waitForTimeout(3000);
    });
});
