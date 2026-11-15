import { test, expect, type Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { apiGetJson, clearBoardExcept, clearTasksAfterEach, csrfFetch, gotoProject, isolatedProject, ownProjectId, ownProjectPath, requireSandbox } from './helpers';

// Own project: enabling sandboxing on a project is refused (409) while tasks are running on it, so on the
// shared project this test had no state it could trust. Its own project has only its own tasks.
isolatedProject();
// The tasks this file's project holds are released between tests: a live task owns a container charged
// against the executor's one memory budget, which every worker on this lane shares.
// `docs/sandbox-runtime.md → The memory budget`.
clearTasksAfterEach();

/** The Sandbox Configuration dialog and the path picker it opens. `[role="dialog"]` is not a unique locator. */
const form = (page: Page) => page.locator('.sandbox-config-modal');
const picker = (page: Page) => page.locator('.path-browser');

function projectDir(): string {
  const dir = ownProjectPath();
  if (!dir) throw new Error('this spec needs the project isolatedProject() gives its worker');
  return dir;
}

/**
 * Opens the Sandbox Configuration form for this file's own project with sandboxing on, and leaves a Dockerfile
 * in the project directory for the picker to find.
 *
 * A sandbox setting cannot be written while a task of the project runs, and the form is read-only then too, so
 * the project is emptied first: the two tasks `isolatedProject()` seeds each auto-start a turn, and a task of
 * this project is the only thing that can refuse the write. Emptying is also what releases their containers.
 */
async function prepareSandboxProject(page: Page): Promise<void> {
  await gotoProject(page);
  const projectId = ownProjectId();
  fs.writeFileSync(path.join(projectDir(), 'Dockerfile'), 'FROM alpine:latest\n');
  await clearBoardExcept(page, projectId, []);
  const written = await csrfFetch(page, `/api/projects/${projectId}`, {
    method: 'PUT',
    body: { sandbox_enabled: true, sandbox_network_isolation: false },
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

test.describe.serial('Sandbox Dockerfile Build', () => {
  test.beforeEach(async ({ page }) => {
    // Build asks this host to build an image, so this spec rests on a working container store.
    requireSandbox();
    await prepareSandboxProject(page);
  });

  test.afterEach(async ({ page }) => {
    fs.rmSync(path.join(projectDir(), 'Dockerfile'), { force: true });
    // Returns the project the way it was found: no tasks, sandboxing off, no Dockerfile remembered. The
    // per-worker project outlives the file, and the explorer reads `sandbox_enabled` to choose which root
    // `/api/fs` answers, so a project left sandboxed is another file's `path outside allowed project directory`.
    const projectId = ownProjectId();
    await clearBoardExcept(page, projectId, []);
    const left = await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: false, sandbox_dockerfile_path: '' },
    });
    if (!left.ok) {
      throw new Error(
        `could not return this file's project to unsandboxed (${left.status} ${JSON.stringify(left.data)}); ` +
        'disabling sandboxing needs the lane to start the server with DANGER_ZONE=1',
      );
    }
  });

  test('stores the Dockerfile the Build button was clicked with, though it was never saved', async ({ page }) => {
    await page.click('.sandbox-config-btn');
    await expect(form(page)).toBeVisible({ timeout: 5000 });
    // The editable form and the tasks-in-progress read-out are the two branches of this dialog, so the
    // controls below only exist when this one is on screen.
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    await page.locator('.radio-label').filter({ hasText: 'Dockerfile' }).click();

    // The placeholder and the path are two different nodes of this row, so an assertion naming only the
    // path could not fail: before a choice is made the path node is not built at all.
    await expect(page.locator('.dockerfile-path-placeholder')).toHaveText('No Dockerfile selected');

    await page.locator('.btn-browse').click();
    await expect(picker(page)).toBeVisible({ timeout: 5000 });

    const dockerfileEntry = picker(page).locator('.file-entry').filter({ hasText: 'Dockerfile' });
    // The picker lists a real directory; under a full-suite run the server is answering
    // several of these at once and 5s was regularly too tight.
    await expect(dockerfileEntry).toBeVisible({ timeout: 20000 });
    await dockerfileEntry.click();

    await picker(page).locator('.btn-primary', { hasText: 'Select' }).click();
    await expect(picker(page)).toBeHidden();

    await expect(page.locator('.dockerfile-path-text')).toHaveText(/Dockerfile$/);
    const buildBtn = page.locator('.btn-build');
    await expect(buildBtn).toBeVisible({ timeout: 5000 });

    await buildBtn.click();

    await expect.poll(async () => {
      const row = await apiGetJson<any>(page, `/api/projects/${ownProjectId()}`);
      return row?.sandbox_dockerfile_path;
    }, { timeout: 5000 }).toMatch(/Dockerfile$/);
  });
});
