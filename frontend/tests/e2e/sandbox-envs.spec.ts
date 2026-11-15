import { test, expect, type Page } from '@playwright/test';
import { apiGetJson, clearBoardExcept, clearTasksAfterEach, csrfFetch, gotoProject, isolatedProject, ownProjectId } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();
// The tasks this file's project holds are released between tests: a live task owns a container charged
// against the executor's one memory budget, which every worker on this lane shares.
// `docs/sandbox-runtime.md → The memory budget`.
clearTasksAfterEach();

/** The Sandbox Configuration dialog. `[role="dialog"]` is not a unique locator here; this class is its own. */
const form = (page: Page) => page.locator('.sandbox-config-modal');

async function readProject(page: Page): Promise<any> {
  return await apiGetJson<any>(page, `/api/projects/${ownProjectId()}`);
}

/**
 * Opens the Sandbox Configuration form for this file's own project with sandboxing on, writing `fields` with
 * the same request that switches it on.
 *
 * A sandbox setting cannot be written while a task of the project runs, and the form is read-only then too, so
 * the project is emptied first: the two tasks `isolatedProject()` seeds each auto-start a turn, and a task of
 * this project is the only thing that can refuse the write. Emptying is also what releases their containers.
 */
async function prepareSandboxProject(page: Page, fields: Record<string, unknown> = {}): Promise<void> {
  await gotoProject(page);
  const projectId = ownProjectId();
  await clearBoardExcept(page, projectId, []);
  const written = await csrfFetch(page, `/api/projects/${projectId}`, {
    method: 'PUT',
    body: { sandbox_enabled: true, sandbox_network_isolation: false, ...fields },
  });
  if (!written.ok) {
    throw new Error(
      `sandbox settings refused on the project this file owns (${written.status} ${JSON.stringify(written.data)}); ` +
      'nothing but this file starts a task here, so a refusal is the behaviour under test, not the lane',
    );
  }
  await expect.poll(() => readProject(page), { timeout: 5000 }).toMatchObject({ sandbox_enabled: true });
  await page.reload();
  await expect(page.locator('.sandbox-config-btn')).toBeVisible({ timeout: 10000 });
}

/**
 * Returns this file's own project the way it found it: no tasks, sandboxing off, no variables saved. The
 * per-worker project outlives the file, and the explorer reads `sandbox_enabled` to choose which root `/api/fs`
 * answers, so a project left sandboxed is another file's `path outside allowed project directory`.
 */
async function leaveProjectAsFound(page: Page): Promise<void> {
  const projectId = ownProjectId();
  // Emptied here rather than left to `clearTasksAfterEach`: the write below is refused while a task runs, and
  // this says so instead of trusting the order the two hooks run in.
  await clearBoardExcept(page, projectId, []);
  const left = await csrfFetch(page, `/api/projects/${projectId}`, {
    method: 'PUT',
    body: { sandbox_enabled: false, sandbox_envs: [] },
  });
  if (!left.ok) {
    throw new Error(
      `could not return this file's project to unsandboxed (${left.status} ${JSON.stringify(left.data)}); ` +
      'disabling sandboxing needs the lane to start the server with DANGER_ZONE=1',
    );
  }
}

/** Opens the collapsible subsection whose heading is `heading`, and waits for its add row to be built. */
async function openSection(page: Page, heading: string, addRow: string): Promise<void> {
  await page.locator('.config-row-expandable').filter({ hasText: heading }).click();
  await expect(page.locator(addRow)).toBeVisible({ timeout: 5000 });
}

test.describe.serial('Sandbox Environment Variables', () => {
  test.beforeEach(async ({ page }) => {
    await prepareSandboxProject(page, { sandbox_envs: [] });
  });

  test.afterEach(async ({ page }) => leaveProjectAsFound(page));

  test('saves an added variable to the server', async ({ page }) => {
    await page.click('.sandbox-config-btn');
    await expect(form(page)).toBeVisible({ timeout: 5000 });
    // The editable form and the tasks-in-progress read-out are the two branches of this dialog, so the
    // controls below only exist when this one is on screen.
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    await openSection(page, 'Environment Variables', '.env-add-row');
    await page.locator('.env-input-key').fill('DB_HOST');
    await page.locator('.env-input-value').fill('localhost');
    await page.locator('.btn-add-env').click();
    await expect(page.locator('.env-item')).toHaveCount(1);

    await form(page).locator('.modal-footer .btn-primary').click();
    await expect(form(page)).toBeHidden();

    const saved = await readProject(page);
    expect(saved?.sandbox_envs).toHaveLength(1);
    expect(saved.sandbox_envs[0]).toMatchObject({ key: 'DB_HOST', value: 'localhost' });
  });

  test('lists the variables the server holds', async ({ page }) => {
    await csrfFetch(page, `/api/projects/${ownProjectId()}`, {
      method: 'PUT',
      body: { sandbox_envs: [
        { key: 'UI_TEST_KEY', value: 'ui_test_value' },
        { key: 'SECOND_KEY', value: 'second_value' },
      ] },
    });
    await page.reload();
    await expect(page.locator('.sandbox-config-btn')).toBeVisible({ timeout: 10000 });

    await page.click('.sandbox-config-btn');
    await expect(form(page)).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });
    await openSection(page, 'Environment Variables', '.env-add-row');

    await expect(page.locator('.env-item')).toHaveCount(2);
    await expect(page.locator('.env-key').first()).toHaveText('UI_TEST_KEY');
    await expect(page.locator('.env-value').first()).toHaveText('ui_test_value');

    await form(page).locator('.close-btn').click();
    await expect(form(page)).toBeHidden();
  });
});
