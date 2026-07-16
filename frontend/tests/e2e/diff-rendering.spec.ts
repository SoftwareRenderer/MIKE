import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync } from 'fs';
import { join } from 'path';

// --- Shared utilities ---

function getConfig() {
    return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function findWorktreeDir(): string | null {
    const worktreesDir = getConfig().workspace_dir;
    try {
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

function getDiffFileViaApi(projectId: string, taskId: string, filePath: string): any {
    return JSON.parse(execSync(
        `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff/file?path=${encodeURIComponent(filePath)}"`,
        { encoding: 'utf-8' }
    ));
}

// --- Tests: visual rendering edge cases ---

test.describe.serial('Diff rendering edge cases', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(30000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    // From original task-diff-blank-file.spec.ts

    test('unstaged blank file appears in diff panel', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `blank-untracked-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 5000 });
        await page.waitForTimeout(3000);

        execSync(`touch ${filePath}`);
        await page.waitForTimeout(5000);

        const diff = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        if (diff.files) {
            expect(diff.files[testFile]).toBeDefined();
            expect(diff.files[testFile].type).toBe('untracked');
            expect(diff.files[testFile].line_count).toBe(0);
        } else {
            expect(diff.untracked.some((u: any) => u.path === testFile)).toBe(true);
        }

        await expect(page.locator('.diff-file-section-untracked')).toBeVisible({ timeout: 5000 });
        const panelContent = await page.textContent('.diff-content');
        expect(panelContent).toContain(testFile);

        execSync(`rm -f ${filePath}`);
        await page.waitForTimeout(3000);
    });

    test('staged blank file appears in diff panel', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `blank-staged-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 5000 });
        await page.waitForTimeout(3000);

        execSync(`touch ${filePath}`);
        execSync(`cd ${worktreeDir} && git add ${testFile}`);
        await page.waitForTimeout(5000);

        const diff = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        if (diff.files) {
            expect(diff.files[testFile]).toBeDefined();
            expect(diff.files[testFile].type).toBe('tracked');
            expect(diff.files[testFile].staged).toBe(true);
        }

        const panelContent = await page.textContent('.diff-content');
        expect(panelContent).toContain(testFile);

        execSync(`cd ${worktreeDir} && git reset HEAD -- ${testFile}`);
        execSync(`rm -f ${filePath}`);
        await page.waitForTimeout(3000);
    });

    // From original task-diff-blank-lines.spec.ts

    test('file with blank removed/added lines shows diff content, not "Empty file"', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `blank-lines-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        execSync(`cat > ${filePath} << 'EOF'
line1

line3

line5
EOF`);
        execSync(`cd ${worktreeDir} && git add ${testFile} && git commit -m "Add ${testFile}"`);

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 5000 });
        await page.waitForTimeout(3000);

        execSync(`cat > ${filePath} << 'EOF'
line1

newLine

line5
EOF`);

        await page.waitForTimeout(5000);

        const diffData = getDiffFileViaApi(taskInfo.projectId, taskInfo.taskId, testFile);
        expect(diffData.rows).toBeDefined();
        expect(Array.isArray(diffData.rows)).toBe(true);
        expect(diffData.rows.length).toBeGreaterThan(0);

        for (let i = 0; i < diffData.rows.length; i++) {
            expect(Object.prototype.hasOwnProperty.call(diffData.rows[i], 'text')).toBe(true),
                `row ${i} (variant=${diffData.rows[i].variant}) should have 'text' field`;
        }

        await expect(page.locator('.diff-file-section')).toBeVisible({ timeout: 5000 });

        const fileHeader = page.locator('.diff-file-header').filter({ hasText: testFile });
        await expect(fileHeader).toBeVisible({ timeout: 5000 });
        await fileHeader.click();
        await page.waitForTimeout(1000);

        const fileContent = page.locator('.diff-file-section').filter({ hasText: testFile });
        const hasEmptyNotice = await fileContent.locator('.diff-empty-file-notice').count();
        expect(hasEmptyNotice).toBe(0, 'File with changes should NOT show "Empty file" notice');

        const diffRows = await fileContent.locator('.diff-row').count();
        expect(diffRows).toBeGreaterThan(0, 'File with changes should show diff rows');

        execSync(`cd ${worktreeDir} && git checkout -- ${testFile}`);
        execSync(`cd ${worktreeDir} && git reset HEAD~1`);
        execSync(`rm -f ${filePath}`);
        await page.waitForTimeout(2000);
    });

    // From original task-diff-untracked-clear.spec.ts

    test('diff panel clears when untracked file is removed', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const testFile = `untracked-clear-test-${uniqueId}.txt`;
        const filePath = join(worktreeDir, testFile);

        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 5000 });
        await page.waitForTimeout(3000);

        execSync(`echo "test content" > ${filePath}`);
        await page.waitForTimeout(5000);

        const diffBefore = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diffBefore.untracked.length).toBeGreaterThan(0);
        expect(diffBefore.untracked.some((u: any) => u.path === testFile)).toBe(true);

        const untrackedSectionsBefore = await page.locator('.diff-file-section-untracked').count();
        expect(untrackedSectionsBefore).toBeGreaterThan(0);

        execSync(`rm -f ${filePath}`);
        await page.waitForTimeout(6000);

        const diffAfter = getDiffViaApi(taskInfo.projectId, taskInfo.taskId);
        expect(diffAfter.untracked.some((u: any) => u.path === testFile)).toBe(false);

        const panelContent = await page.textContent('.diff-content');
        expect(panelContent).not.toContain(testFile);
    });

    // From original task-diff-section-collapse.spec.ts

    test('collapsing a section collapses all child files, file headers remain visible', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const files = [
            `section-collapse-test-${uniqueId}-a.txt`,
            `section-collapse-test-${uniqueId}-b.txt`,
            `section-collapse-test-${uniqueId}-c.txt`,
        ];
        for (const name of files) {
            writeFileSync(join(worktreeDir, name), `Content of ${name}\nLine 2\nLine 3\n`);
        }

        try {
            await page.locator('.card').first().click();
            await page.waitForSelector('.task-detail-page', { timeout: 10000 });

            await expect(async () => {
                const diffRows = await page.locator('.diff-row').count();
                expect(diffRows).toBeGreaterThan(0);
            }).toPass({ timeout: 10000 });

            await expect(async () => {
                const fileHeaders = await page.locator('.diff-file-header').count();
                expect(fileHeaders).toBeGreaterThanOrEqual(3);
            }).toPass({ timeout: 10000 });

            const unstagedHeader = page.locator('.diff-section-header.diff-section-unstaged');
            await expect(unstagedHeader).toBeVisible();

            await expect(async () => {
                const expandedHeaders = await page.locator('.diff-file-header[aria-expanded="true"]').count();
                expect(expandedHeaders).toBeGreaterThanOrEqual(3);
            }).toPass({ timeout: 5000 });

            const diffRowsBefore = await page.locator('.diff-row').count();
            expect(diffRowsBefore).toBeGreaterThan(0);

            await unstagedHeader.click();

            await expect(page.locator('.diff-section-body')).toBeVisible();

            await expect(async () => {
                const fileHeaders = await page.locator('.diff-file-header').count();
                expect(fileHeaders).toBeGreaterThanOrEqual(3);
            }).toPass({ timeout: 5000 });

            await expect(async () => {
                const collapsedHeaders = await page.locator('.diff-file-header[aria-expanded="false"]').count();
                expect(collapsedHeaders).toBeGreaterThanOrEqual(3);
            }).toPass({ timeout: 5000 });

            await expect(async () => {
                const diffRowsAfter = await page.locator('.diff-row').count();
                expect(diffRowsAfter).toBeLessThan(diffRowsBefore);
            }).toPass({ timeout: 5000 });
        } finally {
            for (const name of files) {
                try { execSync(`rm -f "${join(worktreeDir, name)}"`); } catch { /* ignore */ }
            }
        }
    });

    test('expanding a section expands all child files individually', async ({ page }) => {
        const taskInfo = getTaskInfo();
        const worktreeDir = findWorktreeDir();
        if (!taskInfo || !worktreeDir) {
            await expect(page.locator('.board')).toBeVisible();
            return;
        }

        const uniqueId = Date.now();
        const files = [
            `section-expand-test-${uniqueId}-a.txt`,
            `section-expand-test-${uniqueId}-b.txt`,
        ];
        for (const name of files) {
            writeFileSync(join(worktreeDir, name), `Content of ${name}\nLine 2\n`);
        }

        try {
            await page.locator('.card').first().click();
            await page.waitForSelector('.task-detail-page', { timeout: 10000 });

            await expect(async () => {
                const diffRows = await page.locator('.diff-row').count();
                expect(diffRows).toBeGreaterThan(0);
            }).toPass({ timeout: 10000 });

            await expect(async () => {
                const fileHeaders = await page.locator('.diff-file-header').count();
                expect(fileHeaders).toBeGreaterThanOrEqual(2);
            }).toPass({ timeout: 10000 });

            const unstagedHeader = page.locator('.diff-section-header.diff-section-unstaged');
            await expect(unstagedHeader).toBeVisible();

            await unstagedHeader.click();
            await expect(async () => {
                const collapsedHeaders = await page.locator('.diff-file-header[aria-expanded="false"]').count();
                expect(collapsedHeaders).toBeGreaterThanOrEqual(2);
            }).toPass({ timeout: 5000 });

            await unstagedHeader.click();

            await expect(async () => {
                const expandedHeaders = await page.locator('.diff-file-header[aria-expanded="true"]').count();
                expect(expandedHeaders).toBeGreaterThanOrEqual(2);
            }).toPass({ timeout: 5000 });

            await expect(async () => {
                const diffRows = await page.locator('.diff-row').count();
                expect(diffRows).toBeGreaterThan(0);
            }).toPass({ timeout: 5000 });
        } finally {
            for (const name of files) {
                try { execSync(`rm -f "${join(worktreeDir, name)}"`); } catch { /* ignore */ }
            }
        }
    });
});
