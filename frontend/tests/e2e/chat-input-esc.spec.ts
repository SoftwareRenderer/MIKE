import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


// ============================================================
// CHAT INPUT — ESCAPE REMOVES FOCUS (Task View)
// ============================================================
// With the input focused and no mention dropdown open, Esc blurs it: it must not close
// the task view or clear the draft. With the dropdown open, Esc closes the dropdown and
// keeps focus. Esc never stops generation — only the Stop button does. So the SECOND Esc
// is the "back to board" one.

test.describe.serial('Chat input Escape removes focus', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
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

  test('Escape while streaming blurs the input and never stops generation', async ({ page }) => {
    test.setTimeout(60000);
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `chat-esc-streaming-${Date.now()}`);
    await navigateToTask(page, taskId);
    expect(await ensureTaskStarted(page, projectId, taskId)).toBe(true);

    const chatInput = page.locator('#chat-message-input');
    await expect(chatInput).toBeVisible({ timeout: 10000 });

    // Escape must land on an in-flight turn, so wait for streaming (Stop button) first.
    const streaming = await page.locator('.btn-stop').waitFor({ state: 'visible', timeout: 15000 }).then(() => true).catch(() => false);
    test.skip(!streaming, 'initial turn was not streaming');

    await chatInput.click();
    await expect(chatInput).toBeFocused();

    await chatInput.press('Escape');

    // Blurred, task view still open — and the turn was NOT cancelled.
    expect(await page.evaluate(() => document.activeElement?.id)).not.toBe('chat-message-input');
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // The turn runs to completion on its own. A cancelled turn ends on the
    // user message, so ending on a non-user message proves it kept running.
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    const last = page.locator('.chat-message').last();
    await expect(last).toBeVisible();
    await expect(last).not.toHaveClass(/user/);
  });

  test('the next Escape (input blurred) is the "back to board" one', async ({ page }) => {
    test.setTimeout(60000);
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `chat-esc-back-to-board-${Date.now()}`);
    await navigateToTask(page, taskId);
    expect(await ensureTaskStarted(page, projectId, taskId)).toBe(true);

    const chatInput = page.locator('#chat-message-input');
    await expect(chatInput).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    await chatInput.click();
    await expect(chatInput).toBeFocused();

    // Press 1 — consumed by the chat input: blur only, task view stays open.
    await page.keyboard.press('Escape');
    expect(await page.evaluate(() => document.activeElement?.id)).not.toBe('chat-message-input');
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Press 2 — focus is outside any input, so CardDetail's window-level
    // handler closes the task view (back to the board).
    await page.keyboard.press('Escape');
    await expect(page.locator('.task-detail-page')).not.toBeVisible({ timeout: 5000 });
    await expect(page.locator('.board')).toBeVisible();
  });
});
