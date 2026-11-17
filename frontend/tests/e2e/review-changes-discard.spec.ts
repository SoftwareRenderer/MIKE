import { test, expect, type Page } from '@playwright/test';
import { mkdirSync, readFileSync, rmSync, writeFileSync, existsSync } from 'node:fs';
import { join } from 'path';
import { csrfFetch, gitIn, openChangesPanel, signedInPage } from './helpers';

// Uses a dedicated repo and project (/tmp/kanban-test-repo-discard) instead of the shared
// kanban-test-repo: a discard rewrites the working tree, and other specs read that one.

const REPO = '/tmp/kanban-test-repo-discard';
const COMMITTED = 'committed by the fixture\n';

function resetWorktree() {
    gitIn(REPO, 'checkout -- .');
    gitIn(REPO, 'clean -fdq');
    writeFileSync(join(REPO, 'tracked.txt'), 'edited by the spec\n');
    writeFileSync(join(REPO, 'scratch.txt'), 'never committed\n');
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
        gitIn(REPO, 'init');
        gitIn(REPO, 'config user.email "test@kanban.local"');
        gitIn(REPO, 'config user.name "Test"');
        writeFileSync(join(REPO, 'tracked.txt'), COMMITTED);
        gitIn(REPO, 'add tracked.txt');
        gitIn(REPO, 'commit -m "discard fixture"');
        resetWorktree();

        const { context, page } = await signedInPage(browser);
        try {
            const res = await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: REPO } });
            if (!res.ok) throw new Error(`create discard project: ${JSON.stringify(res.data)}`);
            projectId = res.data.id;
        } finally {
            await context.close();
        }
    });

    test.afterAll(async ({ browser }) => {
        const { context, page } = await signedInPage(browser);
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

    // A declined dialog is a payload rule with three vitest owners, and a cancelled discard proves
    // nothing about git because nothing is sent: `git-status-panel.unit.svelte.ts` holds "sends nothing
    // when the confirmation is declined" and "names the file and says which shape of loss is coming"
    // (the `goes back to its last commit` / `deleted from disk` wording per status letter), and
    // `confirmation-dialog.unit.svelte.ts` holds "empties the slot and resolves false when cancelled".
    // What is left for this lane is what git does to the bytes, which is the two specs below.

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
