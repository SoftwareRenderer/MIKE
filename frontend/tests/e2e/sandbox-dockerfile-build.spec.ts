import { test, expect } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { execSync } from 'node:child_process';
import { getProjectId, csrfFetch } from './helpers';

test.describe.serial('Sandbox Dockerfile Build', () => {
  let projectId: string;
  let sandboxAvailable = false;

  test.beforeAll(() => {
    try {
      execSync('podman --runtime runsc images', { timeout: 15000, stdio: 'pipe' });
      sandboxAvailable = true;
    } catch {
      sandboxAvailable = false;
    }
  });

  test.beforeEach(async ({ page }) => {
    test.skip(!sandboxAvailable, 'gVisor sandbox is not functional in this environment');

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);

    const project = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    const worktreePath = project?.path;
    if (worktreePath) {
      fs.writeFileSync(path.join(worktreePath, 'Dockerfile'), 'FROM alpine:latest\n');
    }

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

    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test.afterEach(async ({ page }) => {
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
    await page.click('.sandbox-config-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });

    const isLocked = await page.locator('.locked-view').isVisible().catch(() => false);
    if (isLocked) {
      test.skip('Sandbox config modal is locked');
    }

    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });

    await page.locator('.radio-label').filter({ hasText: 'Dockerfile' }).click();

    await page.locator('.btn-browse').click();
    await expect(page.locator('[role="dialog"]')).toHaveCount(2, { timeout: 5000 });

    const dockerfileEntry = page.locator('.file-entry').filter({ hasText: 'Dockerfile' });
    await expect(dockerfileEntry).toBeVisible({ timeout: 5000 });
    await dockerfileEntry.click();

    await page.locator('.btn-primary', { hasText: 'Select' }).click();

    await expect(page.locator('.dockerfile-path-text')).not.toHaveText('No Dockerfile selected');
    const buildBtn = page.locator('.btn-build');
    await expect(buildBtn).toBeVisible({ timeout: 5000 });

    await buildBtn.click();

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
