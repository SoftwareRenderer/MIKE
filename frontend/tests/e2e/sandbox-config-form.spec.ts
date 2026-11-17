import { test, expect, type Page } from '@playwright/test';
import { clearBoardExcept, clearTasksAfterEach, csrfFetch, gotoProject, isolatedProject, ownProjectId } from './helpers';

// The Sandbox Configuration dialog on a phone: the add row needs 434px on one line and a portrait screen has
// less, so below that width the mount form stacks — and this measures the stacked form, its thumb-sized
// targets and its un-clippable paths. Everything else about this dialog is decided where it can be staged
// more cheaply: the draft's own rules (what may be added, what one Save sends, which sections exist at all,
// when the form is read-only) in
// `src/lib/components/__tests__/sandbox-config-{draft,payload,ports,pair-rows}.unit.svelte.ts`; the request
// that Save makes in `src/lib/api/__tests__/projects.unit.ts`; and every `sandbox_*` list's trip into the row
// and back, a mount's mode included, by `TestProjectSandboxListSettingsRoundTrip`. What the saved row then
// means to a container is `docs/sandbox-runtime.md → The container environment` and its Go tests
// (`backend/sandbox/containerenv_test.go`, `hostenv_test.go`) — nothing here starts a task, and a task, a
// terminal or a build is the only thing that starts a container.

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();
// The tasks this file's project holds are released between tests: a live task owns a container charged
// against the executor's one memory budget, which every worker on this lane shares.
// `docs/sandbox-runtime.md → The memory budget`.
clearTasksAfterEach();

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
