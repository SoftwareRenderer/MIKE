import { test, expect, type Page } from '@playwright/test';
import { apiGetJson, clearBoardExcept, clearTasksAfterEach, csrfFetch, gotoProject, isolatedProject, ownProjectId } from './helpers';

// One screen, one decision: what the Sandbox Configuration dialog offers, what it saves, and what it reads
// back — all of it hanging off the one column that says where a task runs, `project.sandbox_enabled`.
// Environment variables, bind mounts and forwarded ports are three sections of that one dialog, so they are
// one file: each pays the same setup (empty the project this file owns, switch sandboxing on, open the dialog)
// and each leaves the project as it found it.
//
// What the saved row then means to a container is `docs/sandbox-runtime.md → The container environment` and
// its Go tests (`backend/sandbox/containerenv_test.go`, `hostenv_test.go`); nothing here measures it, because
// nothing here starts a task — and a task, a terminal or a build is the only thing that starts a container.
// The draft's own rules (what may be added, what one Save sends, when the form is read-only) live in
// `src/lib/components/__tests__/sandbox-config-{draft,payload,ports,pair-rows}.unit.svelte.ts`. What is left
// for a browser is the row that only the server can fill, and the phone block at the bottom.

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();
// The tasks this file's project holds are released between tests: a live task owns a container charged
// against the executor's one memory budget, which every worker on this lane shares.
// `docs/sandbox-runtime.md → The memory budget`.
clearTasksAfterEach();

/** The Sandbox Configuration dialog. `[role="dialog"]` is not a unique locator here; this class is its own. */
const form = (page: Page) => page.locator('.sandbox-config-modal');

/** The row Forward Ports owns — a node the dialog only builds while the container can be reached at all. */
const portsRow = (page: Page) => page.locator('.config-row-subsection');

/** The host and container path fields of the mount form. */
const hostInput = (page: Page) => page.locator('.mount-input[aria-label="Host path"]');
const containerInput = (page: Page) => page.locator('.mount-input[aria-label="Container path"]');

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
  await page.reload();
  await expect(page.locator('.sandbox-config-btn')).toBeVisible({ timeout: 10000 });
}

/**
 * Returns this file's own project the way it found it: no tasks, sandboxing off, nothing saved. The
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
    body: {
      sandbox_enabled: false,
      sandbox_envs: [],
      sandbox_mounts: [],
      sandbox_forward_ports: [],
    },
  });
  if (!left.ok) {
    throw new Error(
      `could not return this file's project to unsandboxed (${left.status} ${JSON.stringify(left.data)}); ` +
      'disabling sandboxing needs the lane to start the server with DANGER_ZONE=1',
    );
  }
}

/** Writes the sandbox fields and brings the dialog's own view of them back up. */
async function writeAndReload(page: Page, fields: Record<string, unknown>): Promise<void> {
  const written = await csrfFetch(page, `/api/projects/${ownProjectId()}`, { method: 'PUT', body: fields });
  if (!written.ok) {
    throw new Error(`sandbox settings refused (${written.status} ${JSON.stringify(written.data)})`);
  }
  await page.reload();
  await expect(page.locator('.sandbox-config-btn')).toBeVisible({ timeout: 10000 });
}

/** Opens the collapsible subsection whose heading is `heading`, and waits for its add row to be built. */
async function openSection(page: Page, heading: string, addRow: string): Promise<void> {
  await page.locator('.config-row-expandable').filter({ hasText: heading }).click();
  await expect(page.locator(addRow)).toBeVisible({ timeout: 5000 });
}

/** Opens the dialog and waits for the editable form — the read-only tasks-in-progress read-out is the other
 *  branch of this dialog, so the controls below only exist when this one is on screen. */
async function openForm(page: Page): Promise<void> {
  await page.click('.sandbox-config-btn');
  await expect(form(page)).toBeVisible({ timeout: 5000 });
  await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });
}

