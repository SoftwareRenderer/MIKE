import { test, expect, type Browser, type Page } from '@playwright/test';
import { rmSync, mkdirSync, writeFileSync, existsSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch } from './helpers';

// Review Changes panel: the Commit button must always be visible (disabled
// when nothing is staged), and Pull/Push must work against a real remote.
//
// Isolation: this spec runs against a DEDICATED repo + project
// (/tmp/kanban-test-repo-pullpush) with a local bare remote
// (/tmp/kanban-test-remote-pullpush) — never the shared kanban-test-repo — so
// its git operations cannot race with other specs' parallel workers.

const REPO = '/tmp/kanban-test-repo-pullpush';
const REMOTE = '/tmp/kanban-test-remote-pullpush';
const CLONE = '/tmp/kanban-test-clone-pullpush';
const PROJECT_NAME = 'kanban-test-repo-pullpush';

function git(dir: string, args: string) {
    execSync(`git ${args}`, { cwd: dir, stdio: 'pipe' });
}

// `page` fixtures aren't available in beforeAll/afterAll — create a dedicated
// context (with the harness's logged-in storage state) for setup/teardown.
async function setupPage(browser: Browser) {
    const context = await browser.newContext({
        storageState: '/tmp/kanban-test-storage-state.json',
    });
    const page = await context.newPage();
    await page.goto('/');
    return { context, page };
}

/** Open the dedicated project's Review Changes panel. */
async function openChangesPanel(page: Page) {
    await page.locator('.project-item', { hasText: PROJECT_NAME }).first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    await page.locator('.git-status-btn').click();
    await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
}

test.describe.serial('Review Changes: commit button + pull/push', () => {
    let projectId = '';

    test.beforeAll(async ({ browser }) => {
        // Fresh dedicated repo with an initial commit.
        rmSync(REPO, { recursive: true, force: true });
        rmSync(REMOTE, { recursive: true, force: true });
        rmSync(CLONE, { recursive: true, force: true });
        mkdirSync(REPO, { recursive: true });
        writeFileSync(join(REPO, 'README.md'), 'pullpush fixture repo\n');
        git(REPO, 'init');
        git(REPO, 'config user.email "test@kanban.local"');
        git(REPO, 'config user.name "Test"');
        git(REPO, 'add README.md');
        git(REPO, 'commit -m "Initial commit"');

        // Bare remote with the initial commit (upstream tracking configured).
        mkdirSync(REMOTE, { recursive: true });
        git(REMOTE, 'init --bare');
        git(REPO, `remote add origin ${REMOTE}`);
        git(REPO, 'push -u origin HEAD');

        const { context, page } = await setupPage(browser);
        try {
            const res = await csrfFetch(page, '/api/projects', {
                method: 'POST',
                body: { path: REPO },
            });
            if (!res.ok) throw new Error(`create pullpush project: ${JSON.stringify(res.data)}`);
            projectId = res.data.id;
        } finally {
            await context.close();
        }
    });

    test.afterAll(async ({ browser }) => {
        const { context, page } = await setupPage(browser);
        try {
            if (projectId) {
                await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
            }
        } catch { /* ignore teardown errors */ } finally {
            await context.close();
        }
        rmSync(REPO, { recursive: true, force: true });
        rmSync(REMOTE, { recursive: true, force: true });
        rmSync(CLONE, { recursive: true, force: true });
    });

    test.beforeEach(async ({ page }) => {
        test.setTimeout(60000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
        await openChangesPanel(page);
    });

    test('Commit button is visible but disabled on a clean tree; Pull/Push buttons present', async ({ page }) => {
        const commitBtn = page.locator('.panel-commit-btn');
        await expect(commitBtn).toBeVisible();
        await expect(commitBtn).toBeDisabled();
        await expect(commitBtn).toHaveAttribute('title', 'Nothing staged to commit');

        // A clean tree defaults to the last-commit comparison (EMPTY_TREE → HEAD
        // for this single-commit fixture repo; its only file is README.md).
        await expect(page.locator('.sr-only', { hasText: 'Showing diff with 1 file' })).toBeVisible({ timeout: 15000 });

        const pullBtn = page.locator('button[aria-label="Pull from remote"]');
        const pushBtn = page.locator('button[aria-label="Push to remote"]');
        await expect(pullBtn).toBeVisible();
        await expect(pushBtn).toBeVisible();
    });

    test('Commit button enables with staged changes; Push delivers the commit to the remote', async ({ page }) => {
        const uniqueId = Date.now();
        const file = `push-${uniqueId}.txt`;
        writeFileSync(join(REPO, file), 'push test\n');
        git(REPO, `add ${file}`);

        // Browser-refresh reloads the panel (restored via view=review) with fresh git state.
        await page.reload();
        await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 15000 });
        await expect(async () => {
            expect(await page.locator('.changes-file-row', { hasText: file }).count()).toBeGreaterThan(0);
        }).toPass({ timeout: 20000 });

        // Staged entries exist → Commit button is enabled.
        const commitBtn = page.locator('.panel-commit-btn');
        await expect(commitBtn).toBeEnabled();
        await expect(commitBtn).toHaveAttribute('title', 'Commit staged changes');

        // Commit via CLI (the commit dialog flow is covered by commit-dialog.spec.ts),
        // then push through the panel's Push button.
        git(REPO, `commit -m "push fixture ${uniqueId}"`);
        await page.locator('button[aria-label="Push to remote"]').click();
        await expect(page.locator('.toast.success', { hasText: 'Pushed to remote' })).toBeVisible({ timeout: 15000 });

        // The bare remote's HEAD must now match the local HEAD (a bare repo
        // has no index, so compare hashes instead of ls-files).
        const localHead = execSync(`git -C ${REPO} rev-parse HEAD`, { encoding: 'utf-8' }).trim();
        const remoteHead = execSync(`git -C ${REMOTE} rev-parse HEAD`, { encoding: 'utf-8' }).trim();
        expect(remoteHead).toBe(localHead);
    });

    test('Pull fetches new remote commits', async ({ page }) => {
        const uniqueId = Date.now();
        const file = `pull-${uniqueId}.txt`;

        // Advance the remote via a second clone.
        git(REPO, `clone ${REMOTE} ${CLONE}`);
        git(CLONE, 'config user.email "test@kanban.local"');
        git(CLONE, 'config user.name "Test"');
        writeFileSync(join(CLONE, file), 'from remote\n');
        git(CLONE, `add ${file}`);
        git(CLONE, `commit -m "remote fixture ${uniqueId}"`);
        git(CLONE, 'push');

        await page.locator('button[aria-label="Pull from remote"]').click();
        await expect(page.locator('.toast.success', { hasText: 'Pulled from remote' })).toBeVisible({ timeout: 15000 });

        // The pulled commit must be in the local repo.
        expect(existsSync(join(REPO, file))).toBe(true);
    });

    test('Push without upstream shows an error toast and keeps the panel open', async ({ page }) => {
        git(REPO, 'branch --unset-upstream');

        await page.locator('button[aria-label="Push to remote"]').click();
        const errorToast = page.locator('.toast.error');
        await expect(errorToast).toBeVisible({ timeout: 15000 });
        await expect(errorToast).toContainText(/upstream/i);

        // The panel stays open and usable.
        await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible();
        await expect(page.locator('.panel-commit-btn')).toBeVisible();
    });
});
