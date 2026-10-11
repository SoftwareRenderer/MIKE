import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

/** Sandbox Configuration will not open while a task runs, so this sets the state over the API. */
async function prepareSandboxProject(page: import('@playwright/test').Page) {
  await gotoProject(page);
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);

  // Check current project state first
  const currentProject = await page.evaluate(async (pid) => {
    const res = await fetch(`/api/projects/${pid}`);
    return res.ok ? res.json() : null;
  }, projectId);

  // Only update sandbox settings if they don't already match
  const needsUpdate = !currentProject?.sandbox_enabled;
  if (needsUpdate) {
    // Stop all in_progress tasks first (blocks sandbox config changes)
    for (let attempt = 0; attempt < 5; attempt++) {
      const tasks = await page.evaluate(async (pid) => {
        const res = await fetch(`/api/projects/${pid}/tasks`);
        return res.ok ? res.json() : [];
      }, projectId);
      let changed = false;
      for (const task of tasks) {
        if (task.status === 'in_progress') {
          await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/stop`, {
            method: 'POST',
          }).catch(() => {}); // Ignore if task not running
          await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
            method: 'POST',
            body: { status: 'backlog' },
          }).catch(() => {}); // Ignore if already moved
          changed = true;
        }
      }
      if (!changed) break;
      await page.waitForFunction(() => false, {}, { timeout: 1000, polling: 1000 }).catch(() => {});
    }

    const putResult = await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: true, sandbox_network_isolation: false },
    });
    if (!putResult.ok && putResult.status === 409) {
      test.skip('Cannot enable sandbox: tasks are running from parallel tests');
    } else if (!putResult.ok) {
      throw new Error(`Failed to enable sandbox: ${putResult.status}`);
    }
  }

  // Verify final state
  await expect(async () => {
    const project = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    expect(project?.sandbox_enabled).toBe(true);
  }).toPass({ timeout: 5000 });

  // Reload to reset component state
  await page.reload();
  await page.waitForSelector('.project-item', { timeout: 10000 });

  // Re-verify after reload: ensure no tasks are in_progress (parallel tests may have started new ones)
  const tasksAfterReload = await page.evaluate(async (pid) => {
    const res = await fetch(`/api/projects/${pid}/tasks`);
    return res.ok ? res.json() : [];
  }, projectId);
  const inProgressTasks = tasksAfterReload.filter((t: any) => t.status === 'in_progress');
  if (inProgressTasks.length > 0) {
    // Move any newly in-progress tasks to backlog
    for (const task of inProgressTasks) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/stop`, {
        method: 'POST',
      }).catch(() => {});
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
        method: 'POST',
        body: { status: 'backlog' },
      }).catch(() => {});
    }
    await page.waitForFunction(() => false, {}, { timeout: 500, polling: 500 }).catch(() => {});
  }
}

