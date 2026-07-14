import { test, expect } from '@playwright/test';
import { createAndOpenTask, waitForChatForm, csrfFetch, getProjectId, navigateToTask } from './helpers';

/**
 * Tab visibility reconciles stale streaming state.
 * On mobile, JS is suspended while hidden and the 'complete' event may be missed.
 * The visibilitychange handler fetches task status from the API and reloads from DB.
 */
test.describe.serial('Tab visibility handling', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('visibility change does not disrupt normal (idle) task state', async ({ page }) => {
    test.setTimeout(30000);

    await createAndOpenTask(page, `visibility-idle ${Date.now()}`);
    await waitForChatForm(page);

    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    const initialEntries = await page.locator('.chat-message').count();

    // Trigger multiple visibility changes
    for (let i = 0; i < 3; i++) {
      await page.evaluate(() => {
        window.dispatchEvent(new Event('visibilitychange'));
      });
      await page.waitForTimeout(200);
    }

    // State should be stable
    await expect(page.locator('.btn-send')).toBeVisible();
    const afterEntries = await page.locator('.chat-message').count();
    expect(afterEntries).toBe(initialEntries);
    // No streaming artifacts
    expect(await page.locator('.cursor-blink').isVisible().catch(() => false)).toBe(false);
    expect(await page.locator('.btn-stop').isVisible().catch(() => false)).toBe(false);
  });

  test('task in review state remains stable after visibility change', async ({ page }) => {
    test.setTimeout(45000);

    const projectId = await getProjectId(page);
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });
    const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);

    // Create task
    const taskResult = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `visibility-review ${Date.now()}`,
        llama_server_url: defaultProvider?.url || '',
        model_name: defaultProvider?.default_model || '',
      },
    });
    expect(taskResult.ok).toBe(true);
    const taskId = taskResult.data?.id;
    expect(taskId).toBeTruthy();

    // Move to in_progress, then to review
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Navigate to the task
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-message').first()).toBeVisible({ timeout: 15000 });

    // Verify task is in review
    const taskStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return task.status;
    }, { pid: projectId, tid: taskId });
    expect(taskStatus).toBe('review');

    // Trigger visibility change
    await page.evaluate(() => {
      window.dispatchEvent(new Event('visibilitychange'));
    });
    await page.waitForTimeout(500);

    // Task should remain in clean state
    expect(await page.locator('.btn-stop').isVisible().catch(() => false)).toBe(false);
    expect(await page.locator('.cursor-blink').isVisible().catch(() => false)).toBe(false);
    // Chat entries should be present
    expect(await page.locator('.chat-message').count()).toBeGreaterThan(0);
  });
});
