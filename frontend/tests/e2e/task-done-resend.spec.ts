import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, waitForChatForm } from './helpers';

test.describe.serial('Task Done - Resend Message', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should enable chat input for a Done task', async ({ page }) => {
    // Create a task via API
    taskId = await createTaskViaApi(page, projectId, `done-resend-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move task to done via API
    const moveStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'done' }),
      });
      return res.status;
    }, { pid: projectId, tid: taskId });
    expect(moveStatus).toBe(200);

    // Navigate to the task via URL
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Chat textarea should NOT be disabled
    const textarea = page.locator('textarea[placeholder*="message"], textarea[placeholder*="Send"]');
    await expect(textarea).toBeVisible();
    await expect(textarea).not.toBeDisabled();

    // Send button should be enabled (when there's text)
    await textarea.fill('test message');
    const sendBtn = page.locator('.btn-send');
    await expect(sendBtn).toBeVisible();
    await expect(sendBtn).not.toBeDisabled();
  });

  test('should move task back to In Progress when sending a message from Done', async ({ page }) => {
    // Create a task via API
    taskId = await createTaskViaApi(page, projectId, `done-resend-move-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move task to done via API
    await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'done' }),
      });
      return res.json();
    }, { pid: projectId, tid: taskId });

    // Verify task is in done
    const beforeStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return { status: task.status, status_state: task.status_state };
    }, { pid: projectId, tid: taskId });
    expect(beforeStatus.status).toBe('done');
    expect(beforeStatus.status_state).toBe('completed');

    // Navigate to the task via URL
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"], textarea[placeholder*="Send"]');
    await textarea.fill(`Follow up message ${Date.now()}`);
    await textarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait a moment for the move + start to propagate
    await page.waitForTimeout(2000);

    // Verify task has moved back to in_progress
    const afterStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return { status: task.status, status_state: task.status_state };
    }, { pid: projectId, tid: taskId });
    expect(afterStatus.status).toBe('in_progress');
    expect(afterStatus.status_state).not.toBe('completed');
  });
});
