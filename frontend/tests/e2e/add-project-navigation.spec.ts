import { test, expect, type Page } from '@playwright/test';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { apiGetJson, csrfFetch, getProjectId, gotoProject } from './helpers';

// A project the person just added is the screen they land on: its board, under its own address, with Back
// returning to where they were. A clone navigates before it answers, because the progress bar is on that
// project's row.

// Per-worker scratch: the lane runs four workers against one server. The clone source never sits under
// CLONE_PARENT, or the clone target would be the source.
const WORKER = process.env.TEST_PARALLEL_INDEX || '0';
const SCRATCH = `/tmp/kanban-e2e-add-nav-${WORKER}`;
const SRC_ROOT = `${SCRATCH}/src`;
const CLONE_PARENT = `${SCRATCH}/dst`;

/** A git repo with one commit, ready to add or clone. */
function gitRepo(path: string): string {
    rmSync(path, { recursive: true, force: true });
    mkdirSync(path, { recursive: true });
    writeFileSync(`${path}/README.md`, 'seed\n');
    execSync('git init -q', { cwd: path });
    execSync('git add -A', { cwd: path });
    execSync('git -c user.email="e2e@mike.local" -c user.name="e2e" commit -qm seed', { cwd: path });
    return path;
}

/** The row the sidebar marks current. */
function activeRow(page: Page) {
    return page.locator('.project-item-wrapper.active .project-item');
}

/** Wait until the server has a project for `path`, and return it. */
async function waitForProject(page: Page, path: string): Promise<any> {
    let found: any = null;
    await expect(async () => {
        found = (await apiGetJson<any[]>(page, '/api/projects')).find((p: any) => p.path === path);
        expect(found).toBeTruthy();
    }).toPass({ timeout: 30000 });
    return found;
}

test.describe('Add project navigation', () => {
    /** Project paths this test created; their rows are deleted after it. */
    let created: string[] = [];

    test.beforeEach(async ({ page }) => {
        // gitRepo creates SRC_ROOT along with the repo.
        mkdirSync(CLONE_PARENT, { recursive: true });
        await gotoProject(page);
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test.afterEach(async ({ page }) => {
        const list = await apiGetJson<any[]>(page, '/api/projects').catch(() => []);
        for (const path of created) {
            const row = (list || []).find((p: any) => p.path === path);
            if (row) await csrfFetch(page, `/api/projects/${row.id}`, { method: 'DELETE' });
        }
        created = [];
        rmSync(SCRATCH, { recursive: true, force: true });
    });

    test('adding a folder lands on its board, not on the screen the previous project held', async ({ page }) => {
        const home = await getProjectId(page);
        await page.locator(`a.project-item[href$="project=${home}"]`).click();
        await page.click('.explorer-btn');
        await expect.poll(() => page.url()).toContain('view=explorer');
        await expect(page.locator('.explorer-view')).toBeVisible();

        const dir = gitRepo(`${SRC_ROOT}/folder-${Date.now()}`);
        created.push(dir);
        await page.click('.add-project-btn');
        const pathInput = page.locator('.path-input');
        await pathInput.fill(dir);
        await pathInput.press('Enter');
        await expect(page.locator('.git-indicator')).toBeVisible();
        await page.click('.btn-primary:has-text("Select")');

        const proj = await waitForProject(page, dir);

        // Its address names it, and nothing the previous project was showing.
        await expect.poll(() => page.url(), { timeout: 15000 }).toContain(`project=${proj.id}`);
        expect(page.url()).not.toContain('view=');
        expect(page.url()).not.toContain('file=');
        expect(page.url()).not.toContain('task=');

        await expect(activeRow(page)).toContainText(dir.split('/').pop() as string);
        await expect(page.locator('.board')).toBeVisible();
        await expect(page.locator('.explorer-view')).toHaveCount(0);

        // Back returns to the previous project, on the screen it was showing.
        await page.goBack();
        await expect.poll(() => page.url()).toContain(`project=${home}`);
        expect(page.url()).toContain('view=explorer');
    });

    test('a clone navigates to the project while it is still cloning', async ({ page }) => {
        const home = await getProjectId(page);
        const src = gitRepo(`${SRC_ROOT}/repo-${Date.now()}`);
        const name = src.split('/').pop() as string;
        const target = `${CLONE_PARENT}/${name}`;
        created.push(target);

        // Hold the clone request open so the assertion below cannot race the clone answering.
        await page.route('**/api/projects/clone', async (route) => {
            await new Promise(resolve => setTimeout(resolve, 1500));
            await route.continue();
        });

        await page.click('.add-project-btn');
        // The clone lands in the directory the picker is showing, so put it somewhere known first.
        const pathInput = page.locator('.path-input');
        await pathInput.fill(CLONE_PARENT);
        await pathInput.press('Enter');
        await expect(page.locator('.current-path')).toContainText(CLONE_PARENT);
        await page.locator('.tab', { hasText: 'Clone from Git' }).click();
        await page.locator('.clone-url-input').fill(`file://${src}`);
        await page.click('.btn-primary:has-text("Clone")');

        // While the clone is in flight: the new project is the one on screen, showing its progress.
        await expect(activeRow(page)).toContainText(name);
        await expect(activeRow(page).locator('[role="progressbar"]')).toBeVisible();
        await expect.poll(() => page.url()).toContain('project=clone-pending-');
        await expect(page.locator('.board')).toBeVisible();

        // The real project takes that address, and the row stops being a placeholder.
        const proj = await waitForProject(page, target);
        await expect.poll(() => page.url(), { timeout: 15000 }).toContain(`project=${proj.id}`);
        expect(page.url()).not.toContain('clone-pending-');
        await expect(activeRow(page)).toContainText(name);
        await expect(activeRow(page).locator('[role="progressbar"]')).toHaveCount(0);

        // Back goes straight to the previous project: the placeholder never became a history entry.
        await page.goBack();
        await expect.poll(() => page.url()).toContain(`project=${home}`);
    });
});
