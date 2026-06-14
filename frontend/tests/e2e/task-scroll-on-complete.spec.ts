import { test, expect } from '@playwright/test';
import { ensureChatForm, createAndOpenTask } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Task Scroll on Complete', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should scroll to bottom after LLM response completes', async ({ page }) => {
    // Create and open a task — ensureChatForm handles Start Task + waits for LLM idle
    await createAndOpenTask(page, `scroll-test ${Date.now()}`);
    await ensureChatForm(page);

    // Send a message that triggers a text response (default mock route)
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('hello world');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for send button to reappear (LLM complete, streaming stopped)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });

    // Wait a moment for double requestAnimationFrame to settle
    await page.waitForTimeout(200);

    // Verify the chat container is scrolled to (or very near) the bottom
    const isAtBottom = await page.evaluate(() => {
      const el = document.querySelector('.chat-messages') as HTMLElement | null;
      if (!el) return false;
      const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
      // Allow up to 50px tolerance for rendering differences
      return gap < 50;
    });
    expect(isAtBottom).toBe(true);
  });

  test('should scroll to bottom after tool call task completes', async ({ page }) => {
    // Create and open a task
    await createAndOpenTask(page, `scroll-tool-test ${Date.now()}`);
    await ensureChatForm(page);

    // Send a message that triggers tool calls (mock routes "list files" to tool_call_response)
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Please list all files');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for send button to reappear (LLM complete after tool calls)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 20000 });

    // Wait a moment for double requestAnimationFrame to settle
    await page.waitForTimeout(200);

    // Verify the chat container is scrolled to (or very near) the bottom
    const isAtBottom = await page.evaluate(() => {
      const el = document.querySelector('.chat-messages') as HTMLElement | null;
      if (!el) return false;
      const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
      // Allow up to 50px tolerance for rendering differences
      return gap < 50;
    });
    expect(isAtBottom).toBe(true);
  });
});
