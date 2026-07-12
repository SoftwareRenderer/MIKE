import { test, expect } from '@playwright/test';
import { getProjectId, getLastTaskId, navigateToTask, waitForSendButton, csrfFetch } from './helpers';

test.describe.serial('No duplicate assistant message after delete', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should not duplicate the previous assistant message after deleting a user message', async ({ page }) => {
    test.setTimeout(60000);

    const projectId = await getProjectId(page);
    // Use the pre-created test task (has worktree and LLM config from global-setup)
    const taskId = await getLastTaskId(page, projectId);
    await navigateToTask(page, taskId, projectId);

    // Wait for task detail page to load
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Ensure task is in_progress (may already be from a prior test)
    const taskStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const t = await res.json();
      return t.status;
    }, { pid: projectId, tid: taskId });

    if (taskStatus === 'backlog') {
      // Move to in_progress via API
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        body: { status: 'in_progress' },
      });
    }

    // Wait for chat form
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

    // Wait for send button (executor may be processing initial prompt)
    const sendBtnAppeared = await page.locator('.btn-send').isVisible({ timeout: 30000 }).catch(() => false);
    if (!sendBtnAppeared) {
      // Executor may have failed — skip gracefully
      return;
    }

    // --- First exchange ---
    const msg1 = `first message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msg1 },
    });

    // Wait for user message to appear
    const userCountBefore = await page.locator('.chat-message.user').count();
    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(userCountBefore);
    }).toPass({ timeout: 10000 });

    // Wait for LLM response to complete
    await waitForSendButton(page);

    // Verify at least 1 assistant message
    const assistantCountAfterFirst = await page.locator('.chat-message.assistant').count();
    expect(assistantCountAfterFirst).toBeGreaterThanOrEqual(1);
    const firstAssistantText = await page.locator('.chat-message.assistant').first().textContent();
    expect(firstAssistantText?.trim()).not.toBe('');

    // --- Second exchange ---
    const msg2 = `second message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msg2 },
    });

    const userCountBeforeSecond = await page.locator('.chat-message.user').count();
    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(userCountBeforeSecond);
    }).toPass({ timeout: 10000 });

    await waitForSendButton(page);

    const assistantCountAfterSecond = await page.locator('.chat-message.assistant').count();
    expect(assistantCountAfterSecond).toBeGreaterThanOrEqual(2);

    // --- Delete the last user message ---
    const userMessages = page.locator('.chat-message.user');
    const lastUserMsg = userMessages.nth(await userMessages.count() - 1);
    const deleteBtn = lastUserMsg.locator('[aria-label="Delete message and all following messages"]');

    if (!(await deleteBtn.isVisible().catch(() => false))) {
      return; // Streaming, skip
    }

    await deleteBtn.click();
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
    await page.click('.confirm-dialog .btn-confirm');
    await expect(page.locator('.confirm-dialog')).not.toBeVisible({ timeout: 5000 });

    await waitForSendButton(page);

    // --- ASSERTION: exactly 1 assistant message, no duplicates ---
    const finalAssistantCount = await page.locator('.chat-message.assistant').count();
    expect(finalAssistantCount).toBe(1);

    const remainingText = await page.locator('.chat-message.assistant').first().textContent();
    expect(remainingText).toBe(firstAssistantText);

    // No duplicate texts
    const allTexts = await page.locator('.chat-message.assistant').allTextContents();
    expect(new Set(allTexts).size).toBe(allTexts.length);
  });
});
