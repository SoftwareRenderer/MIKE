import { test, expect } from '@playwright/test';
import { createAndOpenTask, getProjectId, ensureChatForm } from './helpers';

test.describe.serial('Executor Context (task_messages)', () => {
  let taskId: string | null = null;
  let projectId: string | null = null;

  test('should not include deleted messages in task_messages', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Create a fresh task and open it
    const taskTitle = `context_test_${Date.now()}`;
    await createAndOpenTask(page, taskTitle);

    // Capture IDs
    taskId = (await page.url()).match(/task=([^&]+)/)?.[1] || null;
    projectId = await getProjectId(page);
    expect(taskId).toBeTruthy();
    expect(projectId).toBeTruthy();

    // Wait for chat form to be ready (clicks "Start Task" if needed, waits for send button)
    await ensureChatForm(page, 15000);

    const textarea = page.locator('textarea[placeholder*="message"]');

    // Send first message
    const firstMsg = `first_message_${Date.now()}`;
    await textarea.fill(firstMsg);
    await textarea.press('Enter');
    // Wait for textarea to be cleared (message sent)
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for message to be persisted (WebSocket round-trip + DB write)
    await page.waitForTimeout(2000);

    // Verify first message is in task_messages via API
    let apiRes = await page.request.get(`http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages`);
    let messages = await apiRes.json() as any[];
    const firstMsgInDb = messages.find((m: any) => m.role === 'user' && m.content === firstMsg);
    expect(firstMsgInDb, 'First message should be in task_messages').toBeTruthy();

    // Delete the first message - find the LAST user message and delete it
    const userMsgs = page.locator('.chat-message.user');
    await expect(userMsgs.last()).toBeVisible({ timeout: 10000 });
    await userMsgs.last().locator('.action-btn[title="Delete"]').click();
    await page.locator('.delete-dialog .btn-delete').click();
    await expect(page.locator('.delete-dialog')).not.toBeVisible({ timeout: 5000 });

    // Wait for deletion to propagate
    await page.waitForTimeout(1000);

    // Verify first message is removed from task_messages via API
    apiRes = await page.request.get(`http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages`);
    messages = await apiRes.json() as any[];
    const deletedMsgInDb = messages.find((m: any) => m.role === 'user' && m.content === firstMsg);
    expect(deletedMsgInDb, 'Deleted message should NOT be in task_messages').toBeFalsy();

    // Send a second message
    await textarea.fill(`second_message_${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await page.waitForTimeout(2000);

    // Verify task_messages has the second message but NOT the deleted first message
    apiRes = await page.request.get(`http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages`);
    messages = await apiRes.json() as any[];
    const userMessagesInDb = messages.filter((m: any) => m.role === 'user');
    const hasDeleted = userMessagesInDb.some((m: any) => m.content === firstMsg);
    const hasSecond = userMessagesInDb.some((m: any) => m.content?.startsWith('second_message_'));
    expect(hasDeleted, 'task_messages should NOT contain deleted message').toBeFalsy();
    expect(hasSecond, 'task_messages should contain second message').toBeTruthy();

    // Reload page and verify the deleted message does not reappear in the chat
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 15000 });
    await page.waitForTimeout(2000);

    const chatContent = await page.locator('.chat-messages').textContent();
    expect(chatContent, 'Chat should NOT show deleted message after reload').not.toContain(firstMsg);
  });
});
