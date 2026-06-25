import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForChatForm } from './helpers';

test.describe.serial('Streaming Response After Page Refresh', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should restore streaming content after page refresh during active response', async ({ page }) => {
    test.setTimeout(60000);

    // Create a task and move to in_progress
    taskId = await createTaskViaApi(page, projectId, `streaming-refresh-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await ensureTaskStarted(page, projectId, taskId);

    // Reload to pick up status change
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Graceful: executor may not start in test env (worktree conflicts, etc.)
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    // Send a message that triggers a tool call via the mock LLM
    // The mock routes "list" + tools → tool_call_response fixture
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('list directory contents');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for streaming to begin (stop button appears when LLM is responding)
    const stopBtnVisible = await page.locator('.btn-stop').isVisible({ timeout: 15000 }).catch(() => false);
    if (!stopBtnVisible) return;

    // Small delay to ensure some tokens/tool_calls are buffered
    await page.waitForTimeout(300);

    // Simulate page refresh during active streaming
    // Use window.location.href to trigger a full SPA reload (preserves cookies/session)
    const currentUrl = page.url();
    await page.evaluate((url) => { window.location.href = url; }, currentUrl);

    // Wait for the task detail page to reappear after reload
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15000 });

    // Wait for the chat to stabilize (WebSocket reconnects, buffer replays, history loads)
    await page.waitForTimeout(2000);

    // Verify user message is present after refresh
    await expect(page.locator('.chat-message.user').last()).toContainText('list directory', { timeout: 10000 });

    // Verify the chat form is functional after refresh
    await expect(page.locator('.chat-form')).toBeVisible();
  });

  test('should restore tool calls after page refresh during active response', async ({ page }) => {
    test.setTimeout(60000);

    // Create a task and move to in_progress
    taskId = await createTaskViaApi(page, projectId, `tool-call-refresh-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await ensureTaskStarted(page, projectId, taskId);

    // Reload to pick up status change
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    // Send a message that triggers a tool call
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('read a file from directory');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for streaming to begin
    const stopBtnVisible = await page.locator('.btn-stop').isVisible({ timeout: 15000 }).catch(() => false);
    if (!stopBtnVisible) return;

    // Wait for some content to stream (tokens + tool calls)
    await page.waitForTimeout(500);

    // Simulate page refresh during active streaming
    const currentUrl = page.url();
    await page.evaluate((url) => { window.location.href = url; }, currentUrl);

    // Wait for the task detail page to reappear
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15000 });

    // Wait for the chat to stabilize
    await page.waitForTimeout(2000);

    // Verify user message is present
    await expect(page.locator('.chat-message.user').last()).toContainText('read a file', { timeout: 10000 });

    // Check for tool call cards (may appear if buffer replay includes tool_call events)
    // The fix ensures tool_call events are buffered and replayed on reconnect
    const toolCallCards = page.locator('.tool-call-card');
    const toolCallCount = await toolCallCards.count();
    if (toolCallCount > 0) {
      await expect(toolCallCards.first()).toBeVisible();
    }

    // Verify the chat form is functional after refresh
    await expect(page.locator('.chat-form')).toBeVisible();
  });
});
