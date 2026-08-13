import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId } from './helpers';

/**
 * Regression test: staging individual files in Explorer → Review Changes must
 * reliably move each file from Unstaged to Staged. Previously, staging several
 * files in quick succession launched overlapping reloads whose out-of-order
 * responses overwrote each other, leaving just-staged files shown as unstaged
 * ("inconsistent" updates).
 */

/** Ensure the project exists and commit the base files so they are tracked. */
async function setupProject(page: Page, projectId: string): Promise<string | null> {
    const project = await page.evaluate(async (pid: string) => {
        const res = await fetch(`/api/projects/${pid}`);
        return res.ok ? res.json() : null;
    }, projectId);
    if (!project?.path) return null;
    return project.path;
}

/** Open the Review Changes panel in the Explorer view. */
async function openExplorerChangesPanel(page: Page) {
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    await page.locator('.git-status-btn').click();
    await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
}

/** Find the sidebar stage/unstage button row for a given filename within a section. */
function fileRow(page: Page, sectionTitle: string, filename: string) {
    const section = page.locator('.changes-section').filter({ has: page.locator(`.changes-section-header:has-text("${sectionTitle}")`) });
    return section.locator('.changes-file-row').filter({ has: page.locator(`.changes-file-basename:has-text("${filename}")`) });
}

test.describe.serial('Review Changes: staging individual files', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(60000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('staging two files in quick succession moves both to Staged', async ({ page }) => {
        const projectId = await getProjectId(page);
        const projectPath = await setupProject(page, projectId);
        if (!projectPath) {
            test.skip('Could not locate project path');
            return;
        }

        const uniqueId = Date.now();
        const file1 = `review-stage-${uniqueId}-1.txt`;
        const file2 = `review-stage-${uniqueId}-2.txt`;

        try {
            // Create two tracked files (commit them so they show as "M" when modified).
            execSync(`echo "base content 1" > "${projectPath}/${file1}"`);
            execSync(`echo "base content 2" > "${projectPath}/${file2}"`);
            execSync(`cd "${projectPath}" && git add -A`);
            execSync(`cd "${projectPath}" && git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Review changes staging base ${uniqueId}"`);

            // Modify both to create working-tree changes.
            execSync(`echo "modified content 1" > "${projectPath}/${file1}"`);
            execSync(`echo "modified content 2" > "${projectPath}/${file2}"`);

            // Wait for the file watcher (polls every 2s) to pick up the changes.
            await page.waitForTimeout(3000);

            await openExplorerChangesPanel(page);

            // Wait for both files to appear in the Unstaged sidebar section.
            await expect(async () => {
                const row1 = fileRow(page, 'Unstaged', file1);
                const row2 = fileRow(page, 'Unstaged', file2);
                expect(await row1.count()).toBeGreaterThan(0);
                expect(await row2.count()).toBeGreaterThan(0);
            }).toPass({ timeout: 20000 });

            // Stage both files in quick succession (no wait between clicks).
            await fileRow(page, 'Unstaged', file1).locator('.changes-stage-btn').click();
            await fileRow(page, 'Unstaged', file2).locator('.changes-stage-btn').click();

            // Both files must end up in the Staged section, and neither may remain
            // stuck in Unstaged (the regression this test guards against).
            await expect(async () => {
                const stagedRow1 = fileRow(page, 'Staged', file1);
                const stagedRow2 = fileRow(page, 'Staged', file2);
                expect(await stagedRow1.count()).toBeGreaterThan(0);
                expect(await stagedRow2.count()).toBeGreaterThan(0);
            }).toPass({ timeout: 20000 });

            await expect(async () => {
                const unstagedRow1 = fileRow(page, 'Unstaged', file1);
                const unstagedRow2 = fileRow(page, 'Unstaged', file2);
                expect(await unstagedRow1.count()).toBe(0);
                expect(await unstagedRow2.count()).toBe(0);
            }).toPass({ timeout: 20000 });
        } finally {
            // Reset the index and clean up test files/commits.
            try {
                execSync(`cd "${projectPath}" && git reset --hard HEAD~1 2>/dev/null || true`);
                execSync(`cd "${projectPath}" && git clean -fd 2>/dev/null || true`);
                execSync(`rm -f "${projectPath}/${file1}" "${projectPath}/${file2}"`);
            } catch { /* ignore cleanup errors */ }
        }
    });
});
