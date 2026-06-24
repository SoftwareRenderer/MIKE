import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

test.describe.serial('Sandbox Toggle', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    // Ensure no tasks are in progress (blocks sandbox toggle)
    const projectId = await getProjectId(page);
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
    // Reload to reset component state (projectCounts, modal state)
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should toggle sandbox on via config modal', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is off via API
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: false } });
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Verify sandbox is off
    const isSandboxOn = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.sandbox_enabled;
    }, projectId);
    expect(isSandboxOn).toBe(false);

    // Open sandbox config modal via top bar button
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Toggle sandbox on
    const toggle = page.locator('.toggle-switch:not(.active)');
    await expect(toggle).toBeVisible({ timeout: 5000 });
    await toggle.click();

    // Save
    await page.click('.btn-save');
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 5000 });

    // Verify API state
    const apiSandboxOn = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.sandbox_enabled;
    }, projectId);
    expect(apiSandboxOn).toBe(true);

    // Verify UI updated (sandbox badge visible, not off)
    await expect(page.locator('.sandbox-badge:not(.sandbox-badge-off)')).toBeVisible({ timeout: 10000 });
  });

  test('should show confirmation dialog when toggling sandbox off via modal', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is on via API
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: true } });
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Toggle should be on
    await expect(page.getByRole('switch', { name: 'Sandbox isolation' })).toBeChecked();

    // Toggle sandbox off
    await page.getByRole('switch', { name: 'Sandbox isolation' }).click();

    // Click save — should show confirmation
    await page.click('.btn-save');

    // Confirmation dialog should appear
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.confirm-dialog h3')).toContainText('Disable Sandbox');

    // Confirm
    await page.click('.btn-confirm');

    // Modal should close
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 5000 });

    // Verify toggled off via API
    const apiSandboxOff = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.sandbox_enabled;
    }, projectId);
    expect(apiSandboxOff).toBe(false);
  });

  test('should cancel toggle when confirmation is dismissed', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is on via API
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: true } });
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Toggle sandbox off
    await page.getByRole('switch', { name: 'Sandbox isolation' }).click();

    // Click save — should show confirmation
    await page.click('.btn-save');
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });

    // Cancel
    await page.click('.confirm-dialog .btn-cancel');

    // Confirm dialog should close, sandbox config modal should still be open
    await expect(page.locator('.confirm-dialog')).not.toBeVisible({ timeout: 5000 });
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible();

    // Verify sandbox is still on via API
    const sandboxOn = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.sandbox_enabled;
    }, projectId);
    expect(sandboxOn).toBe(true);

    // Close the modal
    await page.click('.sandbox-config-overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 5000 });
  });

  test('should not allow toggle when tasks are In Progress', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is on via API
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: true } });

    // Create a task and move it to In Progress (CSRF-aware)
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });
    const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);
    const taskResult = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `sandbox-lock-test-${Date.now()}`,
        llama_server_url: defaultProvider?.url || '',
        model_name: defaultProvider?.default_model || '',
      },
    });
    const taskId = taskResult.data.id;

    // Move to In Progress
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Reload to pick up task counts
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Click sandbox config button — should show toast, not open modal
    await page.click('.sandbox-config-btn');

    // Toast error should appear
    await expect(page.locator('.toast.error')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.toast.error')).toContainText('Cannot change sandbox settings');

    // Modal should NOT be visible
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 2000 });
  });

  test('should not navigate to project when opening sandbox config', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is off
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: false } });
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Record current URL before click
    const urlBefore = page.url();

    // Click the sandbox config button (opens modal, should not navigate)
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Verify URL didn't change (no navigation)
    const urlAfter = page.url();
    expect(urlAfter).toBe(urlBefore);

    // Close modal
    await page.click('.sandbox-config-overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 5000 });
  });

  test('should show sandbox icon in task detail header when enabled', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is on
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: true } });

    // Create a task and open it
    const taskId = await createTaskViaApi(page, projectId, `sandbox-icon-test-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify sandbox badge is visible in the header (shield icon, not shield-off)
    const detailBadge = page.locator('.task-detail-page .header .sandbox-badge:not(.sandbox-badge-off)');
    await expect(detailBadge).toBeVisible({ timeout: 5000 });
  });

  test('should show sandbox-off icon in task detail header when disabled', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is off
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: false } });

    // Create a task and open it
    const taskId = await createTaskViaApi(page, projectId, `sandbox-icon-off-test-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify sandbox-off badge is visible in the header
    const detailBadge = page.locator('.task-detail-page .header .sandbox-badge.sandbox-badge-off');
    await expect(detailBadge).toBeVisible({ timeout: 5000 });
  });
});
