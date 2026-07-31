import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId } from './helpers';

/**
 * Ensure the test project has at least 2 commits so we can compare them.
 */
async function ensureCommitsExist(page: Page, projectId: string): Promise<{ projectPath: string; uniqueId: number } | null> {
    const project = await page.evaluate(async (pid: string) => {
        const res = await fetch(`/api/projects/${pid}`);
        return res.ok ? res.json() : null;
    }, projectId);
    if (!project?.path) return null;

    const projectPath = project.path;

    let commitCount = 0;
    try {
        const output = execSync(`cd "${projectPath}" && git rev-list --count HEAD 2>/dev/null || echo 0`, { encoding: 'utf-8' }).trim();
        commitCount = parseInt(output, 10);
    } catch { /* ignore */ }

    const uniqueId = Date.now();
    for (let i = commitCount; i < 2; i++) {
        const filePath = `${projectPath}/changes-default-test-${uniqueId}-${i}.txt`;
        execSync(`echo "commit ${i} content" > "${filePath}"`);
        execSync(`cd "${projectPath}" && git add -A`);
        execSync(`cd "${projectPath}" && git commit -m "Test commit ${i} for changes default"`);
    }
    return { projectPath, uniqueId };
}

/** Clean up test files and commits. */
function cleanupTestFiles(projectPath: string, uniqueId: number): void {
    try {
        execSync(`rm -f "${projectPath}"/changes-default-test-${uniqueId}-*.txt`);
        execSync(`cd "${projectPath}" && git reset --soft HEAD~2 2>/dev/null || true`);
        execSync(`cd "${projectPath}" && git checkout -- . 2>/dev/null || true`);
        execSync(`cd "${projectPath}" && git clean -fd 2>/dev/null || true`);
    } catch { /* ignore cleanup errors */ }
}

/** Navigate to the Changes panel in the Explorer view. */
async function openExplorerChangesPanel(page: Page, projectId: string) {
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });

    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    await page.locator('.git-status-btn').click();
    await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
}

test.describe.serial('Changes panel default comparison mode', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(30000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('shows commit comparison when no working tree changes exist', async ({ page }) => {
        const projectId = await getProjectId(page);
        const result = await ensureCommitsExist(page, projectId);
        if (!result) {
            test.skip('Could not ensure commits exist');
            return;
        }

        // Ensure working tree is clean
        execSync(`cd "${result.projectPath}" && git checkout -- . 2>/dev/null || true`);
        execSync(`cd "${result.projectPath}" && git clean -fd 2>/dev/null || true`);

        try {
            await openExplorerChangesPanel(page, projectId);

            // Commit comparison selectors should be populated with commit hashes
            const fromInput = page.locator('#compare-from-input');
            const toInput = page.locator('#compare-to-input');
            await expect(fromInput).toBeVisible({ timeout: 10000 });
            await expect(toInput).toBeVisible();

            // Both inputs should have actual commit hash values (not empty placeholders)
            await expect(fromInput).toHaveValue(/.+/, { timeout: 10000 });
            await expect(toInput).toHaveValue(/.+/);

            // The diff viewer should show commit comparison content
            await expect(async () => {
                const diffScroll = await page.locator('.diff-scroll').count();
                const diffFileHeader = await page.locator('.diff-file-header').count();
                const diffSection = await page.locator('.diff-section').count();
                expect(diffScroll + diffFileHeader + diffSection).toBeGreaterThan(0);
            }).toPass({ timeout: 15000 });

            // Should NOT show "No changes since last commit" when commits exist
            await expect(async () => {
                const visible = await page.locator('.panel-empty:has-text("No changes since last commit")').isVisible().catch(() => false);
                expect(visible).toBe(false);
            }).toPass({ timeout: 15000 });
        } finally {
            cleanupTestFiles(result.projectPath, result.uniqueId);
        }
    });

    test('shows working tree diff when changes exist', async ({ page }) => {
        const projectId = await getProjectId(page);
        const result = await ensureCommitsExist(page, projectId);
        if (!result) {
            test.skip('Could not ensure commits exist');
            return;
        }

        const testFile = `working-tree-test-${Date.now()}.txt`;
        const filePath = `${result.projectPath}/${testFile}`;

        try {
            // Create a working tree change and commit it first so git tracks the directory
            execSync(`echo "Working tree change content" > "${filePath}"`);
            execSync(`cd "${result.projectPath}" && git add ${testFile}`);
            execSync(`cd "${result.projectPath}" && git commit -m "Add test file for working tree test"`);

            // Now modify the file to create a working tree change
            execSync(`echo "Modified content for working tree test" > "${filePath}"`);

            // Wait for file watcher (polls every 2s)
            await page.waitForTimeout(3000);

            await openExplorerChangesPanel(page, projectId);

            // The changes panel should show the working tree diff (unstaged section)
            await expect(async () => {
                const sidebarText = await page.locator('.changes-sidebar').textContent().catch(() => '');
                expect(sidebarText).toContain('Unstaged');
            }).toPass({ timeout: 15000 });

            // The file should appear in the sidebar or diff
            await expect(async () => {
                const sidebarText = await page.locator('.changes-sidebar').textContent().catch(() => '');
                const diffText = await page.locator('.changes-diff').textContent().catch(() => '');
                expect(sidebarText + diffText).toContain(testFile);
            }).toPass({ timeout: 15000 });
        } finally {
            // Reset the commit and clean up
            execSync(`cd "${result.projectPath}" && git reset --soft HEAD~1 2>/dev/null || true`);
            execSync(`rm -f "${filePath}"`);
            execSync(`cd "${result.projectPath}" && git checkout -- . 2>/dev/null || true`);
            cleanupTestFiles(result.projectPath, result.uniqueId);
        }
    });
});
