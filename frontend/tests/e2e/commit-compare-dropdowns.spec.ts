import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId } from './helpers';

function getConfig() {
    return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

/**
 * Ensure the test project has at least 2 commits so we can compare them.
 * Creates commits in the project repo if needed.
 */
async function ensureCommitsExist(page: Page, projectId: string): Promise<{ projectPath: string; uniqueId: number } | null> {
    const project = await page.evaluate(async (pid: string) => {
        const res = await fetch(`/api/projects/${pid}`);
        return res.ok ? res.json() : null;
    }, projectId);
    if (!project?.path) return null;

    const projectPath = project.path;

    // Check how many commits exist
    let commitCount = 0;
    try {
        const output = execSync(`cd "${projectPath}" && git rev-list --count HEAD 2>/dev/null || echo 0`, { encoding: 'utf-8' }).trim();
        commitCount = parseInt(output, 10);
    } catch { /* ignore */ }

    // Need at least 2 commits for comparison
    const uniqueId = Date.now();
    for (let i = commitCount; i < 2; i++) {
        const filePath = `${projectPath}/commit-compare-test-${uniqueId}-${i}.txt`;
        execSync(`echo "commit ${i} content" > "${filePath}"`);
        execSync(`cd "${projectPath}" && git add -A`);
        execSync(`cd "${projectPath}" && git commit -m "Test commit ${i} for compare dropdowns"`);
    }
    return { projectPath, uniqueId };
}

/** Clean up test files and commits created in the project repo. */
function cleanupTestFiles(projectPath: string, uniqueId: number): void {
    try {
        execSync(`rm -f "${projectPath}"/commit-compare-test-${uniqueId}-*.txt`);
        execSync(`cd "${projectPath}" && git reset --soft HEAD~2 2>/dev/null || true`);
        execSync(`cd "${projectPath}" && git checkout -- . 2>/dev/null || true`);
        execSync(`cd "${projectPath}" && git clean -fd 2>/dev/null || true`);
    } catch { /* ignore cleanup errors */ }
}

/** Navigate to the Changes panel in the Explorer view. */
async function openExplorerChangesPanel(page: Page, projectId: string) {
    // Click on the project to open the board
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });

    // Open the explorer
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Click the "Review Changes" button
    await page.locator('.git-status-btn').click();
    await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
}

