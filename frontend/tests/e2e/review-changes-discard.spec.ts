import { test, expect, type Browser, type Page } from '@playwright/test';
import { mkdirSync, readFileSync, rmSync, writeFileSync, existsSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch, selectProject } from './helpers';

// Uses a dedicated repo and project (/tmp/kanban-test-repo-discard) instead of the shared
// kanban-test-repo: a discard rewrites the working tree, and other specs read that one.

const REPO = '/tmp/kanban-test-repo-discard';
const COMMITTED = 'committed by the fixture\n';

function git(dir: string, args: string) {
    execSync(`git ${args}`, { cwd: dir, stdio: 'pipe' });
}

function resetWorktree() {
    git(REPO, 'checkout -- .');
    git(REPO, 'clean -fdq');
    writeFileSync(join(REPO, 'tracked.txt'), 'edited by the spec\n');
    writeFileSync(join(REPO, 'scratch.txt'), 'never committed\n');
}

async function setupPage(browser: Browser) {
    const context = await browser.newContext({
        storageState: '/tmp/kanban-test-storage-state.json',
    });
    const page = await context.newPage();
    await page.goto('/');
    return { context, page };
}

async function openChangesPanel(page: Page, projectId: string) {
    await page.goto('/');
    await selectProject(page, projectId);
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    await page.locator('.git-status-btn').click();
    await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
}

async function clickDiscard(page: Page, file: string) {
    const row = page.locator('.changes-file-row', { hasText: file });
    await expect(row).toBeVisible({ timeout: 15000 });
    await row.hover();
    await row.locator(`button[aria-label="Discard ${file}"]`).click();
}

function confirmDialog(page: Page) {
    return page.locator('[role="dialog"]', { hasText: 'Discard changes?' });
}

test.describe.serial('Review Changes: discard', () => {
    let projectId = '';

    test.beforeAll(async ({ browser }) => {
        rmSync(REPO, { recursive: true, force: true });
        mkdirSync(REPO, { recursive: true });
        git(REPO, 'init');
        git(REPO, 'config user.email "test@kanban.local"');
        git(REPO, 'config user.name "Test"');
        writeFileSync(join(REPO, 'tracked.txt'), COMMITTED);
        git(REPO, 'add tracked.txt');
        git(REPO, 'commit -m "discard fixture"');
        resetWorktree();

        const { context, page } = await setupPage(browser);
        try {
            const res = await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: REPO } });
            if (!res.ok) throw new Error(`create discard project: ${JSON.stringify(res.data)}`);
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
    });

    test.beforeEach(async ({ page }) => {
        test.setTimeout(90000);
        resetWorktree();
        await openChangesPanel(page, projectId);
    });

    test('cancelling the confirmation leaves the file alone', async ({ page }) => {
        await clickDiscard(page, 'tracked.txt');
        const dialog = confirmDialog(page);
        await expect(dialog).toBeVisible({ timeout: 5000 });
        await expect(dialog).toContainText('goes back to its last commit');
        await dialog.locator('.btn-cancel').click();
        await expect(dialog).not.toBeVisible({ timeout: 5000 });

        expect(readFileSync(join(REPO, 'tracked.txt'), 'utf-8')).toBe('edited by the spec\n');
        await expect(page.locator('.changes-file-row', { hasText: 'tracked.txt' })).toBeVisible();
    });

    test('confirming reverts one file and touches no other', async ({ page }) => {
        await clickDiscard(page, 'tracked.txt');
        const dialog = confirmDialog(page);
        await expect(dialog).toBeVisible({ timeout: 5000 });
        await expect(dialog).toContainText('tracked.txt');
        await dialog.locator('.btn-confirm').click();

        await expect(page.locator('.toast.success', { hasText: 'Changes discarded' })).toBeVisible({ timeout: 15000 });
        expect(readFileSync(join(REPO, 'tracked.txt'), 'utf-8')).toBe(COMMITTED);
        await expect(page.locator('.changes-file-row', { hasText: 'tracked.txt' })).toHaveCount(0);

        expect(readFileSync(join(REPO, 'scratch.txt'), 'utf-8')).toBe('never committed\n');
        await expect(page.locator('.changes-file-row', { hasText: 'scratch.txt' })).toBeVisible();
    });

    test('confirming deletes an untracked file', async ({ page }) => {
        await clickDiscard(page, 'scratch.txt');
        const dialog = confirmDialog(page);
        await expect(dialog).toBeVisible({ timeout: 5000 });
        await expect(dialog).toContainText('deleted from disk');
        await dialog.locator('.btn-confirm').click();

        await expect(page.locator('.toast.success', { hasText: 'Changes discarded' })).toBeVisible({ timeout: 15000 });
        expect(existsSync(join(REPO, 'scratch.txt'))).toBe(false);
        await expect(page.locator('.changes-file-row', { hasText: 'scratch.txt' })).toHaveCount(0);
        expect(readFileSync(join(REPO, 'tracked.txt'), 'utf-8')).toBe('edited by the spec\n');
    });
});