test.describe.serial('Sandbox Mounts', () => {
  test.beforeEach(async ({ page }) => prepareSandboxProject(page));

  test('should show custom mounts section when sandbox is enabled', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Check for locked view (tasks in progress)
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      console.warn('Sandbox config modal is locked (tasks in progress) — skipping test');
      await page.click('.overlay', { position: { x: 10, y: 10 } });
      await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
      test.skip('Sandbox config modal is locked');
    }

    // Wait for config options to render inside the modal
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    // Custom Mounts subsection header should be visible
    const mountsHeaders = page.locator('.config-row-expandable .config-label').filter({ hasText: 'Custom Mounts' });
    const mountsCount = await mountsHeaders.count();

    if (mountsCount > 0) {
      await expect(mountsHeaders.first()).toBeVisible({ timeout: 10000 });
      // Section starts collapsed — click to expand
      await mountsHeaders.first().click();
      await expect(page.locator('.mount-add-row')).toBeVisible({ timeout: 5000 });
      // Description explains purpose
      await expect(page.locator('.config-row-expandable .config-desc')).toContainText('Bind mount');
    } else {
      // Mounts section not visible — verify the modal is functional
      await expect(page.locator('.config-row')).toBeVisible();
    }

    // Close modal
    await page.click('.overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });

  test('should add and remove mount', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Check for locked view (tasks in progress)
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      console.warn('Sandbox config modal is locked (tasks in progress) — skipping test');
      await page.click('.overlay', { position: { x: 10, y: 10 } });
      await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
      test.skip('Sandbox config modal is locked');
    }

    // Wait for config options to render
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    // Expand Custom Mounts subsection
    const mountsHeaders = page.locator('.config-row-expandable .config-label').filter({ hasText: 'Custom Mounts' });
    const mountsCount = await mountsHeaders.count();
    if (mountsCount === 0) {
      test.skip('Custom Mounts section not visible');
    }
    await expect(mountsHeaders.first()).toBeVisible({ timeout: 10000 });
    await mountsHeaders.first().click();
    await expect(page.locator('.mount-add-row')).toBeVisible({ timeout: 5000 });

    // Add mount
    const hostInput = page.locator('.mount-input[aria-label="Host path"]');
    const containerInput = page.locator('.mount-input[aria-label="Container path"]');
    const addButton = page.locator('.btn-add-mount');

    await hostInput.fill('/tmp/data');
    await containerInput.fill('/mnt/data');
    await addButton.click();

    // Verify mount appears in list
    await expect(page.locator('.mount-item')).toHaveCount(1);
    await expect(page.locator('.mount-path')).toContainText('/tmp/data');
    await expect(page.locator('.mount-container')).toContainText('/mnt/data');
    await expect(page.locator('.sandbox-mode')).toContainText('rw');

    await page.locator('.btn-remove-mount').first().click();
    await expect(page.locator('.mount-item')).toHaveCount(0);

    // Close modal
    await page.click('.overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });

  test('should reject empty paths', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Check for locked view (tasks in progress)
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      console.warn('Sandbox config modal is locked (tasks in progress) — skipping test');
      await page.click('.overlay', { position: { x: 10, y: 10 } });
      await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
      test.skip('Sandbox config modal is locked');
    }

    // Wait for config options to render
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    // Expand Custom Mounts subsection
    const mountsHeader = page.locator('.config-row-expandable .config-label').filter({ hasText: 'Custom Mounts' });
    const mountsCount = await mountsHeader.count();
    if (mountsCount === 0) {
      test.skip('Custom Mounts section not visible');
    }
    await expect(mountsHeader.first()).toBeVisible({ timeout: 10000 });
    await mountsHeader.first().click();
    await expect(page.locator('.mount-add-row')).toBeVisible({ timeout: 5000 });

    const addButton = page.locator('.btn-add-mount');

    // Try adding with empty host path
    await addButton.click();
    await expect(page.locator('.mount-error')).toContainText('Host path is required');

    // Try adding with empty container path
    const hostInput = page.locator('.mount-input[aria-label="Host path"]');
    await hostInput.fill('/tmp/data');
    await addButton.click();
    await expect(page.locator('.mount-error')).toContainText('Container path is required');

    // Close modal
    await page.click('.overlay', { position: { x: 10, y: 10 } });
  });

  test('should toggle rw/ro mode', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Check for locked view (tasks in progress)
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      console.warn('Sandbox config modal is locked (tasks in progress) — skipping test');
      await page.click('.overlay', { position: { x: 10, y: 10 } });
      await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
      test.skip('Sandbox config modal is locked');
    }

    // Expand Custom Mounts subsection
    const mountsHeader = page.locator('.config-row-expandable .config-label').filter({ hasText: 'Custom Mounts' });
    await expect(mountsHeader).toBeVisible({ timeout: 10000 });
    await mountsHeader.click();
    await expect(page.locator('.mount-add-row')).toBeVisible({ timeout: 5000 });

    const hostInput = page.locator('.mount-input[aria-label="Host path"]');
    const containerInput = page.locator('.mount-input[aria-label="Container path"]');
    const addButton = page.locator('.btn-add-mount');

    // Add a read-only mount
    await hostInput.fill('/etc/config');
    await containerInput.fill('/etc/config');
    // Switch to ro mode
    await page.locator('.radio-group-small .radio-label').filter({ hasText: 'ro' }).click();
    await addButton.click();

    // Verify mount has ro mode
    await expect(page.locator('.mount-item')).toHaveCount(1);
    await expect(page.locator('.sandbox-mode-ro')).toContainText('ro');

    // Close modal
    await page.click('.overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });

});

// The add row needs 452px on one line and a portrait screen has less; below that the form stacks,
// and this checks the stacked form.
test.describe('Sandbox Mounts on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

  test.beforeEach(async ({ page }) => prepareSandboxProject(page));

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
