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

function getDiffFileViaApi(projectId: string, taskId: string, path: string): any {
    return JSON.parse(execSync(
        `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff/file?path=${encodeURIComponent(path)}"`,
        { encoding: 'utf-8' }
    ));
}

test.describe.serial('Diff panel correctly renders files with blank lines', () => {
    test('file with blank removed/added lines shows diff content, not "Empty file"', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `blank-lines-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        // Create a file with content (including blank lines) and commit it
        execSync(`cat > ${filePath} << 'EOF'
line1

line3

line5
EOF`);
        execSync(`cd ${worktreeDir} && git add ${testFile} && git commit -m "Add ${testFile}"`);

        // Open task detail
        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 5000 });

        // Wait for initial diff to load
        await page.waitForTimeout(3000);

        // Now modify the file: replace content with blank lines mixed in
        execSync(`cat > ${filePath} << 'EOF'
line1

newLine

line5
EOF`);

        // Wait for broadcast update (poller runs every 2s)
        await page.waitForTimeout(5000);

        // Verify via API that the diff file has rows with text fields
        const diffData = getDiffFileViaApi(taskInfo.projectId, taskInfo.taskId, testFile);
        expect(diffData.rows).toBeDefined();
        expect(Array.isArray(diffData.rows)).toBe(true);
        expect(diffData.rows.length).toBeGreaterThan(0);

        // Verify every row has a "text" key (even blank lines should have text: "")
        for (let i = 0; i < diffData.rows.length; i++) {
            expect(Object.prototype.hasOwnProperty.call(diffData.rows[i], 'text')).toBe(true),
                `row ${i} (variant=${diffData.rows[i].variant}) should have 'text' field`;
        }

        // Verify the file is visible in the UI and does NOT show "Empty file"
        await expect(page.locator('.diff-file-section')).toBeVisible({ timeout: 5000 });

        // Click to expand the file
        const fileHeader = page.locator('.diff-file-header').filter({ hasText: testFile });
        await expect(fileHeader).toBeVisible({ timeout: 5000 });
        await fileHeader.click();

        // Wait for the file content to expand
        await page.waitForTimeout(1000);

        // The expanded file should show diff rows, NOT "Empty file"
        const fileContent = page.locator('.diff-file-section').filter({ hasText: testFile });
        const hasEmptyNotice = await fileContent.locator('.diff-empty-file-notice').count();
        expect(hasEmptyNotice).toBe(0, 'File with changes should NOT show "Empty file" notice');

        // Should have diff rows visible
        const diffRows = await fileContent.locator('.diff-row').count();
        expect(diffRows).toBeGreaterThan(0, 'File with changes should show diff rows');

        // Cleanup
        execSync(`cd ${worktreeDir} && git checkout -- ${testFile}`);
        execSync(`cd ${worktreeDir} && git reset HEAD~1`);
        execSync(`rm -f ${filePath}`);
        await page.waitForTimeout(2000);
    });
});
