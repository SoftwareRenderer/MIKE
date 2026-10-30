import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


// ============================================================
// CHAT INPUT — ESCAPE REMOVES FOCUS (Task View)
// ============================================================
// What only a browser answers here: the blur must not reach CardDetail's window-level
// handler, so the SECOND Esc is the "back to board" one, and an Esc landing on an in-flight
// turn blurs without cancelling it. What the input itself does with the key — blur first,
// keep the draft, close the @ mention dropdown before anything else, never stop — is
// `components/__tests__/chat-form-escape.unit.svelte.ts`.

test.describe.serial('Chat input Escape removes focus', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
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
