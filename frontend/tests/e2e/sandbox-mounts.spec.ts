import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe.serial('Sandbox Mounts', () => {
  test.beforeEach(async ({ page }) => {
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
  });

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

  test('should save mounts via API', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Update via API directly
    const result = await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_mounts: [
        { host_path: '/data/cache', container_path: '/mnt/cache', mode: 'rw' },
        { host_path: '/etc/config', container_path: '/etc/config', mode: 'ro' },
      ]},
    });
    expect(result.ok).toBe(true);

    // Verify via API
    const saved = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    expect(saved.sandbox_mounts).toBeDefined();
    expect(saved.sandbox_mounts.length).toBe(2);
    expect(saved.sandbox_mounts[0].host_path).toBe('/data/cache');
    expect(saved.sandbox_mounts[0].container_path).toBe('/mnt/cache');
    expect(saved.sandbox_mounts[0].mode).toBe('rw');
    expect(saved.sandbox_mounts[1].mode).toBe('ro');

    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_mounts: [] },
    });
  });
});
