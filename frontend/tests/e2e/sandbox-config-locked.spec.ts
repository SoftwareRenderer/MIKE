import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch, createTaskViaApi, ensureTaskStarted } from './helpers';

test.describe.serial('Sandbox Config Locked View', () => {
  let projectId: string;

  test.afterEach(async ({ page }) => {
    // Clean up: move all tasks to backlog so parallel tests aren't affected
    if (projectId) {
      const tasks = await page.evaluate(async (pid) => {
        const res = await fetch(`/api/projects/${pid}/tasks`);
        return res.ok ? res.json() : [];
      }, projectId);
      for (const task of tasks) {
        if (task.status === 'in_progress') {
          await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
            method: 'POST',
            body: { status: 'backlog' },
          });
        }
      }
    }
  });

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);

    // Move all tasks to backlog for clean state
    const tasks = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/tasks`);
      return res.ok ? res.json() : [];
    }, projectId);
    for (const task of tasks) {
      if (task.status === 'in_progress') {
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
          method: 'POST',
          body: { status: 'backlog' },
        });
      }
    }
  });

  test('should show read-only view when tasks are in progress', async ({ page }) => {
    // Enable sandbox with settings to display
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: {
        sandbox_enabled: true,
        sandbox_network_isolation: false,
        sandbox_memory_limit: 2048,
        sandbox_image: 'ubuntu:22.04',
      },
    });

    // Create and start a task
    const taskId = await createTaskViaApi(page, projectId, `locked-test-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);

    // Navigate to force full SPA reload
    await page.evaluate(() => { window.location.href = '/'; });
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Should show the yellow locked notice
    const lockedNotice = page.locator('.locked-notice');
    await expect(lockedNotice).toBeVisible();
    await expect(lockedNotice).toContainText('Cannot change sandbox settings while tasks are In Progress');

    // Should show read-only settings (not the editable form)
    await expect(page.locator('.locked-view')).toBeVisible();

    // Should display current settings in read-only format
    const settingsList = page.locator('.settings-list');
    await expect(settingsList).toBeVisible();

    // Sandbox Isolation setting
    await expect(page.locator('.setting-label').filter({ hasText: 'Sandbox Isolation' })).toBeVisible();
    await expect(page.locator('.setting-value').first()).toContainText('Enabled');

    // Network Isolation setting (visible because sandbox is enabled)
    await expect(page.locator('.setting-label').filter({ hasText: 'Network Isolation' })).toBeVisible();

    // Memory Limit setting
    await expect(page.locator('.setting-label').filter({ hasText: 'Memory Limit' })).toBeVisible();
    await expect(page.locator('.setting-value').filter({ hasText: '2048 MB' })).toBeVisible();

    // Image setting
    await expect(page.locator('.setting-label').filter({ hasText: 'Image' })).toBeVisible();
    await expect(page.locator('.setting-value-mono')).toContainText('ubuntu:22.04');

    // Should NOT show the editable form elements
    await expect(page.locator('.config-options')).not.toBeVisible();
    await expect(page.locator('.toggle-switch')).not.toBeVisible();

    // Should NOT show Save/Cancel footer buttons (footer is hidden when locked)
    await expect(page.locator('.modal-footer')).not.toBeVisible();
  });

  test('should show disabled state when sandbox is off but tasks in progress', async ({ page }) => {
    // Disable sandbox (tasks are in backlog from beforeEach, so this is allowed)
    const result = await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: false },
    });
    expect(result.ok).toBe(true);

    // Create and start a task with sandbox disabled
    const taskId = await createTaskViaApi(page, projectId, `locked-disabled-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);

    // Navigate to force full SPA reload
    await page.evaluate(() => { window.location.href = '/'; });
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Should show the yellow locked notice
    const lockedNotice = page.locator('.locked-notice');
    await expect(lockedNotice).toBeVisible();
    await expect(lockedNotice).toContainText('Cannot change sandbox settings while tasks are In Progress');

    // Should show Sandbox Isolation as Disabled
    await expect(page.locator('.setting-value').first()).toContainText('Disabled');

    // Should NOT show sandbox-specific settings (network isolation, memory, image) when sandbox is disabled
    await expect(page.locator('.setting-label').filter({ hasText: 'Network Isolation' })).not.toBeVisible();
    await expect(page.locator('.setting-label').filter({ hasText: 'Memory Limit' })).not.toBeVisible();
    await expect(page.locator('.setting-label').filter({ hasText: 'Image' })).not.toBeVisible();
  });

  test('should show editable form when no tasks are in progress', async ({ page }) => {
    // Ensure sandbox is enabled
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: {
        sandbox_enabled: true,
        sandbox_network_isolation: false,
      },
    });

    // Navigate to force full SPA reload
    await page.evaluate(() => { window.location.href = '/'; });
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Should show the editable form (not the locked view)
    await expect(page.locator('.config-options')).toBeVisible();
    await expect(page.locator('.locked-view')).not.toBeVisible();
    await expect(page.locator('.locked-notice')).not.toBeVisible();

    // Should show Save/Cancel footer
    await expect(page.locator('.modal-footer')).toBeVisible();
    await expect(page.locator('.btn-primary')).toContainText('Save');
    await expect(page.locator('.btn-secondary')).toContainText('Cancel');
  });
});
