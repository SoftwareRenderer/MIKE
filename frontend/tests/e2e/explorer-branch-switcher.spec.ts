import { test, expect } from '@playwright/test';
import { execSync } from 'node:child_process';
import { writeFileSync, rmSync } from 'node:fs';
import { gotoProject, isolatedProject, ownProjectId, ownProjectPath } from './helpers.ts';

/**
 * Switching branches in the explorer's header, against a real repository: only what git and the server can
 * stage. Payload-shaped rules live in `explorer-header-branch-list.unit.svelte.ts` (docs/testing.md).
 */

const BLOCKER = 'branch-switch-blocker.txt';

test.describe('Explorer branch switcher', { tag: '@explorer-branch' }, () => {
    isolatedProject();

    test('the list opens on the trigger and closes on a click anywhere else', async ({ page }) => {
        const projectId = ownProjectId();
        await gotoProject(page, `/?project=${projectId}&view=explorer`);

        await page.click('.explorer-dropdown-btn');
        await expect(page.locator('.explorer-dropdown-list')).toBeVisible();
        // Opening puts focus in the filter field.
        await expect(page.locator('.explorer-dropdown-filter')).toBeFocused();

        await page.click('.explorer-view', { position: { x: 5, y: 5 } });
        await expect(page.locator('.explorer-dropdown-list')).toHaveCount(0);
    });

    test('a branch another worktree holds is not offered, and the list says what it left out', async ({ page }) => {
        const repo = repoPath();
        const projectId = ownProjectId();
        // The name carries a slash that is not MIKE's namespace, because the rule is the holder and not the prefix.
        const HELD_BRANCH = 'held/by/worktree';
        // The directory is worker-scoped, so two workers cannot remove each other's worktree.
        const heldDir = `/tmp/kanban-held-worktree-w${process.env.TEST_PARALLEL_INDEX ?? '0'}`;
        execSync(`git -C ${repo} worktree prune`);
        execSync(`rm -rf ${heldDir}`);
        execSync(`git -C ${repo} worktree add -q -B ${HELD_BRANCH} ${heldDir} HEAD`);

        try {
            await gotoProject(page, `/?project=${projectId}&view=explorer`);
            await page.click('.explorer-dropdown-btn');
            await expect(page.locator('.explorer-dropdown-filter')).toBeVisible();

            const names = await page.locator('.explorer-dropdown-name').allTextContents();
            expect(names, 'git refuses this checkout, so it is not a choice').not.toContain(HELD_BRANCH);
            // The short list is accounted for.
            await expect(page.locator('.explorer-dropdown-note'))
                .toContainText('1 branch is checked out in another worktree');
            // No task owns this ref, so nothing promises a row to find by typing.
            await expect(page.locator('.explorer-dropdown-hint')).toHaveCount(0);
        } finally {
            execSync(`git -C ${repo} worktree remove --force ${heldDir}`);
        }
    });

    test('a switch git refuses says so, with git\'s reason attached', async ({ page }) => {
        const repo = repoPath();
        const projectId = ownProjectId();
        const heldDir = `/tmp/kanban-blocker-worktree-w${process.env.TEST_PARALLEL_INDEX ?? '0'}`;
        execSync(`git -C ${repo} worktree prune`);
        execSync(`rm -rf ${heldDir}`);
        // A committed file collides with an untracked one here, so git refuses the switch itself.
        execSync(`git -C ${repo} worktree add -q -B refuse-me ${heldDir} HEAD`);
        writeFileSync(`${heldDir}/${BLOCKER}`, 'committed on the other branch\n');
        execSync(`git -C ${heldDir} add ${BLOCKER}`);
        execSync(`git -C ${heldDir} commit -qm "collide"`);
        execSync(`git -C ${repo} worktree remove --force ${heldDir}`);
        writeFileSync(`${repo}/${BLOCKER}`, 'untracked here\n');

        try {
            await gotoProject(page, `/?project=${projectId}&view=explorer`);
            await page.click('.explorer-dropdown-btn');
            await page.fill('.explorer-dropdown-filter', 'refuse-me');
            await page.click('.explorer-dropdown-rows .explorer-dropdown-item >> text=refuse-me');

            const toast = page.locator('.toast').first();
            await expect(toast).toBeVisible({ timeout: 15_000 });
            const notice = (await toast.textContent()) ?? '';
            expect(notice).toContain('refuse-me');
            expect(notice.toLowerCase(), 'the reason is the useful half').toContain('untracked');
        } finally {
            execSync(`git -C ${repo} checkout -q master`, { stdio: 'ignore' });
            execSync(`git -C ${repo} branch -q -D refuse-me`, { stdio: 'ignore' });
            rmSync(`${repo}/${BLOCKER}`, { force: true });
        }
    });

    test('the filter leaves the rows a query could name, and never claims an empty repository', async ({ page }) => {
        const repo = repoPath();
        const projectId = ownProjectId();
        execSync(`git -C ${repo} rev-parse --verify feature/switch-target 2>/dev/null || git -C ${repo} branch feature/switch-target HEAD`);

        await gotoProject(page, `/?project=${projectId}&view=explorer`);
        await page.click('.explorer-dropdown-btn');
        await expect(page.locator('.explorer-dropdown-filter')).toBeVisible();

        await page.fill('.explorer-dropdown-filter', 'switch-target');
        await expect(page.locator('.explorer-dropdown-item')).toHaveCount(1);
        await expect(page.locator('.explorer-dropdown-item').first()).toContainText('feature/switch-target');

        // A query no ref answers keeps the list and says what happened.
        await page.fill('.explorer-dropdown-filter', 'zzz-no-such-branch');
        await expect(page.locator('.explorer-dropdown-list')).toBeVisible();
        await expect(page.locator('.explorer-dropdown-item.empty')).toContainText('No branch matches');
        await expect(page.locator('.explorer-dropdown-item.empty')).not.toContainText('This repository has no branches');
    });

    test('the keyboard reaches a row and Enter moves the checkout', async ({ page }) => {
        const repo = repoPath();
        const projectId = ownProjectId();
        const before = execSync(`git -C ${repo} rev-parse --abbrev-ref HEAD`).toString().trim();
        execSync(`git -C ${repo} rev-parse --verify feature/switch-target 2>/dev/null || git -C ${repo} branch feature/switch-target HEAD`);

        try {
            await gotoProject(page, `/?project=${projectId}&view=explorer`);
            await page.click('.explorer-dropdown-btn');
            await expect(page.locator('.explorer-dropdown-filter')).toBeVisible();
            await page.fill('.explorer-dropdown-filter', 'switch-target');

            await page.keyboard.press('ArrowDown');
            // This asserts focus, not the `active` class, which marks the branch this checkout is on.
            await expect(page.locator('.explorer-dropdown-item:focus')).toContainText('feature/switch-target');
            await page.keyboard.press('Enter');

            await expect.poll(() => execSync(`git -C ${repo} rev-parse --abbrev-ref HEAD`).toString().trim(),
                { timeout: 15_000 }).toBe('feature/switch-target');
        } finally {
            execSync(`git -C ${repo} checkout -q ${before} 2>/dev/null || true`);
        }
    });
});

/** isolatedProject() claimed this worker's repo, and these tests edit it. */
function repoPath(): string {
    const path = ownProjectPath();
    if (!path) throw new Error('this spec claims no project of its own');
    return path;
}
