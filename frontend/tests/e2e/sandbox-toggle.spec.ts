import { test, expect, getProjectId, createTaskViaApi } from './helpers';

test.describe.serial('Sandbox Toggle', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should toggle sandbox on by clicking the shield', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is off (default), then toggle on
    const isSandboxOn = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.sandbox_enabled;
    }, projectId);

    if (isSandboxOn) {
      // Toggle off first (with confirmation)
      await page.click('.sandbox-badge:not(.sandbox-badge-off)');
      await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
      await page.click('.btn-confirm');
      await page.waitForFunction(
        async () => {
          const res = await fetch('/api/projects');
          const projects = await res.json();
          return projects[0]?.sandbox_enabled === false;
        },
        { timeout: 5000 }
      );
    }

    // Now toggle on (no confirmation needed)
    await page.click('.sandbox-badge-off');
    await page.waitForFunction(
      async () => {
        const res = await fetch('/api/projects');
        const projects = await res.json();
        return projects[0]?.sandbox_enabled === true;
      },
      { timeout: 5000 }
    );

    // Verify UI updated
    await expect(page.locator('.sandbox-badge:not(.sandbox-badge-off)')).toBeVisible();
  });

  test('should show confirmation dialog when toggling sandbox off', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is on via API
    await page.evaluate(async (pid) => {
      await fetch(`/api/projects/${pid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sandbox_enabled: true }),
      });
    }, projectId);
    await page.reload();
    await page.waitForSelector('.sandbox-badge:not(.sandbox-badge-off)', { timeout: 5000 });

    // Click to toggle off — should show confirmation
    await page.click('.sandbox-badge:not(.sandbox-badge-off)');

    // Wait for confirmation dialog
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.confirm-dialog h3')).toContainText('Disable Sandbox');

    // Confirm
    await page.click('.btn-confirm');

    // Verify toggled off
    await expect(page.locator('.sandbox-badge-off')).toBeVisible({ timeout: 5000 });
  });

  test('should cancel toggle when confirmation is dismissed', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is on via API
    await page.evaluate(async (pid) => {
      await fetch(`/api/projects/${pid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sandbox_enabled: true }),
      });
    }, projectId);
    await page.reload();
    await page.waitForSelector('.sandbox-badge:not(.sandbox-badge-off)', { timeout: 5000 });

    // Click to toggle off — should show confirmation
    await page.click('.sandbox-badge:not(.sandbox-badge-off)');
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });

    // Cancel
    await page.click('.btn-cancel');

    // Verify still enabled (no toggle happened)
    await expect(page.locator('.confirm-dialog')).not.toBeVisible();
    await expect(page.locator('.sandbox-badge:not(.sandbox-badge-off)')).toBeVisible();
  });

  test('should not allow toggle when tasks are In Progress', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is on via API
    await page.evaluate(async (pid) => {
      await fetch(`/api/projects/${pid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sandbox_enabled: true }),
      });
    }, projectId);

    // Create a task and move it to In Progress
    await page.evaluate(async (pid) => {
      const configRes = await fetch('/api/config');
      const config = await configRes.json();
      const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);

      const taskRes = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt: `sandbox-lock-test-${Date.now()}`,
          llama_server_url: defaultProvider?.url || '',
          model_name: defaultProvider?.default_model || '',
        }),
      });
      const task = await taskRes.json();

      // Move to In Progress
      await fetch(`/api/projects/${pid}/tasks/${task.id}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'in_progress' }),
      });
    }, projectId);

    // Reload to pick up task counts
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Verify the shield shows as locked (tooltip contains lock message)
    const shield = page.locator('.sandbox-badge.sandbox-badge-locked');
    await expect(shield).toBeVisible();
    const tooltip = await shield.getAttribute('title');
    expect(tooltip).toContain('Cannot change');

    // Click should not toggle
    await shield.click();
    // Should still show as enabled (no toggle, no confirmation)
    await expect(page.locator('.confirm-dialog')).not.toBeVisible({ timeout: 2000 });
    await expect(page.locator('.sandbox-badge:not(.sandbox-badge-off)')).toBeVisible();
  });

  test('should not navigate to project when clicking shield', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure no tasks are In Progress (clean up from previous test)
    await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      for (const task of board.tasks) {
        if (task.status === 'in_progress') {
          await fetch(`/api/projects/${pid}/tasks/${task.id}/move`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ status: 'backlog' }),
          });
        }
      }
    }, projectId);

    // Ensure sandbox is off
    await page.evaluate(async (pid) => {
      await fetch(`/api/projects/${pid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sandbox_enabled: false }),
      });
    }, projectId);
    await page.reload();
    await page.waitForSelector('.sandbox-badge-off', { timeout: 5000 });

    // Record current URL before click
    const urlBefore = page.url();

    // Click the shield (should toggle, not navigate)
    await page.click('.sandbox-badge-off');

    // Verify sandbox toggled on
    await expect(page.locator('.sandbox-badge:not(.sandbox-badge-off)')).toBeVisible({ timeout: 5000 });

    // Verify URL didn't change (no navigation)
    const urlAfter = page.url();
    expect(urlAfter).toBe(urlBefore);
  });

  test('should show sandbox icon in task detail header when enabled', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is on
    await page.evaluate(async (pid) => {
      await fetch(`/api/projects/${pid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sandbox_enabled: true }),
      });
    }, projectId);

    // Create a task and open it
    const taskId = await createTaskViaApi(page, projectId, `sandbox-icon-test-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify sandbox badge is visible in the header (shield icon, not shield-off)
    const detailBadge = page.locator('.task-detail-page .header .sandbox-badge:not(.sandbox-badge-off)');
    await expect(detailBadge).toBeVisible({ timeout: 5000 });
  });

  test('should show sandbox-off icon in task detail header when disabled', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is off
    await page.evaluate(async (pid) => {
      await fetch(`/api/projects/${pid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sandbox_enabled: false }),
      });
    }, projectId);

    // Create a task and open it
    const taskId = await createTaskViaApi(page, projectId, `sandbox-icon-off-test-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify sandbox-off badge is visible in the header
    const detailBadge = page.locator('.task-detail-page .header .sandbox-badge.sandbox-badge-off');
    await expect(detailBadge).toBeVisible({ timeout: 5000 });
  });
});
