import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForSendButton, csrfFetch } from './helpers';

test.describe.serial('Message copy and edit actions', () => {
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
    // Send a message via API
    const msgContent = `copy test message ${Date.now()}`;
    const countBefore = await page.locator('.chat-message.user').count();
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msgContent },
    });

    // Wait for the new user message to appear (count increases)
    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    // Wait for LLM response to complete
    await waitForSendButton(page);

    // The new message is at index `countBefore` (0-based, chronological order)
    const msgIdx = countBefore;
    const copyBtn = page.locator('.chat-message.user').nth(msgIdx).locator('[aria-label="Copy message"]');
    await expect(copyBtn).toBeVisible();

    // Click copy
    await copyBtn.click();

    // The button should show the .copied class (checkmark feedback)
    await expect(copyBtn).toHaveClass(/copied/);

    // After the timeout, the copied class should be removed
    await page.waitForTimeout(2500);
    await expect(copyBtn).not.toHaveClass(/copied/);
  });

  test('should open edit form with pre-filled content on user message', async ({ page }) => {
    // Send a message via API
    const msgContent = `edit me please ${Date.now()}`;
    const countBefore = await page.locator('.chat-message.user').count();
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msgContent },
    });

    // Wait for the new user message to appear
    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    // Wait for LLM response to complete
    await waitForSendButton(page);

    // The new message is at index `countBefore` (0-based, chronological order)
    const msgIdx = countBefore;

    // Click the edit/fork button on the newest user message
    const editBtn = page.locator('.chat-message.user').nth(msgIdx).locator('[aria-label="Start new thread from this message"]');
    await expect(editBtn).toBeVisible();
    await editBtn.click();

    // Wait for the edit form to appear with action buttons
    await expect(async () => {
      const result = await page.evaluate((idx) => {
        const msgs = document.querySelectorAll('.chat-message.user');
        const msg = msgs[idx];
        return {
          hasEditForm: !!msg?.querySelector('.edit-form'),
          hasTextarea: !!msg?.querySelector('.edit-form textarea'),
          hasCancel: !!msg?.querySelector('.btn-edit-cancel'),
          hasSubmit: !!msg?.querySelector('.btn-edit-submit'),
        };
      }, msgIdx);
      expect(result.hasEditForm).toBe(true);
      expect(result.hasTextarea).toBe(true);
      expect(result.hasCancel).toBe(true);
      expect(result.hasSubmit).toBe(true);
    }).toPass({ timeout: 5000 });

    // Cancel the edit to clean up state
    await page.evaluate((idx) => {
      const msgs = document.querySelectorAll('.chat-message.user');
      const cancelBtn = msgs[idx]?.querySelector('.btn-edit-cancel');
      cancelBtn?.click();
    }, msgIdx);

    // EditForm should disappear and original message bubble should be restored
    await expect(async () => {
      const result = await page.evaluate((idx) => {
        const msgs = document.querySelectorAll('.chat-message.user');
        const msg = msgs[idx];
        return {
          hasBubble: !!msg?.querySelector('.message-bubble'),
          hasEditForm: !!msg?.querySelector('.edit-form'),
          text: msg?.querySelector('.message-text')?.textContent || '',
        };
      }, msgIdx);
      expect(result.hasBubble).toBe(true);
      expect(result.hasEditForm).toBe(false);
      expect(result.text).toContain(msgContent);
    }).toPass({ timeout: 5000 });
  });

  test('should preserve original message when canceling edit', async ({ page }) => {
    // Send a message via API
    const originalContent = `original message ${Date.now()}`;
    const countBefore = await page.locator('.chat-message.user').count();
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: originalContent },
    });

    // Wait for the new user message to appear
    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    // Wait for LLM response to complete
    await waitForSendButton(page);

    // The new message is at index `countBefore` (0-based, chronological order)
    const msgIdx = countBefore;

    // Open the edit form
    const editBtn = page.locator('.chat-message.user').nth(msgIdx).locator('[aria-label="Start new thread from this message"]');
    await editBtn.click();

    // Wait for EditForm to appear
    await expect(async () => {
      const hasEditForm = await page.evaluate((idx) => {
        const msgs = document.querySelectorAll('.chat-message.user');
        const msg = msgs[idx];
        return msg ? !!msg.querySelector('.edit-form') : false;
      }, msgIdx);
      expect(hasEditForm).toBe(true);
    }).toPass({ timeout: 5000 });

    // Modify the content in the textarea
    await page.evaluate((idx) => {
      const msgs = document.querySelectorAll('.chat-message.user');
      const msg = msgs[idx];
      const ta = msg?.querySelector('.edit-form textarea');
      if (ta) ta.value = 'completely different content';
      ta?.dispatchEvent(new Event('input', { bubbles: true }));
    }, msgIdx);

    const modifiedValue = await page.evaluate((idx) => {
      const msgs = document.querySelectorAll('.chat-message.user');
      const msg = msgs[idx];
      const ta = msg?.querySelector('.edit-form textarea');
      return ta ? ta.value : '';
    }, msgIdx);
    expect(modifiedValue).toBe('completely different content');

    // Click cancel
    await page.evaluate((idx) => {
      const msgs = document.querySelectorAll('.chat-message.user');
      const cancelBtn = msgs[idx]?.querySelector('.btn-edit-cancel');
      cancelBtn?.click();
    }, msgIdx);

    // EditForm should disappear
    await expect(async () => {
      const hasEditForm = await page.evaluate((idx) => {
        const msgs = document.querySelectorAll('.chat-message.user');
        const msg = msgs[idx];
        return msg ? !!msg.querySelector('.edit-form') : false;
      }, msgIdx);
      expect(hasEditForm).toBe(false);
    }).toPass({ timeout: 5000 });

    // The original message content should be preserved in the displayed message
    const restoredText = await page.evaluate((idx) => {
      const msgs = document.querySelectorAll('.chat-message.user');
      const msg = msgs[idx];
      return msg?.querySelector('.message-text')?.textContent || '';
    }, msgIdx);
    expect(restoredText).toContain(originalContent);
    expect(restoredText).not.toContain('completely different content');
  });

  test('should preserve file mention (@file) in edit mode', async ({ page }) => {
    // Mock the file search API so @ mentions don't trigger real FS calls
    await page.route('/api/fs/search*', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          root: '/tmp/test-repo',
          results: [{ path: 'README.md' }],
        }),
      });
    });

    // Send a message that contains a file mention
    const msgWithMention = `check @README.md for details ${Date.now()}`;
    const countBefore = await page.locator('.chat-message.user').count();
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msgWithMention },
    });

    // Wait for the new user message to appear
    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    // Wait for LLM response to complete
    await waitForSendButton(page);

    // The new message is at index `countBefore` (0-based, chronological order)
    const msgIdx = countBefore;

    // Open the edit form
    const editBtn = page.locator('.chat-message.user').nth(msgIdx).locator('[aria-label="Start new thread from this message"]');
    await editBtn.click();

    // Wait for EditForm to appear and textarea to be pre-filled with the mention
    await expect(async () => {
      const result = await page.evaluate((idx) => {
        const msgs = document.querySelectorAll('.chat-message.user');
        const msg = msgs[idx];
        const ta = msg?.querySelector('.edit-form textarea');
        return {
          hasEditForm: !!msg?.querySelector('.edit-form'),
          textareaValue: ta ? ta.value : '',
        };
      }, msgIdx);
      expect(result.hasEditForm).toBe(true);
      expect(result.textareaValue).toContain('@README.md');
    }).toPass({ timeout: 5000 });

    // Cancel to clean up
    await page.evaluate((idx) => {
      const msgs = document.querySelectorAll('.chat-message.user');
      const cancelBtn = msgs[idx]?.querySelector('.btn-edit-cancel');
      cancelBtn?.click();
    }, msgIdx);

    await expect(async () => {
      const hasEditForm = await page.evaluate((idx) => {
        const msgs = document.querySelectorAll('.chat-message.user');
        const msg = msgs[idx];
        return msg ? !!msg.querySelector('.edit-form') : false;
      }, msgIdx);
      expect(hasEditForm).toBe(false);
    }).toPass({ timeout: 5000 });
  });

  test('first user message should have no delete button', async ({ page }) => {
    // Wait for the initial user message from task creation to appear
    await expect(page.locator('.chat-message.user')).toHaveCount(1, { timeout: 10000 });

    // Wait for LLM response to complete
    await waitForSendButton(page);

    // The first (and only) user message should NOT have a delete button
    const firstMessage = page.locator('.chat-message.user').first();
    const deleteBtn = firstMessage.locator('[aria-label="Delete message and all following messages"]');
    await expect(deleteBtn).not.toBeVisible();
  });
});
