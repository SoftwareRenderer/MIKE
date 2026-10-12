import { test, expect, type Page } from '@playwright/test';
import { isolatedProject, gotoProject, ownProjectId, csrfFetch } from './helpers';

// Which screen an address answers is App-shell state plus a real address bar, so the close is measured in
// a browser. `docs/link-rows.md → What a close lands on`.

const CLOSE = '[aria-label="Close task explorer"]';
const OPEN_FROM_DETAIL = '.task-detail-page [aria-label="Open explorer"]';
const EXPLORER = '.explorer-view';
const DETAIL = '.task-detail-page';
const REVIEW_COLUMN = '.explorer-changes-column';

/** An explicit title and a one-word prompt keep the task out of the executor: this spec needs a
 *  worktree, not a turn. */
async function makeTask(page: Page, projectId: string, title: string) {
    const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
        method: 'POST',
        body: { title, prompt: 'x', status: 'backlog' },
    });
    expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
    return created.data.id as string;
}

test.describe.serial('Closing the task explorer', () => {
    isolatedProject();

    test('closing one that arrived by URL lands on that task, not the board', async ({ page }) => {
        const projectId = ownProjectId();
        const taskId = await makeTask(page, projectId, `Explorer close deep link ${Date.now()}`);

        await gotoProject(page, `/?project=${projectId}&task=${taskId}&view=task-explorer`);
        await expect(page.locator(EXPLORER)).toBeVisible({ timeout: 20000 });

        await page.locator(CLOSE).click();
        await expect(page.locator(DETAIL)).toBeVisible({ timeout: 20000 });
        expect(page.url()).toContain(`task=${taskId}`);
        expect(page.url()).not.toContain('view=');
    });

    test('closing the Review Changes panel keeps its explorer, and the next close leaves the explorer',
        async ({ page }) => {
            const projectId = ownProjectId();
            const taskId = await makeTask(page, projectId, `Explorer review close ${Date.now()}`);

            await gotoProject(page, `/?project=${projectId}&task=${taskId}&view=task-review`);
            await expect(page.locator(EXPLORER)).toBeVisible({ timeout: 20000 });
            await expect(page.locator(REVIEW_COLUMN)).toBeVisible();

            await page.locator(CLOSE).click();
            await expect(page.locator(REVIEW_COLUMN)).toBeHidden();
            await expect(page.locator(EXPLORER)).toBeVisible();
            expect(page.url()).toContain('view=task-explorer');

            await page.locator(CLOSE).click();
            await expect(page.locator(DETAIL)).toBeVisible({ timeout: 20000 });
            expect(page.url()).not.toContain('view=');
        });

    test('the close is a Back entry: Back returns to the explorer', async ({ page }) => {
        const projectId = ownProjectId();
        const taskId = await makeTask(page, projectId, `Explorer close back ${Date.now()}`);

        await gotoProject(page, `/?project=${projectId}&task=${taskId}&view=task-explorer`);
        await expect(page.locator(EXPLORER)).toBeVisible({ timeout: 20000 });

        await page.locator(CLOSE).click();
        await expect(page.locator(DETAIL)).toBeVisible({ timeout: 20000 });

        await page.goBack();
        await expect(page.locator(EXPLORER)).toBeVisible({ timeout: 20000 });
        expect(page.url()).toContain('view=task-explorer');
    });

    test('closing the explorer a card opened lands on that card’s task', async ({ page }) => {
        const projectId = ownProjectId();
        const title = `Explorer close from card ${Date.now()}`;
        const taskId = await makeTask(page, projectId, title);

        await gotoProject(page, `/?project=${projectId}`);
        const card = page.locator('.card', { hasText: title });
        await expect(card).toBeVisible({ timeout: 20000 });
        await card.click();
        await expect(page.locator(DETAIL)).toBeVisible({ timeout: 20000 });

        await page.locator(OPEN_FROM_DETAIL).click();
        await expect(page.locator(EXPLORER)).toBeVisible({ timeout: 20000 });

        await page.locator(CLOSE).click();
        await expect(page.locator(DETAIL)).toBeVisible({ timeout: 20000 });
        expect(page.url()).toContain(`task=${taskId}`);
    });
});
