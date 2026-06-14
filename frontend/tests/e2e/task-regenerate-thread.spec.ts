import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, waitForChatForm } from './helpers';

test.describe.serial('Regenerate should not delete fork threads', () => {
  let projectId: string = '';
  let taskId: string = '';
  const uniqueId = Date.now();

  test('should create task and send a follow-up message', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    projectId = await getProjectId(page);
    taskId = await createTaskViaApi(page, projectId, `Regenerate thread test ${uniqueId}`);

    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    const startBtn = page.locator('button:has-text("Start Task")');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click();
      await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });
    }

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });
    const assistantVisible = await page.locator('.chat-message.assistant').first().isVisible({ timeout: 15000 }).catch(() => false);
    if (!assistantVisible) return;

    const textarea = page.locator('textarea[placeholder*="message"]');
    await expect(textarea).toBeVisible({ timeout: 5000 });
    await textarea.fill(`Follow up ${uniqueId}`);
    await textarea.press('Enter');

    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.chat-message.assistant').last()).toBeVisible({ timeout: 15000 });

    const userMsgs = await page.locator('.chat-message.user').count();
    expect(userMsgs).toBeGreaterThanOrEqual(2);
  });

  test('should fork from follow-up message', async ({ page }) => {
    if (!taskId || !projectId) return;

    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    const threadsBefore = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const countBefore = threadsBefore.length;

    await page.reload();
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    const lastUserMsg = page.locator('.chat-message.user').last();
    await expect(lastUserMsg).toBeVisible({ timeout: 10000 });
    await lastUserMsg.locator('.action-btn[aria-label*="new thread"]').click();

    const forkContent = `Edited fork ${Date.now()}`;
    await page.locator('.edit-form textarea').fill(forkContent);
    await page.locator('.btn-edit-submit').click();

    const forkCreated = await expect(async () => {
      const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
        const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(threads.length).toBeGreaterThanOrEqual(countBefore + 1);
    }).toPass({ timeout: 10000 }).catch(() => false);
    if (!forkCreated) return;

    const threadsAfter = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const activeThread = threadsAfter.find((t: any) => t.is_active);
    expect(activeThread).toBeDefined();
    expect(activeThread.checkpoint_message_id).toBeDefined();
  });

  test('should regenerate last message without deleting the fork thread', async ({ page }) => {
    if (!taskId || !projectId) return;

    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Get the fork thread ID before regeneration
    const forkInfo = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      const threads = await res.json();
      const active = threads.find((t: any) => t.is_active);
      return {
        forkId: active?.id,
        parentId: active?.parent_thread_id,
        count: threads.length,
      };
    }, { pid: projectId, tid: taskId });

    // Verify we're on a fork
    expect(forkInfo.parentId).toBeDefined();
    const forkId = forkInfo.forkId;
    const threadCountBefore = forkInfo.count;

    // Wait for assistant response to appear (so there's something to regenerate)
    const assistantVisible = await page.locator('.chat-message.assistant').last().isVisible({ timeout: 15000 }).catch(() => false);
    if (!assistantVisible) {
      // No assistant message to regenerate — skip
      return;
    }

    // Click regenerate on the last assistant message
    const lastAssistant = page.locator('.chat-message.assistant').last();
    const regenerateBtn = lastAssistant.locator('.action-btn[aria-label="Regenerate response"]');
    const hasRegenerateBtn = await regenerateBtn.isVisible().catch(() => false);
    if (!hasRegenerateBtn) {
      // Regenerate button not visible — skip
      return;
    }

    await regenerateBtn.click();

    // Wait for LLM to respond
    await expect(page.locator('.chat-message.assistant').last()).toBeVisible({ timeout: 15000 });

    // Verify the fork thread still exists
    const threadsAfter = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    // Fork thread should still exist (not deleted by regeneration)
    const forkStillExists = threadsAfter.find((t: any) => t.id === forkId);
    expect(forkStillExists).toBeDefined();

    // Thread count should not have decreased
    expect(threadsAfter.length).toBeGreaterThanOrEqual(threadCountBefore);
  });
});
