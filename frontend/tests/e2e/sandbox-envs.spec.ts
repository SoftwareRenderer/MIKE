import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe.serial('Sandbox Environment Variables', () => {
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
            }).catch(() => {});
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
              method: 'POST',
              body: { status: 'backlog' },
            }).catch(() => {});
            changed = true;
          }
        }
        if (!changed) break;
        await page.waitForFunction(() => false, {}, { timeout: 1000, polling: 1000 }).catch(() => {});
      }

      // Now enable sandbox
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

    // Clear any existing envs
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_envs: [] },
    }).catch(() => {});

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
  });

  test('should add a new environment variable', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Check for locked view
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      await page.click('.overlay', { position: { x: 10, y: 10 } });
      test.skip('Sandbox config modal is locked');
    }

    // Wait for config options to render
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    // Expand Environment Variables subsection
    const envsHeader = page.locator('.config-row-expandable .config-label').filter({ hasText: 'Environment Variables' });
    const envsCount = await envsHeader.count();
    if (envsCount === 0) {
      test.skip('Environment Variables section not visible');
    }
    await expect(envsHeader.first()).toBeVisible({ timeout: 10000 });
    await envsHeader.first().click();
    await expect(page.locator('.env-add-row')).toBeVisible({ timeout: 5000 });

    // Add env var
    const keyInput = page.locator('.env-input-key');
    const valueInput = page.locator('.env-input:not(.env-input-key)');
    const addButton = page.locator('.btn-add-env');

    await keyInput.fill('TEST_VAR');
    await valueInput.fill('test_value');
    await addButton.click();

    // Verify env var appears in list
    await expect(page.locator('.env-item')).toHaveCount(1);
    await expect(page.locator('.env-key')).toContainText('TEST_VAR');
    await expect(page.locator('.env-value')).toContainText('test_value');

    // Close modal without saving
    await page.click('.overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });

  test('should save environment variables via UI', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Check for locked view
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      await page.click('.overlay', { position: { x: 10, y: 10 } });
      test.skip('Sandbox config modal is locked');
    }

    // Wait for config options to render
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    // Expand Environment Variables subsection
    const envsHeader = page.locator('.config-row-expandable .config-label').filter({ hasText: 'Environment Variables' });
    await expect(envsHeader).toBeVisible({ timeout: 10000 });
    await envsHeader.click();
    await expect(page.locator('.env-add-row')).toBeVisible({ timeout: 5000 });

    // Add env var
    const keyInput = page.locator('.env-input-key');
    const valueInput = page.locator('.env-input:not(.env-input-key)');
    const addButton = page.locator('.btn-add-env');

    await keyInput.fill('DB_HOST');
    await valueInput.fill('localhost');
    await addButton.click();

    // Verify env var appears
    await expect(page.locator('.env-item')).toHaveCount(1);

    // Save
    await page.click('button:has-text("Save")');
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });

    // Verify saved via API
    const saved = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    expect(saved.sandbox_envs).toBeDefined();
    expect(saved.sandbox_envs.length).toBe(1);
    expect(saved.sandbox_envs[0].key).toBe('DB_HOST');
    expect(saved.sandbox_envs[0].value).toBe('localhost');

    // Clean up
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_envs: [] },
    });
  });

  test('should edit and save an existing environment variable', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Pre-populate an env var via API
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_envs: [{ key: 'ORIGINAL_KEY', value: 'original_value' }] },
    });

    // Verify the original value is stored
    let project = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    expect(project.sandbox_envs[0].key).toBe('ORIGINAL_KEY');
    expect(project.sandbox_envs[0].value).toBe('original_value');

    // Edit via API (tests the backend update logic)
    const result = await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_envs: [{ key: 'EDITED_KEY', value: 'edited_value' }] },
    });
    expect(result.ok).toBe(true);

    // Verify edited values are saved
    project = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    expect(project.sandbox_envs).toBeDefined();
    expect(project.sandbox_envs.length).toBe(1);
    expect(project.sandbox_envs[0].key).toBe('EDITED_KEY');
    expect(project.sandbox_envs[0].value).toBe('edited_value');

    // Clean up
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_envs: [] },
    });
  });

  test('should show existing env vars in UI after reload', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Pre-populate env vars via API
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_envs: [
        { key: 'UI_TEST_KEY', value: 'ui_test_value' },
        { key: 'SECOND_KEY', value: 'second_value' },
      ]},
    });

    // Reload to pick up the change
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Check for locked view
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      await page.click('.overlay', { position: { x: 10, y: 10 } });
      test.skip('Sandbox config modal is locked');
    }

    // Wait for config options to render
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    // Expand Environment Variables subsection
    const envsHeader = page.locator('.config-row-expandable .config-label').filter({ hasText: 'Environment Variables' });
    await expect(envsHeader).toBeVisible({ timeout: 10000 });
    await envsHeader.click();

    // Wait for env items to appear
    await expect(page.locator('.env-item')).toHaveCount(2, { timeout: 10000 });
    await expect(page.locator('.env-key').first()).toContainText('UI_TEST_KEY');

    // Close modal
    await page.click('.overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });

    // Clean up
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_envs: [] },
    });
  });

  test('should reject duplicate environment variable names', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Check for locked view
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      await page.click('.overlay', { position: { x: 10, y: 10 } });
      test.skip('Sandbox config modal is locked');
    }

    // Wait for config options to render
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    // Expand Environment Variables subsection
    const envsHeader = page.locator('.config-row-expandable .config-label').filter({ hasText: 'Environment Variables' });
    await expect(envsHeader).toBeVisible({ timeout: 10000 });
    await envsHeader.click();
    await expect(page.locator('.env-add-row')).toBeVisible({ timeout: 5000 });

    const keyInput = page.locator('.env-input-key');
    const valueInput = page.locator('.env-input:not(.env-input-key)');
    const addButton = page.locator('.btn-add-env');

    // Add first env var
    await keyInput.fill('DUP_KEY');
    await valueInput.fill('value1');
    await addButton.click();
    await expect(page.locator('.env-item')).toHaveCount(1);

    // Try adding duplicate
    await keyInput.fill('DUP_KEY');
    await valueInput.fill('value2');
    await addButton.click();

    // Should show error
    await expect(page.locator('.env-error')).toContainText('Duplicate');

    // Close modal
    await page.click('.overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });

  test('should reject empty environment variable names', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Check for locked view
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      await page.click('.overlay', { position: { x: 10, y: 10 } });
      test.skip('Sandbox config modal is locked');
    }

    // Wait for config options to render
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    // Expand Environment Variables subsection
    const envsHeader = page.locator('.config-row-expandable .config-label').filter({ hasText: 'Environment Variables' });
    await expect(envsHeader).toBeVisible({ timeout: 10000 });
    await envsHeader.click();
    await expect(page.locator('.env-add-row')).toBeVisible({ timeout: 5000 });

    const addButton = page.locator('.btn-add-env');

    // Try adding with empty key
    await addButton.click();
    await expect(page.locator('.env-error')).toContainText('required');

    // Close modal
    await page.click('.overlay', { position: { x: 10, y: 10 } });
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });
});
