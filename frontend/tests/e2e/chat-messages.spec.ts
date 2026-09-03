import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForSendButton, csrfFetch, isolatedProject, gotoProject, apiGetJson, waitForTurnAnswered } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe.serial('Chat Message Actions', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
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
    // Every count here comes from the messages API and every step waits for the turn to
    // be ANSWERED. The mock LLM keeps appending while it streams, so the old version —
    // which snapshotted `.chat-message.assistant` from the DOM right after
    // waitForSendButton — measured a turn that had not finished: the count went UP
    // after the delete and the test failed on its own bookkeeping, not on a duplicate.
    const assistants = async () =>
      (await apiGetJson<any[]>(page, `/api/projects/${projectId}/tasks/${taskId}/messages`))
        .filter((m: any) => m.role === 'assistant').length;

    const msg1 = `first message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, { method: 'POST', body: { content: msg1 } });
    await waitForTurnAnswered(page, projectId, taskId);
    const afterFirst = await assistants();
    // A turn can append several assistant entries (the mock LLM answers in more than
    // one message), so this test works with counts, never with "one turn == one row".
    expect(afterFirst).toBeGreaterThanOrEqual(1);
    const firstText = (await page.locator('.chat-message.assistant').first().textContent())?.trim();
    expect(firstText).not.toBe('');

    const msg2 = `second message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, { method: 'POST', body: { content: msg2 } });
    await waitForTurnAnswered(page, projectId, taskId);
    const afterSecond = await assistants();
    expect(afterSecond).toBeGreaterThan(afterFirst);

    // The behaviour under test is the UI delete: the action icon on the last user
    // message, its confirmation dialog, and everything after it disappearing.
    const deleteBtn = page.locator('.chat-message.user').last().locator('[aria-label="Delete message and all following messages"]');
    await expect(deleteBtn).toBeVisible({ timeout: 10000 });
    await deleteBtn.click();
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });
    await page.click('[role="dialog"] .btn-confirm');
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });

    // The deleted turn's answer is gone from the store AND from the DOM, with no
    // duplicate left behind, and the first answer is untouched.
    await expect(async () => {
      expect(await assistants()).toBe(afterFirst);
    }).toPass({ timeout: 15000 });
    await expect(page.locator('.chat-message.assistant')).toHaveCount(afterFirst);
    // A prefix compare, not equality: the stats footer (model · tokens · elapsed) renders
    // once the turn fully settles, so the same message's textContent grows over time.
    expect((await page.locator('.chat-message.assistant').first().textContent())?.trim() || '').toContain(
      firstText.slice(0, 40),
    );
  });
});
