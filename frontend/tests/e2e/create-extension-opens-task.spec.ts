import { test, expect, type Page } from '@playwright/test';
import { rmSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { apiGetJson, csrfFetch, openSettingsTab, signedInPage } from './helpers';
import { DOCKER_DEFAULT_CAPABILITIES } from '../../src/lib/components/SandboxConfigModal/capabilities';

// ============================================================
// Create extension — the sandbox recommendation, and where the create lands
// ============================================================
//
// The one flow that makes a repository, a project and a started task out of a dialog. Two things only a
// browser proves: the recommendation stops a valid form before a byte is written, and the create lands on
// the first task rather than on a board the person must find the card on. The dialog's steps and the
// fields it sends are `ConfigModal/__tests__/extension-create-handoff.unit.svelte.ts`; this file is the
// address bar, the project row and the repository the server keeps.
//
// Owns what it makes: deleting the project takes its task and container, and the repository directory is
// removed here because it outlives a development project's row (docs/extension-authoring.md).

const TS = Date.now();
const EXT_NAME = `E2E review clock ${TS}`;
/** The slug the server derives once and hands to the row, the directory and the project name. */
const SLUG = `e2e-review-clock-${TS}`;
const DESCRIPTION = 'Show how long each task has been sitting in Review.';

const HERE = dirname(fileURLToPath(import.meta.url));
const EXTENSIONS_DIR = process.env.MIKE_EXTENSIONS_DIR
  || resolve(HERE, '..', '..', '..', 'extensions');
/** `extensions.DevNamespace`, where a development repository lives and where it stays. */
const REPO = join(EXTENSIONS_DIR, '_dev', SLUG);

const dialog = (page: Page) => page.locator('.extension-create-modal');
const networkSwitch = (page: Page) => dialog(page).getByRole('switch', { name: /reach the network/ });
const capabilitiesSwitch = (page: Page) => dialog(page).getByRole('switch', { name: /Docker/ });
const createButton = (page: Page) => dialog(page).getByRole('button', { name: 'Create', exact: true });

async function openForm(page: Page) {
  await page.goto('/');
  await page.waitForSelector('.project-item', { timeout: 10000 });
  await openSettingsTab(page, 'Extensions');
  await page.waitForSelector('.extensions-tab', { timeout: 5000 });
  await page.locator('button:has-text("Create extension")').click();
  await dialog(page).waitFor({ timeout: 5000 });
  await page.fill('input[name="name"]', EXT_NAME);
  await page.fill('textarea[name="description"]', DESCRIPTION);
}

async function projectByName(page: Page, name: string): Promise<any | undefined> {
  const rows = await apiGetJson<any[]>(page, '/api/projects');
  return rows.find((p) => p.name === name);
}

test.describe.serial('Create extension', () => {
  test.afterAll(async ({ browser }) => {
    const { context, page } = await signedInPage(browser, { landing: null });
    try {
      // Read back rather than reuse: the project exists as soon as the create answers, which can be
      // before this file's assertions get a turn.
      const mine = await projectByName(page, SLUG).catch(() => undefined);
      if (mine) await csrfFetch(page, `/api/projects/${mine.id}`, { method: 'DELETE' });
    } finally {
      await context.close();
    }
    rmSync(REPO, { recursive: true, force: true });
  });

  test('a valid form stops at the recommendation, and nothing has been made', async ({ page }) => {
    await openForm(page);

    await createButton(page).click();

    await expect(networkSwitch(page)).toHaveAttribute('aria-checked', 'false');
    await expect(capabilitiesSwitch(page)).toHaveAttribute('aria-checked', 'false');
    // The recommendations are offered, not assumed.
    await expect(dialog(page).getByText('Recommended')).toHaveCount(2);
    expect(await projectByName(page, SLUG)).toBeUndefined();
  });

  test('what the person allowed reaches the project row, and the create lands on the task', async ({ page }) => {
    await openForm(page);
    await createButton(page).click();
    await expect(networkSwitch(page)).toHaveAttribute('aria-checked', 'false');

    await networkSwitch(page).click();
    await capabilitiesSwitch(page).click();
    await expect(networkSwitch(page)).toHaveAttribute('aria-checked', 'true');
    await expect(capabilitiesSwitch(page)).toHaveAttribute('aria-checked', 'true');

    await createButton(page).click();

    // The board first, then the task: the detail is on screen, not the column the card sits in.
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 30000 });
    const url = new URL(page.url());
    const projectId = url.searchParams.get('project') || '';
    expect(url.searchParams.get('task')).toBeTruthy();

    const project = await projectByName(page, SLUG);
    expect(project, 'the create made no project').toBeTruthy();
    expect(project.id).toBe(projectId);
    expect(project.sandbox_enabled).toBe(true);
    expect(project.sandbox_network_isolation).toBe(false);
    expect(project.sandbox_capabilities).toEqual([...DOCKER_DEFAULT_CAPABILITIES]);

    // The card that opened is the create's own task, judged by row rather than by count.
    const board = await apiGetJson<{ tasks: any[] }>(page, `/api/projects/${projectId}/board`);
    expect(board.tasks).toHaveLength(1);
    expect(board.tasks[0].id).toBe(url.searchParams.get('task'));
    expect(board.tasks[0].title).toBe(EXT_NAME);

    // The board is the entry Back returns to, because the landing and the opening were two pushes.
    await page.goBack();
    await expect(page.locator('.task-detail-page')).toBeHidden({ timeout: 10000 });
    await expect(page.locator('.board')).toBeVisible();
  });
});
