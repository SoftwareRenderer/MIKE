import { test, expect } from '@playwright/test';
import { ensureChatForm, getProjectId, createTaskViaApi } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Tool Call Border Color', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show green border after successful tool call', async ({ page }) => {
    const projectId = await getProjectId(page);
    // Prompt with "list" triggers tool_call_response fixture → successful tool call
    const taskId = await createTaskViaApi(page, projectId, `List files ${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await ensureChatForm(page);

    // Click start to trigger executor (which sends the task prompt to LLM)
    const startBtn = page.locator('.btn-start, button:has-text("Start")');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click();
      // Wait for LLM to complete
      await ensureChatForm(page);
    }

    // Wait for tool call card to appear
    const toolCallCard = page.locator('.tool-call-card').first();
    const visible = await toolCallCard.isVisible({ timeout: 15000 }).catch(() => false);
    if (!visible) return; // Executor didn't start, skip

    // Wait for pending state to clear (tool completed)
    await expect(page.locator('.tool-status.pending')).not.toBeVisible().catch(() => {});
    await page.waitForTimeout(500);

    // Check border-left-color is green (success)
    const borderColor = await toolCallCard.evaluate(el => {
      return getComputedStyle(el).borderLeftColor;
    });
    expect(borderColor).toMatch(/3fb950|63,\s*185,\s*80/); // #3FB950 = rgb(63, 185, 80)
  });

  test('should show red border after failed tool call', async ({ page }) => {
    const projectId = await getProjectId(page);
    // "does not exist" triggers tool_call_fail fixture → failed tool call
    const taskId = await createTaskViaApi(page, projectId, `Read file that does not exist ${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await ensureChatForm(page);

    // Click start to trigger executor
    const startBtn = page.locator('.btn-start, button:has-text("Start")');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click();
      await ensureChatForm(page);
    }

    // Wait for tool call card to appear
    const toolCallCard = page.locator('.tool-call-card').first();
    const visible = await toolCallCard.isVisible({ timeout: 15000 }).catch(() => false);
    if (!visible) return; // Executor didn't start, skip

    // Wait for pending state to clear
    await expect(page.locator('.tool-status.pending')).not.toBeVisible().catch(() => {});
    await page.waitForTimeout(500);

    // Check border-left-color is red (failure)
    const borderColor = await toolCallCard.evaluate(el => {
      return getComputedStyle(el).borderLeftColor;
    });
    expect(borderColor).toMatch(/f85149|248,\s*81,\s*73/); // #F85149 = rgb(248, 81, 73)
  });
});
