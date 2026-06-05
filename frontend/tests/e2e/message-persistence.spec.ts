import { test, expect } from '@playwright/test';
import { ensureChatForm, createAndOpenTask, getProjectId } from './helpers';

test.describe.serial('Message Persistence', () => {
  let taskId: string | null = null;
  let projectId: string | null = null;

  test('should persist user message and show it after reload', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Create a fresh task and open it
    await createAndOpenTask(page, `persist_prompt_${Date.now()}`);

    // Capture task ID from URL
    taskId = (await page.url()).match(/task=([^&]+)/)?.[1] || null;
    expect(taskId).toBeTruthy();

    // Get project ID via API
    projectId = await getProjectId(page);
    expect(projectId).toBeTruthy();

    // Wait for chat form to be ready (handles new-task "Start Task" state + LLM response)
    await ensureChatForm(page);

    // Send a follow-up message
    const followUpMessage = `followup_${Date.now()}`;
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(followUpMessage);
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Wait for the message to be persisted (WebSocket round-trip + DB write)
    await page.waitForTimeout(2000);

    // Verify the message is persisted via API
    const apiRes = await page.request.get(`http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages`);
    const apiMessages = await apiRes.json() as any[];
    const userMsgInDb = apiMessages.find((m: any) => m.role === 'user' && m.content === followUpMessage);
    expect(userMsgInDb, 'User message should be persisted to the database').toBeTruthy();

    // Navigate directly to the task URL (more reliable than reload)
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 15000 });
    await page.waitForTimeout(3000);

    // The persisted user message should be visible in the chat
    const chatContent = await page.locator('.chat-messages').textContent();
    expect(chatContent, 'Sent message should be visible after page reload').toContain(followUpMessage);
  });

  test('should show entries (not "Start Task") for tasks with persisted messages', async ({ page }) => {
    if (!taskId || !projectId) {
      test.skip();
    }

    // Navigate directly to the task via URL
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });
    await page.waitForTimeout(2000);

    // Check if the "Start Task" button is visible
    const startButton = page.locator('.btn-start-task');
    const isStartButtonVisible = await startButton.isVisible().catch(() => false);

    // Check if there are user messages in the DB
    const apiRes = await page.request.get(`http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages`);
    const apiMessages = await apiRes.json() as any[];
    const hasUserMessages = apiMessages.some((m: any) => m.role === 'user');

    if (hasUserMessages && isStartButtonVisible) {
      // Bug: Start Task button is shown despite having persisted user messages
      // The chat should show entries, not the Start Task button
      const chatContent = await page.locator('.chat-messages').textContent();
      const showsUserMessages = apiMessages
        .filter((m: any) => m.role === 'user')
        .some((m: any) => chatContent?.includes(m.content));

      expect(showsUserMessages,
        'Chat should show persisted user messages instead of "Start Task" button').toBeTruthy();
    }
    // If Start Task button is not visible, the entries are shown - test passes
  });
});
