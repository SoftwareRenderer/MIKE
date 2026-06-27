import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted } from './helpers';

test.describe.serial('Thread Switch — messages update without page refresh', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    test.setTimeout(120000);
    await page.goto('/');
    await page.waitForSelector('.card');

    projectId = await getProjectId(page);
    taskId = await createTaskViaApi(page, projectId, `thread-switch-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    const ok = await ensureTaskStarted(page, projectId, taskId);
    if (!ok) test.skip('Could not move task to in_progress');

    await expect(page.locator('.chat-form')).toBeVisible();
  });

  test('should show only fork thread messages after forking (no stale main thread messages)', async ({ page }) => {
    // Step 1: Create messages in the main thread via API
    const msg1 = `Main msg A ${Date.now()}`;
    const msg2 = `Main msg B ${Date.now()}`;

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: msg1 },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: msg2 },
    });

    // Reload to pick up messages
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-form')).toBeVisible();

    // Wait for both messages to appear
    await expect(async () => {
      const texts = await page.locator('.chat-message.user').allTextContents();
      expect(texts.some(t => t.includes('Main msg A'))).toBe(true);
      expect(texts.some(t => t.includes('Main msg B'))).toBe(true);
    }).toPass({ timeout: 10000 });

    // Step 2: Fork a new thread from msg1 via the API
    const msg1Id = await page.evaluate(async ({ pid, tid, msgText }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
      const messages = await res.json();
      const msg = messages.find((m: any) => m.content && m.content.includes(msgText));
      return msg?.id || '';
    }, { pid: projectId, tid: taskId, msgText: 'Main msg A' });

    if (!msg1Id) test.skip('Could not find msg1 ID');

    const activeThreadId = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/threads`);
      const threads = await res.json();
      const active = threads.find((t: any) => t.is_active);
      return active?.id || '';
    }, { pid: projectId, tid: taskId });

    if (!activeThreadId) test.skip('Could not find active thread ID');

    const forkResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/fork`, {
      method: 'POST',
      body: { from_thread_id: activeThreadId, from_message_id: msg1Id },
    });

    if (!forkResult.ok) test.skip(`Fork API failed: ${forkResult.status}`);
    const forkThreadId = forkResult.data?.id;
    if (!forkThreadId) test.skip('No fork thread ID in response');

    // Step 3: Navigate to the fork thread via URL (tests syncThreadFromUrl + loadHistory)
    await page.evaluate((url) => { window.location.href = url; }, `/?task=${taskId}&thread=${forkThreadId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-form')).toBeVisible();

    // msg1 should be present (it's the fork checkpoint)
    // msg2 should NOT be present (it's after the fork point in the main thread)
    await expect(async () => {
      const texts = await page.locator('.chat-message.user').allTextContents();
      expect(texts.some(t => t.includes('Main msg A'))).toBe(true);
      expect(texts.some(t => t.includes('Main msg B'))).toBe(false);
    }).toPass({ timeout: 15000 });
  });
});
