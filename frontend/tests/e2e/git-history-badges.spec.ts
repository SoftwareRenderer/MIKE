import { test, expect } from '@playwright/test';
import { execSync } from 'node:child_process';
import { getProjectId } from './helpers';

test.describe('Git History Badges', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });
    });

    test('should show branch badges and dot colors in explorer history', async ({ page }) => {
        const projectId = await getProjectId(page);

        // Get the project path from the API
        const projectPath = await page.evaluate(async (pid) => {
            const res = await fetch(`/api/projects/${pid}`);
            const project = await res.json();
            return project.path;
        }, projectId);

        // Create additional commits and a branch in the project repo
        execSync('git -c user.email="test@test.com" -c user.name="Test" commit --allow-empty -m "First feature commit"', { cwd: projectPath });
        execSync('git -c user.email="test@test.com" -c user.name="Test" commit --allow-empty -m "Second feature commit"', { cwd: projectPath });

        // Navigate to the branch view directly
        await page.evaluate((pid) => {
            window.location.href = `/?project=${pid}&view=branch`;
        }, projectId);

        // Wait for the branch view to load
        await expect(page.locator('.branch-view')).toBeVisible({ timeout: 15000 });

        // Wait for commits to load (git-graph-list should have commit rows)
        await expect(async () => {
            const count = await page.locator('.git-commit-row').count();
            expect(count).toBeGreaterThan(1);
        }).toPass({ timeout: 10000 });

        // Verify branch badge is visible (current branch should have a badge on the latest commit)
        const branchBadges = page.locator('.git-badge.branch-badge');
        await expect(branchBadges.first()).toBeVisible({ timeout: 5000 });

        // Verify the current branch badge text matches the branch selector
        const currentBranch = await page.locator('.branch-name').textContent();
        const firstBadge = await branchBadges.first().textContent();
        expect(firstBadge?.trim()).toContain(currentBranch?.trim() || '');

        // Verify dots are visible
        const dots = page.locator('.git-dot');
        await expect(dots.first()).toBeVisible();

        // Without a remote, all dots should be purple (default color)
        // Check that no dots have the .unpushed class (blue)
        const unpushedCount = await page.locator('.git-dot.unpushed').count();
        expect(unpushedCount).toBe(0);

        // Verify origin badge is NOT present (no remote configured)
        const originBadgeCount = await page.locator('.origin-badge').count();
        expect(originBadgeCount).toBe(0);
    });
});
