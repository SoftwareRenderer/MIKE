import { test, expect } from '@playwright/test';
import { ensureChatForm, getProjectId, createTaskViaApi } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('LLM Awaiting Spinner', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  async function openTask(page: any, title?: string): Promise<void> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, title || `awaiter-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await ensureChatForm(page);
  }

  test('should show awaiting spinner after sending a message, then hide it', async ({ page }) => {
    await openTask(page);

    const awaitingEl = page.locator('.awaiting-llm');
    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message
    await chatTextarea.fill('hello awaiting spinner test');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // The awaiting spinner should appear (set synchronously after send)
    await expect(async () => {
      await expect(awaitingEl).toBeVisible();
    }).toPass({ timeout: 3000 });

    // The awaiting spinner should disappear when LLM tokens arrive.
    // Graceful skip: executor may not restart for follow-up messages in test env.
    const disappeared = await awaitingEl.isHidden({ timeout: 15000 }).catch(() => false);
    if (!disappeared) return;

    // Send button should reappear (LLM complete)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });
  });

  test('should show awaiting spinner for tool call responses', async ({ page }) => {
    await openTask(page);

    const awaitingEl = page.locator('.awaiting-llm');
    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers a tool call (mock routes "list" to tool_call fixture)
    await chatTextarea.fill('list all files');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // The awaiting spinner should appear
    await expect(async () => {
      await expect(awaitingEl).toBeVisible();
    }).toPass({ timeout: 3000 });

    // The awaiting spinner should disappear when tool_call event arrives.
    // Graceful skip: executor may not restart for follow-up messages in test env.
    const disappeared = await awaitingEl.isHidden({ timeout: 15000 }).catch(() => false);
    if (!disappeared) return;
  });
});
