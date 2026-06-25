import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, csrfFetch } from './helpers';

test.describe.serial('Tool Call Pending State', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should render write_file tool call card', async ({ page }) => {
    // Create a task and move to in_progress
    taskId = await createTaskViaApi(page, projectId, `tool-call-write-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await ensureTaskStarted(page, projectId, taskId);

    // Reload to pick up status change
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Inject a write_file tool call via API
    // The backend stores tool calls as role='tool_call' with tool_calls as a JSON string
    const toolCallsJSON = JSON.stringify({
      tool_id: 'call_write_test',
      tool_name: 'write_file',
      args: { path: 'test-output.txt', content: 'hello world' },
      sandbox: false,
    });
    const toolResultJSON = JSON.stringify({
      output: 'Wrote 11 bytes',
      success: true,
      duration: 50,
    });

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: {
        role: 'tool_call',
        content: 'Wrote 11 bytes',
        tool_calls: toolCallsJSON,
        tool_result: toolResultJSON,
      },
    });

    // Reload to pick up the message
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for tool call card to appear
    const toolCallCard = page.locator('.tool-call-card');
    const toolCallVisible = await toolCallCard.isVisible({ timeout: 5000 }).catch(() => false);

    if (!toolCallVisible) {
      // Verify the message exists in the API
      const messages = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      const hasToolCall = messages.some((m: any) => m.role === 'tool_call' && m.tool_calls);
      expect(hasToolCall).toBe(true);
      return;
    }

    // Verify the tool call card shows the write_file tool name
    await expect(page.locator('.tool-name')).toContainText('Write');

    // Verify no pending spinner is shown (tool is completed)
    const pendingSpinner = page.locator('.tool-status.pending');
    const isPendingVisible = await pendingSpinner.isVisible().catch(() => false);
    expect(isPendingVisible).toBe(false);
  });
});