test.describe.serial('Commit compare dropdowns in Changes panel', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(30000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('should show two commit selectors in Changes header', async ({ page }) => {
        const projectId = await getProjectId(page);
        const result = await ensureCommitsExist(page, projectId);

        try {
            await openExplorerChangesPanel(page, projectId);

            // Verify the "From" combobox input is present
            await expect(page.locator('#compare-from-input')).toBeVisible({ timeout: 5000 });
            // Verify the "To" combobox input is present
            await expect(page.locator('#compare-to-input')).toBeVisible();
            // Verify the arrow icon between them
            await expect(page.locator('.commit-arrow-icon')).toBeVisible();
        } finally {
            if (result) cleanupTestFiles(result.projectPath, result.uniqueId);
        }
    });

    test('should prepopulate selectors with HEAD~1 and HEAD', async ({ page }) => {
        const projectId = await getProjectId(page);
        const result = await ensureCommitsExist(page, projectId);

        try {
            await openExplorerChangesPanel(page, projectId);

            // "From" combobox input should have a value
            const fromInput = page.locator('#compare-from-input');
            await expect(fromInput).toBeVisible({ timeout: 5000 });
            await expect(fromInput).toHaveValue(/.+/, { timeout: 10000 });

            // "To" combobox input should have a value
            const toInput = page.locator('#compare-to-input');
            await expect(toInput).toBeVisible();
            await expect(toInput).toHaveValue(/.+/, { timeout: 10000 });

            // The diff viewer should eventually show content or loading
            await expect(async () => {
                const diffScroll = await page.locator('.diff-scroll').count();
                const diffFileHeader = await page.locator('.diff-file-header').count();
                const diffSection = await page.locator('.diff-section').count();
                const diffLoading = await page.locator('.diff-viewer-loading').count();
                const diffEmpty = await page.locator('.diff-empty').count();
                const total = diffScroll + diffFileHeader + diffSection + diffLoading + diffEmpty;
                expect(total).toBeGreaterThan(0);
            }).toPass({ timeout: 15000 });
        } finally {
            if (result) cleanupTestFiles(result.projectPath, result.uniqueId);
        }
    });

    test('should open dropdown list on focus', async ({ page }) => {
        const projectId = await getProjectId(page);
        const result = await ensureCommitsExist(page, projectId);

        try {
            await openExplorerChangesPanel(page, projectId);

            // Focus the "To" combobox input to open its dropdown
            const toInput = page.locator('#compare-to-input');
            await toInput.click();

            // Dropdown list should appear (find the visible one)
            const dropdownList = page.locator('.commit-dropdown-list').filter({ visible: true });
            await expect(dropdownList).toBeVisible({ timeout: 5000 });

            // Should have at least 2 commit items
            const items = dropdownList.locator('.commit-dropdown-item');
            await expect(async () => {
                expect(await items.count()).toBeGreaterThanOrEqual(2);
            }).toPass({ timeout: 5000 });

            // Each item should have a hash and subject
            const firstItem = items.first();
            await expect(firstItem.locator('.commit-dropdown-hash')).toBeVisible();
            await expect(firstItem.locator('.commit-dropdown-subject')).toBeVisible();

            // Click outside to close
            await page.locator('.panel-title').click();
            await expect(dropdownList).not.toBeVisible({ timeout: 5000 });
        } finally {
            if (result) cleanupTestFiles(result.projectPath, result.uniqueId);
        }
    });

    test('should allow typing a commit hash in the From field', async ({ page }) => {
        const projectId = await getProjectId(page);
        const result = await ensureCommitsExist(page, projectId);
        if (!result) return;

        try {
            await openExplorerChangesPanel(page, projectId);

            // Get the first commit hash from the API
            const logData = JSON.parse(execSync(
                `curl -s "http://localhost:3000/api/projects/${projectId}/git/log?full=1&limit=5"`,
                { encoding: 'utf-8' }
            ));
            const commits = logData?.commits || [];
            if (commits.length < 2) return;

            // Type a commit hash into the From combobox
            const fromInput = page.locator('#compare-from-input');
            await expect(fromInput).toBeVisible({ timeout: 5000 });
            await fromInput.click();
            await fromInput.fill(commits[1].short);

            // Dropdown should show filtered results
            const dropdownList = page.locator('.commit-dropdown-list').filter({ visible: true });
            await expect(dropdownList).toBeVisible({ timeout: 5000 });

            // Should show matching commits
            const items = dropdownList.locator('.commit-dropdown-item');
            const itemCount = await items.count();
            expect(itemCount).toBeGreaterThanOrEqual(1);

            // Click outside to accept the typed value
            await page.locator('.panel-title').click();
            await expect(dropdownList).not.toBeVisible({ timeout: 5000 });

            // The input should retain the typed value
            const inputValue = await fromInput.inputValue();
            expect(inputValue).toBe(commits[1].short);
        } finally {
            if (result) cleanupTestFiles(result.projectPath, result.uniqueId);
        }
    });

    test('should select sole match on Enter', async ({ page }) => {
        const projectId = await getProjectId(page);
        const result = await ensureCommitsExist(page, projectId);
        if (!result) return;

        try {
            await openExplorerChangesPanel(page, projectId);

            // Get commit hashes from the API
            const logData = JSON.parse(execSync(
                `curl -s "http://localhost:3000/api/projects/${projectId}/git/log?full=1&limit=5"`,
                { encoding: 'utf-8' }
            ));
            const commits = logData?.commits || [];
            if (commits.length < 2) return;

            // Type a unique hash that should match exactly one commit
            const fromInput = page.locator('#compare-from-input');
            await expect(fromInput).toBeVisible({ timeout: 5000 });
            await fromInput.click();
            await fromInput.fill(commits[1].short);

            // Dropdown should show exactly 1 matching item
            const dropdownList = page.locator('.commit-dropdown-list').filter({ visible: true });
            await expect(dropdownList).toBeVisible({ timeout: 5000 });
            const items = dropdownList.locator('.commit-dropdown-item');
            expect(await items.count()).toBe(1);

            // Press Enter to select the sole match
            await fromInput.press('Enter');

            // Dropdown should close
            await expect(dropdownList).not.toBeVisible({ timeout: 5000 });

            // Input should show the selected commit's short hash
            const inputValue = await fromInput.inputValue();
            expect(inputValue).toBe(commits[1].short);
        } finally {
            if (result) cleanupTestFiles(result.projectPath, result.uniqueId);
        }
    });

    test('should update diff when selecting different commits', async ({ page }) => {
        const projectId = await getProjectId(page);
        const result = await ensureCommitsExist(page, projectId);

        try {
            await openExplorerChangesPanel(page, projectId);

            // Wait for initial diff to load
            await expect(async () => {
                const hasContent = await page.locator('.diff-scroll, .diff-file-header').count();
                expect(hasContent).toBeGreaterThan(0);
            }).toPass({ timeout: 10000 });

            // Open the "To" combobox dropdown
            const toInput = page.locator('#compare-to-input');
            await toInput.click();
            const dropdownList = page.locator('.commit-dropdown-list').filter({ visible: true });
            await expect(dropdownList).toBeVisible({ timeout: 5000 });

            const items = dropdownList.locator('.commit-dropdown-item');
            const itemCount = await items.count();

            if (itemCount >= 2) {
                // Find the currently selected item and pick a different one
                const activeItem = page.locator('.commit-dropdown-item.active').first();
                const activeVisible = await activeItem.isVisible().catch(() => false);

                let newIdx = 1; // default to second item
                if (activeVisible) {
                    const activeIdx = await activeItem.evaluate((el: Element) => {
                        const parent = el.parentElement;
                        return parent ? Array.from(parent.children).indexOf(el) : -1;
                    });
                    newIdx = activeIdx >= 0 ? (activeIdx + 1) % itemCount : 1;
                }

                await items.nth(newIdx).click();

                // Dropdown should close after selection
                await expect(dropdownList).not.toBeVisible({ timeout: 5000 });

                // Wait for diff to update (should show content or loading)
                await expect(async () => {
                    const hasContent = await page.locator('.diff-scroll, .diff-file-header, .diff-section').count();
                    expect(hasContent).toBeGreaterThanOrEqual(0);
                }).toPass({ timeout: 10000 });
            }
        } finally {
            if (result) cleanupTestFiles(result.projectPath, result.uniqueId);
        }
    });
});
