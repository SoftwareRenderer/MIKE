import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch } from './helpers';

test.describe.serial('Sandbox Config Modal', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    // Ensure sidebar is expanded
    if (await page.locator('.app.collapsed').isVisible().catch(() => false)) {
      await page.locator('.toggle-btn').click();
      await expect(page.locator('.app:not(.collapsed)')).toBeVisible({ timeout: 5000 });
    }
    await page.waitForSelector('.project-item', { timeout: 10000 });
    // Ensure no tasks are in progress
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
  });

  test('should open sandbox config modal from top bar shield button', async ({ page }) => {
    // Click the sandbox config button in the top bar
    await page.click('.sandbox-config-btn');
    // Modal should be visible
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('#sandbox-config-title')).toContainText('Sandbox Configuration');
  });

  test('should close modal on overlay click', async ({ page }) => {
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });
    // Click on the overlay (outside the dialog)
    await page.click('.sandbox-config-overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 5000 });
  });

  test('should close modal on Escape key', async ({ page }) => {
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });
    await page.keyboard.press('Escape');
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 5000 });
  });

  test('should enable sandbox via modal', async ({ page }) => {
    const projectId = await getProjectId(page);
    // Ensure sandbox is off
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: false } });

    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Toggle sandbox on
    const toggle = page.locator('.toggle-switch:not(.active)');
    await expect(toggle).toBeVisible({ timeout: 5000 });
    await toggle.click();

    // Now toggle should be on
    await expect(page.locator('.toggle-switch.active')).toBeVisible();

    // Save
    await page.click('.btn-save');

    // Wait for modal to close and API to update
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 5000 });

    // Verify API state
    const sandboxOn = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.sandbox_enabled;
    }, projectId);
    expect(sandboxOn).toBe(true);
  });

  test('should enable network isolation via modal', async ({ page }) => {
    const projectId = await getProjectId(page);
    // Ensure sandbox is on and network isolation is off
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: true, sandbox_network_isolation: false } });

    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Network isolation toggle should be visible (sandbox is on)
    const networkRow = page.locator('.config-row:has-text("Network Isolation")');
    await expect(networkRow).toBeVisible({ timeout: 5000 });

    // Toggle network isolation on
    const networkToggle = networkRow.locator('.toggle-switch:not(.active)');
    await networkToggle.click();

    // Save
    await page.click('.btn-save');
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 5000 });

    // Verify API state
    const networkIsolation = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.sandbox_network_isolation;
    }, projectId);
    expect(networkIsolation).toBe(true);
  });

  test('should hide network isolation toggle when sandbox is disabled', async ({ page }) => {
    const projectId = await getProjectId(page);
    // Ensure sandbox is off
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: false, sandbox_network_isolation: false } });
    // Reload to pick up updated project state
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Network isolation row should NOT be visible
    const networkRow = page.locator('.config-row:has-text("Network Isolation")');
    await expect(networkRow).not.toBeVisible({ timeout: 5000 });
  });

  test('should show confirmation when disabling sandbox via modal', async ({ page }) => {
    const projectId = await getProjectId(page);
    // Ensure sandbox is on
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: true } });
    await page.reload();
    await page.waitForSelector('.sandbox-badge:not(.sandbox-badge-off)', { timeout: 10000 });

    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Toggle should be on
    const toggleOn = page.locator('.toggle-switch.active');
    await expect(toggleOn).toBeVisible({ timeout: 5000 });

    // Toggle sandbox off
    await toggleOn.click();

    // Click save — should show confirmation
    await page.click('.btn-save');

    // Confirmation dialog should appear
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.confirm-dialog h3')).toContainText('Disable Sandbox');
  });

  test('should cancel disable sandbox when confirmation is dismissed', async ({ page }) => {
    const projectId = await getProjectId(page);
    // Ensure sandbox is on
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: true } });
    await page.reload();
    await page.waitForSelector('.sandbox-badge:not(.sandbox-badge-off)', { timeout: 10000 });

    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Toggle sandbox off
    await page.click('.toggle-switch.active');

    // Click save — should show confirmation
    await page.click('.btn-save');
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });

    // Cancel
    await page.click('.btn-cancel');

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
  });

  test('should lock settings when tasks are In Progress', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create a task and move it to In Progress
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });
    const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);
    const taskResult = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `sandbox-lock-modal-test-${Date.now()}`,
        llama_server_url: defaultProvider?.url || '',
        model_name: defaultProvider?.default_model || '',
      },
    });
    const taskId = taskResult.data.id;

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Open modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.sandbox-config-dialog')).toBeVisible({ timeout: 5000 });

    // Locked notice should be visible
    await expect(page.locator('.locked-notice')).toBeVisible({ timeout: 5000 });

    // Config options should be dimmed
    const configOptions = page.locator('.config-options.locked');
    await expect(configOptions).toBeVisible();

    // Save button should be disabled
    await expect(page.locator('.btn-save')).toBeDisabled();
  });

  test('should show sidebar shield as status-only (not clickable)', async ({ page }) => {
    // Ensure sandbox is off
    const projectId = await getProjectId(page);
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: false } });

    // Sidebar shield should not have role="button"
    const sidebarShield = page.locator('.project-item .sandbox-badge');
    const role = await sidebarShield.getAttribute('role');
    expect(role).toBeNull();

    // Sidebar shield should not have tabindex (or tabindex=-1)
    const tabindex = await sidebarShield.getAttribute('tabindex');
    expect(tabindex).toBeNull();

    // Clicking sidebar shield should NOT open the modal
    await sidebarShield.click();
    await expect(page.locator('.sandbox-config-dialog')).not.toBeVisible({ timeout: 2000 });
  });
});