test.describe.serial('Sandbox Configuration form', () => {
  test.beforeEach(async ({ page }) => {
    await prepareSandboxProject(page, {
      sandbox_envs: [],
      sandbox_mounts: [],
      sandbox_forward_ports: [],
    });
  });

  test.afterEach(async ({ page }) => leaveProjectAsFound(page));

  test('saves an added variable to the server', async ({ page }) => {
    await openForm(page);
    await openSection(page, 'Environment Variables', '.env-add-row');
    await page.locator('.env-input-key').fill('DB_HOST');
    await page.locator('.env-input-value').fill('localhost');
    await page.locator('.btn-add-env').click();
    await expect(page.locator('.env-item')).toHaveCount(1);

    await form(page).locator('.modal-footer .btn-primary').click();
    await expect(form(page)).toBeHidden();

    const saved = await apiGetJson<any>(page, `/api/projects/${ownProjectId()}`);
    expect(saved?.sandbox_envs).toHaveLength(1);
    expect(saved.sandbox_envs[0]).toMatchObject({ key: 'DB_HOST', value: 'localhost' });
  });

  test('lists the variables the server holds', async ({ page }) => {
    await writeAndReload(page, {
      sandbox_envs: [
        { key: 'UI_TEST_KEY', value: 'ui_test_value' },
        { key: 'SECOND_KEY', value: 'second_value' },
      ],
    });

    await openForm(page);
    await openSection(page, 'Environment Variables', '.env-add-row');

    await expect(page.locator('.env-item')).toHaveCount(2);
    await expect(page.locator('.env-key').first()).toHaveText('UI_TEST_KEY');
    await expect(page.locator('.env-value').first()).toHaveText('ui_test_value');

    await form(page).locator('.close-btn').click();
    await expect(form(page)).toBeHidden();
  });

  test('offers port forwarding while the container can be reached', async ({ page }) => {
    await openForm(page);

    // Nested under Network Isolation, which this file's own project has switched off.
    await expect(portsRow(page)).toBeVisible({ timeout: 5000 });
    await expect(portsRow(page).locator('.config-label')).toHaveText('Forward Ports');

    // The section starts collapsed when no port is set; its add row is only built once it is open.
    await portsRow(page).click();
    await expect(page.locator('.port-add-row')).toBeVisible({ timeout: 5000 });
    await expect(portsRow(page).locator('.config-desc')).toContainText('auto-assigned');

    await form(page).locator('.close-btn').click();
    await expect(form(page)).toBeHidden();
  });

  test('hides port forwarding while the network is isolated', async ({ page }) => {
    await writeAndReload(page, { sandbox_network_isolation: true });

    await openForm(page);
    await expect(portsRow(page)).toHaveCount(0);
  });

  test('hides port forwarding when sandboxing is off', async ({ page }) => {
    await writeAndReload(page, { sandbox_enabled: false });

    await openForm(page);
    await expect(portsRow(page)).toHaveCount(0);
  });

  test('saves a mount with the mode it was chosen with, and lists what the server holds', async ({ page }) => {
    await openForm(page);
    await openSection(page, 'Custom Mounts', '.mount-add-row');
    await hostInput(page).fill('/etc/config');
    await containerInput(page).fill('/var/lib/config');
    await page.locator('.radio-group-small .radio-label').filter({ hasText: 'ro' }).click();
    await page.locator('.btn-add-mount').click();

    // The mode chosen for the row survives the add: a row that fell back to `rw` on the way into the list
    // would hand the container a write nobody asked for.
    await expect(page.locator('.mount-item')).toHaveCount(1);
    await expect(page.locator('.mount-item .sandbox-mode-ro')).toHaveText('ro');

    await form(page).locator('.modal-footer .btn-primary').click();
    await expect(form(page)).toBeHidden();

    // The row the server holds is what the next container mounts, so both paths and the mode have to come
    // back from it rather than from what was typed.
    await page.reload();
    await expect(page.locator('.sandbox-config-btn')).toBeVisible({ timeout: 10000 });
    await openForm(page);
    await openSection(page, 'Custom Mounts', '.mount-add-row');
    await expect(page.locator('.mount-item')).toHaveCount(1);
    await expect(page.locator('.mount-item .mount-path')).toHaveText('/etc/config');
    await expect(page.locator('.mount-item .mount-container')).toHaveText('/var/lib/config');
    await expect(page.locator('.mount-item .sandbox-mode-ro')).toHaveText('ro');
  });
});

