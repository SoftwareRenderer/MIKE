import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted } from './helpers';

// ============================================================
// CHAT INPUT — ESCAPE REMOVES FOCUS (Task View)
// ============================================================
// With the chat input focused (not streaming, mention dropdown
// closed), Esc blurs the input — it must NOT close the task view
// or clear the draft text. Esc while the @ mention dropdown is
// open closes the dropdown first and keeps focus in the input.
// Esc while streaming still stops generation (see chat-core.spec.ts).

test.describe.serial('Chat input Escape removes focus', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('Escape removes focus from the chat input and keeps the task open', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `chat-esc-blur-${Date.now()}`);
    await navigateToTask(page, taskId);
    expect(await ensureTaskStarted(page, projectId, taskId)).toBe(true);

    const chatInput = page.locator('#chat-message-input');
    await expect(chatInput).toBeVisible({ timeout: 10000 });
    // Wait until idle (send button) — while the initial prompt is
    // streaming, Esc stops generation and re-focuses the input.
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    await chatInput.click();
    await expect(chatInput).toBeFocused();
    await chatInput.fill('draft before escape');

    await chatInput.press('Escape');

    // Focus has left the chat input
    expect(await page.evaluate(() => document.activeElement?.id)).not.toBe('chat-message-input');
    // The draft text is preserved
    await expect(chatInput).toHaveValue('draft before escape');
    // The task view stayed open — the blurred Esc event must not reach
    // CardDetail's window-level Esc handler (which would close the task)
    await expect(page.locator('.task-detail-page')).toBeVisible();
  });

  test('Escape closes the mention dropdown first and keeps focus in the input', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `chat-esc-mention-${Date.now()}`);
    await navigateToTask(page, taskId);
    expect(await ensureTaskStarted(page, projectId, taskId)).toBe(true);

    // Deterministic mention results (the task worktree may not exist yet)
    await page.route('/api/fs/search*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ results: [{ path: 'README.md' }] }),
      }),
    );

    const chatInput = page.locator('#chat-message-input');
    await expect(chatInput).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    await chatInput.click();
    // Playwright type() doesn't always fire Svelte oninput
    await chatInput.evaluate((el) => {
      const ta = el as HTMLTextAreaElement;
      ta.value = '@';
      ta.setSelectionRange(1, 1);
      ta.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await expect(page.locator('.mention-dropdown')).toBeVisible({ timeout: 5000 });

    await chatInput.press('Escape');

    await expect(page.locator('.mention-dropdown')).toBeHidden();
    await expect(chatInput).toBeFocused();
  });
});
