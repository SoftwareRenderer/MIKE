import { test, expect } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { getProjectId, csrfFetch } from './helpers';

/**
 * Regression test for: "Error: no Dockerfile path configured for this project"
 * when clicking Build on a newly-selected (unsaved) Dockerfile.
 *
 * The Sandbox Config modal lets you select a Dockerfile and click "Build"
 * before saving. The backend build endpoint reads the Dockerfile path from the
 * project, so the path must be persisted before the build is attempted. This
 * test verifies that clicking Build persists the selected Dockerfile path.
 */
test.describe.serial('Sandbox Dockerfile Build', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);

    // Place a Dockerfile in the project worktree so it is visible inside the
    // sandbox container (the worktree is bind-mounted at /home/sandbox/<name>).
    const project = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    const worktreePath = project?.path;
    if (worktreePath) {
      fs.writeFileSync(path.join(worktreePath, 'Dockerfile'), 'FROM alpine:latest\n');
    }

    // Move all in-progress tasks to backlog (blocks sandbox config changes)
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

    // Ensure sandbox is enabled (required for the Dockerfile section to render)
    const putResult = await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: true, sandbox_network_isolation: false },
    });
    if (!putResult.ok && putResult.status === 409) {
      test.skip('Cannot enable sandbox: tasks are running from parallel tests');
    } else if (!putResult.ok) {
      throw new Error(`Failed to enable sandbox: ${putResult.status}`);
    }

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

  test.afterEach(async ({ page }) => {
    // Clean up: remove the Dockerfile and reset the dockerfile path
    const project = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    if (project?.path) {
      fs.rmSync(path.join(project.path, 'Dockerfile'), { force: true });
    }
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_dockerfile_path: '' },
    }).catch(() => {});
  });

  test('should persist the Dockerfile path when Build is clicked on an unsaved selection', async ({ page }) => {
    // Open sandbox config modal
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    // Skip gracefully if locked (tasks in progress from parallel tests)
    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      test.skip('Sandbox config modal is locked');
    }

    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    // Switch to Dockerfile image source
    await page.locator('input[name="image-source"][value="dockerfile"]').check();

    // Open the Dockerfile picker (starts at the project worktree mount)
    await page.locator('.btn-browse').click();
    await expect(page.locator('[role="dialog"]')).toHaveCount(2, { timeout: 5000 });

    // Select the Dockerfile file in the worktree
    const dockerfileEntry = page.locator('.file-entry').filter({ hasText: 'Dockerfile' });
    await expect(dockerfileEntry).toBeVisible({ timeout: 5000 });
    await dockerfileEntry.click();

    // Confirm selection
    await page.locator('.btn-primary', { hasText: 'Select' }).click();

    // The modal should now show a selected path and a Build button
    await expect(page.locator('.dockerfile-path-text')).not.toHaveText('No Dockerfile selected');
    const buildBtn = page.locator('.btn-build');
    await expect(buildBtn).toBeVisible({ timeout: 5000 });

    // Click Build. This must persist the path BEFORE attempting the build.
    await buildBtn.click();

    // The Dockerfile path should be persisted to the project (the fix).
    await expect(async () => {
      const project = await page.evaluate(async (pid) => {
        const res = await fetch(`/api/projects/${pid}`);
        return res.ok ? res.json() : null;
      }, projectId);
      expect(project?.sandbox_dockerfile_path).toBeTruthy();
      expect(project?.sandbox_dockerfile_path).toMatch(/Dockerfile$/);
    }).toPass({ timeout: 5000 });
  });
});
