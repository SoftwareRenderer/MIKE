import { test, expect, type Page } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  waitForChatForm,
  sendMessage,
  apiGetConfig,
  apiPutConfig,
  waitForConfig,
} from './helpers';

// Auto-summarization + injected "Proceed" user message.
//
// Runs in the `isolated` project (port 3001): it configures a `context_window`
// on the mock provider model so the executor's auto-summarize threshold is
// reachable, then sends a message that triggers a tool-call turn reporting
// >90% context usage (fixture tool_call_summarize.json: 7500 of 8192 tokens).
// The executor auto-summarizes and — because the compressed context is fed
// back to the LLM right away — injects a persisted, visible "Proceed" user
// message, which must render as a user bubble in the chat.

/** Give the mock provider's model a context window (enables auto-summarize). */
async function configureContextWindow(page: Page) {
  const cfg = await apiGetConfig(page);
  const providers = (cfg?.providers || []).map((p: any) => {
    if ((p.url || '').includes(':9999')) {
      return {
        ...p,
        models: (p.models || []).map((m: any) =>
          m.name === 'mock-model' ? { ...m, context_window: 8192 } : m
        ),
      };
    }
    return p;
  });
  await apiPutConfig(page, { ...cfg, providers });
  await waitForConfig(page, (c: any) =>
    (c?.providers || []).some(
      (p: any) =>
        (p.url || '').includes(':9999') &&
        (p.models || []).some((m: any) => m.name === 'mock-model' && m.context_window === 8192)
    )
  );
}

test.describe.serial('Auto-Summarize Proceed', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await configureContextWindow(page);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should inject a visible "Proceed" user message after auto-summarization', async ({ page }) => {
    test.setTimeout(60000);
    const taskId = await createTaskViaApi(page, projectId, `auto-summarize-proceed-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Trigger a tool-call turn whose reported usage exceeds 90% of the model
    // context window — the executor auto-summarizes and injects "Proceed".
    await sendMessage(page, 'summarize-now start');

    // 1. The tool call renders and executes.
    await expect(page.locator('.tool-call-card').first()).toBeVisible({ timeout: 20000 });

    // 2. Auto-summarization runs — the summary entry appears.
    await expect(page.locator('.summarization-entry').first()).toBeVisible({ timeout: 20000 });

    // 3. The injected "Proceed" user message is visible in the chat.
    const proceed = page.locator('.chat-message.user', { hasText: 'Proceed' }).first();
    await expect(proceed).toBeVisible({ timeout: 15000 });

    // 4. The follow-up assistant response completes the turn (send button returns).
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // 5. The "Proceed" message is persisted server-side (not just echoed live).
    const msgs = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
      return await res.json();
    }, { pid: projectId, tid: taskId });
    expect(
      (msgs || []).some((m: any) => m.role === 'user' && m.content === 'Proceed')
    ).toBe(true);
  });
});