// The add row needs 434px on one line and a portrait screen has less; below that the form stacks,
// and this checks the stacked form.
test.describe('Sandbox Configuration form on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

  test.beforeEach(async ({ page }) => prepareSandboxProject(page, { sandbox_mounts: [] }));
  test.afterEach(async ({ page }) => leaveProjectAsFound(page));

  /** Anything of the mount form that sits outside the screen, by name. */
  const outside = (page: import('@playwright/test').Page) =>
    page.evaluate(() =>
      [...document.querySelectorAll('.mount-add-row > *, .mount-add-row .add-row-actions > *')]
        .filter(el => getComputedStyle(el).display !== 'none')
        .filter(el => el.getBoundingClientRect().right > window.innerWidth + 0.5)
        .map(el => `${el.className} right=${Math.round(el.getBoundingClientRect().right)} of ${window.innerWidth}`),
    );

  test('holds the whole mount form on the screen, with a thumb-sized target on each control', async ({ page }) => {
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });
    await page.locator('.config-row-expandable .config-label').filter({ hasText: 'Custom Mounts' }).click();
    await expect(page.locator('.mount-add-row')).toBeVisible({ timeout: 5000 });

    await expect(outside(page)).resolves.toEqual([]);
    // The modal pans sideways without ever hinting it can; nothing may need that to be reached.
    const sideways = await page.locator('.modal-body').evaluate(el => el.scrollWidth - el.clientWidth);
    expect(sideways, 'the modal can be panned sideways to reach a control').toBeLessThanOrEqual(0);

    // A placeholder is gone the moment a path is typed into it, so each field needs a visible name.
    await expect(page.locator('.mount-add-row .field-label')).toHaveText(['Host path', 'Container path']);

    const tooSmall = await page.evaluate(() =>
      ['.btn-add-mount', '.btn-browse-mount', '.radio-group-small .radio-label']
        .map(s => ({ s, r: document.querySelector<HTMLElement>(s)!.getBoundingClientRect() }))
        .filter(k => k.r.width < 44 || k.r.height < 44)
        .map(k => `${k.s} ${Math.round(k.r.width)}x${Math.round(k.r.height)}`),
    );
    expect(tooSmall, 'a control a thumb cannot aim at').toEqual([]);

    await page.locator('.mount-input[aria-label="Host path"]').fill('/srv/datasets/2026/quarterly');
    await page.locator('.mount-input[aria-label="Container path"]').fill('/mnt/datasets');
    await page.locator('.btn-add-mount').tap();
    await expect(page.locator('.mount-item')).toHaveCount(1);

    // A saved path must be readable without the hover tooltip that is its only other copy.
    const clipped = await page.evaluate(() =>
      ['.mount-path', '.mount-container']
        .map(s => document.querySelector<HTMLElement>(`.mount-item ${s}`)!)
        .filter(el => el.scrollWidth > el.clientWidth + 1)
        .map(el => `${el.className}: ${el.textContent}`),
    );
    expect(clipped, 'a saved path that is only partly on screen').toEqual([]);

    // Opening a row brings the form below the list into view.
    await page.locator('.mount-item').first().tap();
    await expect(page.locator('.mount-input[aria-label="Container path"]')).toHaveValue('/mnt/datasets');
    await expect(outside(page)).resolves.toEqual([]);
    const cancel = await page.locator('.btn-cancel-edit').boundingBox();
    expect(cancel!.height).toBeGreaterThanOrEqual(44);

    await page.locator('.btn-remove-mount').first().tap();
    await expect(page.locator('.mount-item')).toHaveCount(0);

    // Escape is the dialog's, not the row's.
    await page.keyboard.press('Escape');
    await expect(page.locator('.config-options')).not.toBeVisible({ timeout: 5000 });
  });
});
