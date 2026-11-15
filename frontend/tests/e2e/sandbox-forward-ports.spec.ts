import { test, expect, type Page } from '@playwright/test';
import { clearBoardExcept, clearTasksAfterEach, csrfFetch, gotoProject, isolatedProject, ownProjectId } from './helpers';

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

/**
 * Opens the Sandbox Configuration form for this file's own project with sandboxing on, network isolation off
 * and no saved ports.
 *
 * A sandbox setting cannot be written while a task of the project runs, and the form is read-only then too, so
 * the project is emptied first: the two tasks `isolatedProject()` seeds each auto-start a turn, and a task of
 * this project is the only thing that can refuse the write. Emptying is also what releases their containers.
 */
async function prepareSandboxProject(page: Page): Promise<void> {
  await gotoProject(page);
  const projectId = ownProjectId();
  await clearBoardExcept(page, projectId, []);
  const written = await csrfFetch(page, `/api/projects/${projectId}`, {
    method: 'PUT',
    body: { sandbox_enabled: true, sandbox_network_isolation: false, sandbox_forward_ports: [] },
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
 * Returns this file's own project the way it found it: no tasks, sandboxing off, no ports saved. The per-worker
 * project outlives the file, and the explorer reads `sandbox_enabled` to choose which root `/api/fs` answers,
 * so a project left sandboxed is another file's `path outside allowed project directory`.
 */
async function leaveProjectAsFound(page: Page): Promise<void> {
  const projectId = ownProjectId();
  // Emptied here rather than left to `clearTasksAfterEach`: the write below is refused while a task runs, and
  // this says so instead of trusting the order the two hooks run in.
  await clearBoardExcept(page, projectId, []);
  const left = await csrfFetch(page, `/api/projects/${projectId}`, {
    method: 'PUT',
    body: { sandbox_enabled: false, sandbox_forward_ports: [] },
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

test.describe.serial('Sandbox Forward Ports', () => {
  test.beforeEach(async ({ page }) => prepareSandboxProject(page));
  test.afterEach(async ({ page }) => leaveProjectAsFound(page));

  test('offers port forwarding while the container can be reached', async ({ page }) => {
    await page.click('.sandbox-config-btn');
    await expect(form(page)).toBeVisible({ timeout: 5000 });
    // The editable form and the tasks-in-progress read-out are the two branches of this dialog, so the
    // controls below only exist when this one is on screen.
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

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

    await page.click('.sandbox-config-btn');
    await expect(form(page)).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });
    await expect(portsRow(page)).toHaveCount(0);
  });

  test('hides port forwarding when sandboxing is off', async ({ page }) => {
    await writeAndReload(page, { sandbox_enabled: false });

    await page.click('.sandbox-config-btn');
    await expect(form(page)).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });
    await expect(portsRow(page)).toHaveCount(0);
  });
});
