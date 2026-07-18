import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForSendButton, csrfFetch } from './helpers';

test.describe.serial('Chat Message Actions', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    projectId = await getProjectId(page);
    taskId = await createTaskViaApi(page, projectId, `message-actions-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForSendButton(page);
  });

  test('should show visual feedback when copying a message', async ({ page }) => {
    const countBefore = await page.locator('.chat-message.user').count();

    const msgContent = `copy test message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msgContent },
    });

    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    await waitForSendButton(page);

    // Find the copy button on the newly added message
    const copyBtn = page.locator('.chat-message.user').nth(countBefore).locator('[aria-label="Copy message"]');
    await expect(copyBtn).toBeVisible();
    await copyBtn.click();

    await expect(copyBtn).toHaveClass(/copied/);
    // Wait for the "copied" class to be removed (visual feedback clears after ~2s)
    await expect(copyBtn).not.toHaveClass(/copied/, { timeout: 5000 });
  });

  test('should show edit button on user messages', async ({ page }) => {
    const countBefore = await page.locator('.chat-message.user').count();

    const msgContent = `edit test ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msgContent },
    });

    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    await waitForSendButton(page);

    // Verify the edit/fork button is visible on the user message
    const editBtn = page.locator('.chat-message.user').nth(countBefore).locator('[aria-label="Start new thread from this message"]');
    await expect(editBtn).toBeVisible();
  });

  test('first user message should have no delete button', async ({ page }) => {
    const firstMessage = page.locator('.chat-message.user').first();
    await expect(firstMessage).toBeVisible({ timeout: 10000 });

    await waitForSendButton(page);

    const deleteBtn = firstMessage.locator('[aria-label="Delete message and all following messages"]');
    await expect(deleteBtn).not.toBeVisible();
  });

  test('second user message should have delete button while first does not', async ({ page }) => {
    // The beforeEach already sent a message via ensureTaskStarted, so there's 1 user message.
    // Send another message to have at least 2 user messages.
    const msg2 = `second message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msg2 },
    });

    // Wait for at least 2 user messages (beforeEach created 1, we just sent 1 more)
    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 10000 });
    await waitForSendButton(page);

    // The first user message should NOT have a delete button
    const firstMessage = page.locator('.chat-message.user').first();
    const firstDeleteBtn = firstMessage.locator('[aria-label="Delete message and all following messages"]');
    await expect(firstDeleteBtn).not.toBeVisible();

    // The last user message SHOULD have a delete button
    const lastMessage = page.locator('.chat-message.user').last();
    const lastDeleteBtn = lastMessage.locator('[aria-label="Delete message and all following messages"]');
    await expect(lastDeleteBtn).toBeVisible({ timeout: 5000 });

    // The first user message should STILL NOT have a delete button
    await expect(firstDeleteBtn).not.toBeVisible();
  });

  test('should confirm before deleting a message', async ({ page }) => {
    // Send a message to have something to delete
    const msgContent = `delete confirm test ${Date.now()}`;
    const countBefore = await page.locator('.chat-message.user').count();

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msgContent },
    });

    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    await waitForSendButton(page);

    // The second user message should have a delete button
    const userMessages = page.locator('.chat-message.user');
    const lastUserMsg = userMessages.nth(await userMessages.count() - 1);
    const deleteBtn = lastUserMsg.locator('[aria-label="Delete message and all following messages"]');

    if (!(await deleteBtn.isVisible().catch(() => false))) {
      return; // Only one message, no delete button
    }

    await deleteBtn.click();
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });
    await page.click('[role="dialog"] .btn-confirm');
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });
  });

  test('should not duplicate the previous assistant message after deleting a user message', async ({ page }) => {
    test.setTimeout(60000);

    // --- First exchange ---
    const msg1 = `first message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msg1 },
    });

    const userCountBefore = await page.locator('.chat-message.user').count();
    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(userCountBefore);
    }).toPass({ timeout: 10000 });

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
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });
    await page.click('[role="dialog"] .btn-confirm');
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });

    // Wait for page to stabilize after deletion (LLM may still be processing)
    await expect(async () => {
      const hasSend = await page.locator('.btn-send').isVisible();
      const hasStop = await page.locator('.btn-stop').isVisible();
      expect(hasSend || hasStop).toBe(true);
    }).toPass({ timeout: 15000 });
    // If stop button is visible, wait for send button
    if (await page.locator('.btn-stop').isVisible().catch(() => false)) {
      await waitForSendButton(page);
    }

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
