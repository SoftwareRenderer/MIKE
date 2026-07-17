import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch } from './helpers';

test.describe.serial('Sandbox Forward Ports', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    // Ensure no tasks are in progress (blocks sandbox config)
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
    // Ensure sandbox is enabled with network isolation off (port forwarding requires sandbox + no network isolation)
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: true, sandbox_network_isolation: false },
    });
    // Reload to reset component state
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show port forwarding section when sandbox is enabled and network isolation is off', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Forward Ports subsection header should be visible (nested under Network Isolation)
    await expect(page.locator('.config-row-subsection .config-label')).toContainText('Forward Ports');
    // Section starts collapsed when no ports are set — click to expand
    await page.click('.config-row-subsection');
    await expect(page.locator('.port-add-row')).toBeVisible({ timeout: 5000 });
    // Description explains auto-assignment
    await expect(page.locator('.config-row-subsection .config-desc')).toContainText('auto-assigned');

    // Close modal
    await page.click('.sandbox-config-overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });

  test('should hide port forwarding section when network isolation is enabled', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Enable network isolation (sandbox already on, isolation off)
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_network_isolation: true },
    });
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Forward Ports subsection should NOT be visible (network isolation blocks it)
    await expect(page.locator('.config-row-subsection')).not.toBeVisible({ timeout: 2000 });

    // Close modal and reset
    await page.click('.sandbox-config-overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });

    // Reset network isolation to off
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_network_isolation: false },
    });
  });

  test('should hide port forwarding section when sandbox is disabled', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Disable sandbox
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: false },
    });
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Forward Ports subsection should NOT be visible (no sandbox = no ports to forward)
    await expect(page.locator('.config-row-subsection')).not.toBeVisible({ timeout: 2000 });

    // Close modal and reset
    await page.click('.sandbox-config-overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });

    // Reset sandbox to enabled
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: true, sandbox_network_isolation: false },
    });
  });

  test('should add and remove port forward', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Expand Forward Ports subsection
    await page.click('.config-row-subsection');
    await expect(page.locator('.port-add-row')).toBeVisible({ timeout: 5000 });

    // Add port forward (container port only, host port is auto-assigned)
    const containerInput = page.locator('.port-input[aria-label="Container port"]');
    const addButton = page.locator('.btn-add-port');

    await containerInput.fill('80');
    await addButton.click();

    // Verify port appears in list (only container port shown)
    await expect(page.locator('.port-item')).toHaveCount(1);
    await expect(page.locator('.port-container')).toContainText('80');

    // Remove port forward
    await page.locator('.btn-remove-port').first().click();
    await expect(page.locator('.port-item')).toHaveCount(0);

    // Close modal
    await page.click('.sandbox-config-overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });

  test('should reject invalid port numbers', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Expand Forward Ports subsection
    await page.click('.config-row-subsection');
    await expect(page.locator('.port-add-row')).toBeVisible({ timeout: 5000 });

    const containerInput = page.locator('.port-input[aria-label="Container port"]');
    const addButton = page.locator('.btn-add-port');

    // Try port 0 (invalid)
    await containerInput.fill('0');
    await addButton.click();
    await expect(page.locator('.port-error')).toContainText('between 1 and 65535');

    // Try port 70000 (invalid)
    await containerInput.fill('70000');
    await addButton.click();
    await expect(page.locator('.port-error')).toContainText('between 1 and 65535');

    // Close modal
    await page.click('.sandbox-config-overlay', { position: { x: 10, y: 10 } });
  });

  test('should save port forwards via API', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Update via API directly (container port only)
    const result = await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_forward_ports: [{ container_port: 8080 }] },
    });
    expect(result.ok).toBe(true);

    // Verify via API
    const saved = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    expect(saved.forward_ports).toBeDefined();
    expect(saved.forward_ports.length).toBe(1);
    expect(saved.forward_ports[0].container_port).toBe(8080);

    // Clean up
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_forward_ports: [] },
    });
  });
});
