import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForChatFormReady } from './helpers';

test.describe.configure({ mode: 'parallel' });

/** Wait for chat form to be idle (send button visible). Stops any active LLM response. */
async function waitForIdle(page: any) {
  await waitForChatFormReady(page);
  // If stop button is visible, click it and wait for send button to reappear
  if (await page.locator('.btn-stop').isVisible().catch(() => false)) {
    await page.locator('.btn-stop').click();
    // Wait up to 60s for send button (LLM mock can be slow)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 60000 });
  }
}

/** Wait for the chat textarea to be available. Does NOT stop LLM responses. */
async function waitForTextarea(page: any) {
  await expect(page.locator('textarea[placeholder*="message"]')).toBeVisible({ timeout: 30000 });
}

/** Open a task in in_progress state and wait for the chat form to be ready. */
async function openReadyTask(page: any, projectId: string, prefix = 'draft-test'): Promise<string> {
  const taskId = await createTaskViaApi(page, projectId, `${prefix}-${Date.now()}`);
  await navigateToTask(page, taskId, projectId);
  await ensureTaskStarted(page, projectId, taskId);
  await page.reload();
  await expect(page.locator('.task-detail-page')).toBeVisible();
  // Wait for textarea instead of idle (LLM mock stop can be unreliable)
  await waitForTextarea(page);
  return taskId;
}

// ─── Text persistence tests ────────────────────────────────────────────────

test.describe('Chat draft text persistence', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item');
  });

  test('text entered in chat input survives page reload', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await openReadyTask(page, projectId);

    const textarea = page.locator('textarea[placeholder*="message"]');
    const testMessage = 'hello world draft test';
    await textarea.click();
    await textarea.pressSequentially(testMessage); // Simulates key-by-key input
    await expect(textarea).toHaveValue(testMessage);
    // Wait for debounced save to IndexedDB
    await page.waitForTimeout(500);

    // Reload the page
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForIdle(page);

    // Verify text persisted in textarea
    await expect(textarea).toHaveValue(testMessage);
  });

  test('each task has its own independent draft', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskIdA = await openReadyTask(page, projectId);

    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.click();
    await textarea.pressSequentially('draft for task A');
    await expect(textarea).toHaveValue('draft for task A');
    // Wait for debounced IndexedDB write (100ms debounce + DB write time)
    await page.waitForTimeout(500);

    // Create and navigate to a second task
    const taskIdB = await createTaskViaApi(page, projectId, `draft-test-b-${Date.now()}`);
    await navigateToTask(page, taskIdB, projectId);
    await ensureTaskStarted(page, projectId, taskIdB);
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForTextarea(page);

    // Task B should have empty input (no draft carried over from A)
    await expect(textarea).toHaveValue('');

    // Navigate back to task A — its draft should still be there
    await navigateToTask(page, taskIdA, projectId);
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForTextarea(page);

    await expect(textarea).toHaveValue('draft for task A');
  });

  test('draft is cleared after successful send', async ({ page }) => {
    const projectId = await getProjectId(page);
    await openReadyTask(page, projectId);

    const textarea = page.locator('textarea[placeholder*="message"]');
    const testMessage = 'message to send';
    // Type message to set draft
    await textarea.click();
    await textarea.pressSequentially(testMessage);
    await expect(textarea).toHaveValue(testMessage);
    // Wait for debounced save to IndexedDB
    await page.waitForTimeout(500);

    // Send via the send button
    await page.locator('.btn-send').click();
    // Chat form hides after send (showRestartButton becomes true).
    // Draft is cleared by onClearDraft callback.
    await page.waitForTimeout(500);

    // Reload and verify draft was cleared (textarea should be empty)
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForTextarea(page);
    await expect(textarea).toHaveValue('');
  });

  test('draft is cleared when navigating to a deleted task', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await openReadyTask(page, projectId);

    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.click();
    await textarea.pressSequentially('draft for soon-to-be-deleted task');
    await page.waitForTimeout(500);
    await expect(textarea).toHaveValue('draft for soon-to-be-deleted task');

    // Delete the task via API
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}`, { method: 'DELETE' });
    }, { pid: projectId, tid: taskId });

    // Navigate to the deleted task URL
    await page.evaluate((tid) => { window.location.href = `/?task=${tid}`; }, taskId);

    // Should show a 404 or task-not-found state — wait for page to stabilize
    await page.waitForLoadState('networkidle').catch(() => {});
    await page.waitForTimeout(1000);

    // Navigate to any other task — the stale draft should have been cleaned
    const otherTaskId = await createTaskViaApi(page, projectId, `after-delete-${Date.now()}`);
    await navigateToTask(page, otherTaskId, projectId);
    await ensureTaskStarted(page, projectId, otherTaskId);
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForTextarea(page);

    // The other task should NOT have the deleted task's draft
    await expect(textarea).toHaveValue('');
  });
});


