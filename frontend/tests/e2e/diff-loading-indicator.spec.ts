import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Diff Loading Indicator', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show loading text in empty diff state during thread switch', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `diff-loading-empty-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);
    await navigateToTask(page, taskId);

    // Wait for chat form and diff panel
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

    // Verify the empty diff state is shown (no changes yet)
    await expect(page.locator('.diff-empty')).toBeVisible();

    // Create a message to fork from
    const msgResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: 'Fork point for loading indicator test' },
    });
    expect(msgResult.ok).toBe(true);
    const msgId = msgResult.data?.id;

    // Get threads and fork
    const threadsResult = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const mainThreadId = threadsResult[0]?.id;

    const forkResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/fork`, {
      method: 'POST',
      body: { from_thread_id: mainThreadId, from_message_id: msgId },
    });
    expect(forkResult.ok).toBe(true);

    // Reload page so checkpoint divider appears
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-form')).toBeVisible();
    await expect(page.locator('.thread-btn').first()).toBeVisible({ timeout: 10000 });

    // Intercept the diff API call to add a delay so the loading state is observable
    await page.route('**/api/projects/*/tasks/*/diff*', async (route) => {
      setTimeout(async () => {
        await route.continue();
      }, 500);
    });

    // Switch thread via UI
    await page.locator('.thread-btn').first().click();
    await expect(page.locator('.thread-menu')).toBeVisible({ timeout: 5000 });
    const nonActiveItem = page.locator('.thread-menu-item:not(.active)').first();
    await nonActiveItem.click();

    // The loading text should appear in the empty diff panel
    await expect(page.locator('.diff-empty .diff-loading')).toBeVisible({ timeout: 10000 });

    // After loading completes, should return to normal empty state
    await expect(page.locator('.diff-empty .diff-loading')).not.toBeVisible({ timeout: 10000 });
  });
});
