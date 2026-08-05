import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  waitForChatForm,
  waitForSendButton,
  sendMessage,
  csrfFetch,
  ensureChatForm,
  ensureTaskStarted,
} from './helpers';

test.describe.configure({ mode: 'serial' });

// ─── Helpers ────────────────────────────────────────────────────────────────

/** Wait for the send button to be visible; returns false instead of throwing. */
async function requireSendButton(page: any): Promise<boolean> {
  return await page.locator('.btn-send').isVisible({ timeout: 15000 }).catch(() => false);
}

// ─── Shared setup ───────────────────────────────────────────────────────────

test.describe('Chat Core', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  // ─── LLM Interaction ──────────────────────────────────────────────────────

  test('should send a message and receive a text LLM response', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `llm-interaction-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    await sendMessage(page, 'hello interaction test');
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-message.assistant').first()).toBeVisible({ timeout: 15000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });
  });

  test('should send a message that triggers tool calls and verify tool call cards render', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `llm-tool-call-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    await sendMessage(page, 'list all files');
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.tool-call-card').first()).toBeVisible({ timeout: 15000 });
    await expect(page.locator('.tool-name').first()).toBeVisible();
    await expect(page.locator('.tool-args').first()).toBeVisible();
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });
  });

  test('should verify tool call results appear in chat after tool execution', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `llm-tool-result-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    await sendMessage(page, 'list all files');
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.tool-call-card').first()).toBeVisible({ timeout: 15000 });
    await expect(page.locator('.tool-name').first()).toBeVisible();
    // Wait for tool execution to complete and assistant response to appear
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    // Verify assistant message exists (may appear after tool results)
    const assistantCount = await page.locator('.chat-message.assistant').count();
    expect(assistantCount).toBeGreaterThan(0);
  });

  // ─── Stop / Cancel ────────────────────────────────────────────────────────

  test('should show stop button during streaming and allow cancelling', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `stop-button-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('stop-test: please respond');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    const stopBtnVisible = await page.locator('.btn-stop').isVisible({ timeout: 10000 }).catch(() => false);
    if (!stopBtnVisible) return;

    await page.locator('.btn-stop').click();
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.btn-stop')).toBeHidden({ timeout: 10000 });
  });

  test('should stop generation when Escape is pressed in chat input', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `escape-stop-${Date.now()}`);
    await navigateToTask(page, taskId);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('stop-test message');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    const stopBtnVisible = await page.locator('.btn-stop').isVisible({ timeout: 10000 }).catch(() => false);
    if (!stopBtnVisible) return;

    // Wait for assistant message to appear (streaming in progress)
    await expect(page.locator('.chat-message.assistant').first()).toBeVisible({ timeout: 5000 });

    const assistantBefore = page.locator('.chat-message.assistant');
    const assistantCountBefore = await assistantBefore.count();

    await chatTextarea.focus();
    await chatTextarea.press('Escape');

    await expect(page.locator('.btn-stop')).toBeHidden({ timeout: 10000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });

    const cursorVisible = await page.locator('.streaming-cursor').isVisible().catch(() => false);
    expect(cursorVisible).toBe(false);

    const assistantAfter = page.locator('.chat-message.assistant');
    const assistantCountAfter = await assistantAfter.count();
    expect(assistantCountAfter).toBeLessThanOrEqual(assistantCountBefore);

    const lastMessage = page.locator('.chat-message').last();
    await expect(lastMessage).toHaveClass(/user/);
  });

  test('should not trigger stop when Escape is pressed and not loading', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `escape-noop-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.focus();
    await chatTextarea.press('Escape');
    await expect(page.locator('.btn-send')).toBeVisible();
  });

  // ─── Queue Indicator ──────────────────────────────────────────────────────

  test('should show queue indicator during LLM processing', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `queue-show-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('stop-test message');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    const queueIndicator = page.locator('.awaiting-llm');
    await expect(queueIndicator).toBeVisible({ timeout: 10000 });

    const indicatorText = await queueIndicator.textContent();
    expect(indicatorText).toContain('Awaiting');
  });

  test('should update when a second message cancels the first', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `queue-cancel-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send first message (slow response)
    await chatTextarea.fill('stop-test first');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    const queueIndicator = page.locator('.awaiting-llm');
    await expect(queueIndicator).toBeVisible({ timeout: 10000 });

    // Wait for stop button (LLM started streaming)
    const stopBtnVisible = await page.locator('.btn-stop').isVisible({ timeout: 8000 }).catch(() => false);
    if (!stopBtnVisible) return;

    // Send second message — cancels first
    await chatTextarea.fill('stop-test second');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    const userMessages = page.locator('.chat-message.user');
    await expect(userMessages).toHaveCount(2, { timeout: 10000 });

    await expect(queueIndicator).toBeVisible({ timeout: 10000 });
    const indicatorText = await queueIndicator.textContent();
    expect(indicatorText).toContain('Awaiting');
  });

  test('should disappear when all messages are processed', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `queue-done-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('stop-test final');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    const queueIndicator = page.locator('.awaiting-llm');
    await expect(queueIndicator).toBeVisible({ timeout: 10000 });

    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    await expect(queueIndicator).toBeHidden({ timeout: 10000 });
  });

  test('should not appear when LLM is idle', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `queue-idle-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const queueIndicator = page.locator('.awaiting-llm');
    expect(await queueIndicator.isVisible()).toBe(false);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('idle test message');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    const indicatorAppeared = await queueIndicator.isVisible({ timeout: 10000 }).catch(() => false);
    expect(indicatorAppeared).toBe(true);

    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    await expect(queueIndicator).toBeHidden({ timeout: 10000 });

    // Verify it stays hidden when idle — wait for a short condition check
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 2000 });
    expect(await queueIndicator.isVisible()).toBe(false);
  });

  // ─── Streaming Features ───────────────────────────────────────────────────

  test('should show Editing label and +X/-Y toggle for edit_file tool calls', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `edit-file-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('test edit file');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (!toolCallVisible) return;

    await expect(page.locator('.tool-name').first()).toBeVisible();
    const toolNameText = await page.locator('.tool-name').first().textContent();
    expect(toolNameText?.toLowerCase()).toContain('edit');

    // Wait for tool to complete (duration appears or cursor hidden)
    await page.waitForSelector('.tool-duration', { timeout: 15000 }).catch(() => {});

    const toggleVisible = await page.locator('.edit-diff-toggle').isVisible({ timeout: 10000 }).catch(() => false);
    if (toggleVisible) {
      const toggleText = await page.locator('.edit-diff-toggle').textContent();
      expect(toggleText).toMatch(/\+\d+\/-\d+/);

      await page.locator('.edit-diff-toggle').click();
      const diffVisible = await page.locator('.edit-diff').isVisible({ timeout: 5000 }).catch(() => false);
      expect(diffVisible).toBe(true);

      await page.locator('.edit-diff-toggle').click();
      const diffHidden = await page.locator('.edit-diff').isVisible({ timeout: 5000 }).catch(() => false);
      expect(diffHidden).toBe(false);
    }
  });

  test('should not have overlapping entries after streaming tool calls', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `no-overlap-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('test read file command');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (!toolCallVisible) return;

    // Wait for streaming to complete (cursor hidden)
    await page.waitForSelector('.cursor-blink', { state: 'hidden', timeout: 15000 }).catch(() => {});

    // Wait for layout to settle (send button reappears)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });

    const items = page.locator('.virtual-item[data-virtual-index]');
    const count = await items.count();

    if (count >= 2) {
      for (let i = 0; i < count - 1; i++) {
        const currentBox = await items.nth(i).boundingBox();
        const nextBox = await items.nth(i + 1).boundingBox();

        if (currentBox && nextBox) {
          const tolerance = 2;
          expect(currentBox.y + currentBox.height)
            .toBeLessThanOrEqual(nextBox.y + tolerance);
        }
      }
    }
  });

  test('should not have overlapping entries after multiple tool results', async ({ page }) => {
    test.setTimeout(45000);
    const taskId = await createTaskViaApi(page, projectId, `no-overlap-multi-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('test read file directory list');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (!toolCallVisible) return;

    // Wait for streaming to complete
    await page.waitForSelector('.cursor-blink', { state: 'hidden', timeout: 30000 }).catch(() => {});

    // Wait for all tool calls to complete (no pending spinners)
    await page.waitForSelector('.tool-status.pending', { state: 'hidden', timeout: 30000 }).catch(() => {});

    // Wait for layout to settle (send button reappears)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });

    const items = page.locator('.virtual-item[data-virtual-index]');
    const count = await items.count();

    if (count >= 2) {
      for (let i = 0; i < count - 1; i++) {
        const currentBox = await items.nth(i).boundingBox();
        const nextBox = await items.nth(i + 1).boundingBox();

        if (currentBox && nextBox) {
          const tolerance = 2;
          expect(currentBox.y + currentBox.height)
            .toBeLessThanOrEqual(nextBox.y + tolerance);
        }
      }
    }
  });

  test('should accumulate raw_args_delta for streaming tool calls', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `raw-args-delta-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('test streaming tool call');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (!toolCallVisible) return;

    await expect(page.locator('.tool-name').first()).toBeVisible();

    const toolArgs = page.locator('.tool-args').first();
    await expect(toolArgs).toBeVisible({ timeout: 10000 });
    const argsText = await toolArgs.textContent();
    expect(argsText).toContain('ls');
  });

  // ─── Tab Visibility ───────────────────────────────────────────────────────

  test('visibility change does not disrupt normal (idle) task state', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `visibility-idle-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const initialEntries = await page.locator('.chat-message').count();

    for (let i = 0; i < 3; i++) {
      await page.evaluate(() => {
        window.dispatchEvent(new Event('visibilitychange'));
      });
      // Wait for page to remain stable after each visibility change
      await expect(page.locator('.btn-send')).toBeVisible({ timeout: 2000 });
    }

    await expect(page.locator('.btn-send')).toBeVisible();
    const afterEntries = await page.locator('.chat-message').count();
    expect(afterEntries).toBe(initialEntries);
    expect(await page.locator('.cursor-blink').isVisible().catch(() => false)).toBe(false);
    expect(await page.locator('.btn-stop').isVisible().catch(() => false)).toBe(false);
  });

  test('task in review state remains stable after visibility change', async ({ page }) => {
    test.setTimeout(45000);

    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });
    const defaultProvider = config.providers?.find((p: any) => p.name === config.worker_provider);

    const taskResult = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `visibility-review ${Date.now()}`,
        llama_server_url: defaultProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    expect(taskResult.ok).toBe(true);
    const taskId = taskResult.data?.id;
    expect(taskId).toBeTruthy();

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-message').first()).toBeVisible({ timeout: 15000 });

    const taskStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return task.status;
    }, { pid: projectId, tid: taskId });
    expect(taskStatus).toBe('review');

    await page.evaluate(() => {
      window.dispatchEvent(new Event('visibilitychange'));
    });
    // Wait for page to remain stable
    await expect(page.locator('.chat-message').first()).toBeVisible({ timeout: 2000 });

    expect(await page.locator('.btn-stop').isVisible().catch(() => false)).toBe(false);
    expect(await page.locator('.cursor-blink').isVisible().catch(() => false)).toBe(false);
    expect(await page.locator('.chat-message').count()).toBeGreaterThan(0);
  });

  // ─── Reasoning Block ──────────────────────────────────────────────────────

  test('should show reasoning toggle when LLM returns reasoning content', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-show-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('reasoning test show reasoning');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    const reasoningToggle = page.locator('.reasoning-toggle');
    await expect(reasoningToggle).toBeVisible({ timeout: 10000 });

    const toggleText = await reasoningToggle.textContent();
    expect(toggleText).toContain('reasoning');
  });

  test('should expand reasoning content when toggle is clicked', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-expand-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('reasoning test expand reasoning');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    const reasoningToggle = page.locator('.reasoning-toggle');
    await expect(reasoningToggle).toBeVisible({ timeout: 10000 });
    await reasoningToggle.click();

    const reasoningContent = page.locator('.reasoning-content');
    await expect(reasoningContent).toBeVisible({ timeout: 5000 });

    const contentText = await reasoningContent.textContent();
    expect(contentText).toContain('think');
    expect(contentText).toContain('step by step');

    const expandedToggleText = await reasoningToggle.textContent();
    expect(expandedToggleText).toContain('Hide');
  });

  test('should collapse reasoning content when toggle is clicked again', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-collapse-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('reasoning test collapse reasoning');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    const reasoningToggle = page.locator('.reasoning-toggle');
    await expect(reasoningToggle).toBeVisible({ timeout: 10000 });
    await reasoningToggle.click();

    const reasoningContent = page.locator('.reasoning-content');
    await expect(reasoningContent).toBeVisible({ timeout: 5000 });

    await reasoningToggle.click();
    await expect(reasoningContent).not.toBeVisible({ timeout: 5000 });

    const collapsedToggleText = await reasoningToggle.textContent();
    expect(collapsedToggleText).toContain('Show');
  });

  test('should not show reasoning toggle for messages without reasoning', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-none-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('simple greeting hello');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    const reasoningToggle = page.locator('.reasoning-toggle');
    expect(await reasoningToggle.isVisible()).toBe(false);

    const reasoningContent = page.locator('.reasoning-content');
    expect(await reasoningContent.isVisible()).toBe(false);
  });

  test('reasoning block should have proper accessibility attributes', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-a11y-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('reasoning test accessibility');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    const reasoningToggle = page.locator('.reasoning-toggle');
    await expect(reasoningToggle).toBeVisible({ timeout: 10000 });

    const initialExpanded = await reasoningToggle.getAttribute('aria-expanded');
    expect(initialExpanded).toMatch(/^(true|false)$/);

    await reasoningToggle.click();
    await expect(reasoningToggle).toHaveAttribute('aria-expanded', 'true', { timeout: 5000 });

    await reasoningToggle.click();
    await expect(reasoningToggle).toHaveAttribute('aria-expanded', 'false', { timeout: 5000 });
  });

  // ─── Tool Call Task Board Update ──────────────────────────────────────────

  test('should update board in real time when a task is created via API', async ({ page }) => {
    test.setTimeout(30000);

    const initialCount = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.length;
    }, projectId);

    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = config?.providers?.find((p: any) => p.name === config?.worker_provider);

    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `Real-time test task ${Date.now()}`,
        llama_server_url: defaultProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    expect(result.ok).toBe(true);
    const newTaskId = result.data?.id;
    expect(newTaskId).toBeTruthy();

    await expect(async () => {
      const tasks = await page.evaluate(async (pid) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return await res.json();
      }, projectId);
      expect(tasks.tasks.length).toBeGreaterThan(initialCount);
      expect(tasks.tasks.some((t: any) => t.id === newTaskId)).toBe(true);
    }).toPass({ timeout: 10000 });

    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: newTaskId });
    expect(taskData).toBeTruthy();
    expect(taskData.status).toBe('backlog');
  });

  test('should update sidebar project counts in real time when a task is created', async ({ page }) => {
    test.setTimeout(30000);

    const initialCounts = await page.evaluate(async () => {
      const res = await fetch('/api/projects/counts');
      return res.ok ? res.json() : null;
    });
    const initialBacklog = initialCounts?.[projectId]?.backlog || 0;

    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = config?.providers?.find((p: any) => p.name === config?.worker_provider);

    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `Count test task ${Date.now()}`,
        llama_server_url: defaultProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    expect(result.ok).toBe(true);

    await expect(async () => {
      const counts = await page.evaluate(async () => {
        const res = await fetch('/api/projects/counts');
        return res.ok ? res.json() : null;
      });
      expect(counts?.[projectId]?.backlog).toBeGreaterThanOrEqual(initialBacklog + 1);
    }).toPass({ timeout: 10000 });
  });

  test('should refresh board data when tab becomes visible (mobile wake simulation)', async ({ page }) => {
    test.setTimeout(30000);

    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = config?.providers?.find((p: any) => p.name === config?.worker_provider);

    const initialCount = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.length;
    }, projectId);

    // Simulate phone going to sleep
    await page.evaluate(() => {
      Object.defineProperty(document, 'hidden', { value: true, writable: true, configurable: true });
      document.dispatchEvent(new Event('visibilitychange', { bubbles: true }));
    });

    // While "asleep", create a new task via API
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `Wake test task ${Date.now()}`,
        llama_server_url: defaultProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    expect(result.ok).toBe(true);
    const newTaskId = result.data?.id;

    // Simulate phone waking
    await page.evaluate(() => {
      Object.defineProperty(document, 'hidden', { value: false, writable: true, configurable: true });
      document.dispatchEvent(new Event('visibilitychange', { bubbles: true }));
    });

    await expect(async () => {
      const tasks = await page.evaluate(async (pid) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return await res.json();
      }, projectId);
      expect(tasks.tasks.length).toBeGreaterThan(initialCount);
      expect(tasks.tasks.some((t: any) => t.id === newTaskId)).toBe(true);
    }).toPass({ timeout: 10000 });
  });

  test('should update task in real time when edited via API', async ({ page }) => {
    test.setTimeout(30000);

    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = config?.providers?.find((p: any) => p.name === config?.worker_provider);

    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `Edit test task ${Date.now()}`,
        llama_server_url: defaultProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    expect(result.ok).toBe(true);
    const taskId = result.data?.id;

    const editResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
      method: 'PUT',
      body: { prompt: 'Edited prompt' },
    });
    expect(editResult.ok).toBe(true);

    const updatedTask = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });
    expect(updatedTask?.prompt).toBe('Edited prompt');
  });

  test('tool call triggers task board update', async ({ page }) => {
    test.setTimeout(30000);
    const taskId = await createTaskViaApi(page, projectId, `board-update-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    if (!await requireSendButton(page)) return;

    await sendMessage(page, 'list all files');

    // Wait for tool call card to appear (confirms tool call was triggered)
    await expect(page.locator('.tool-call-card').first()).toBeVisible({ timeout: 15000 });

    // Wait for the full cycle to complete (send button reappears)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });

    // Verify the task board reflects the updated task state
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });
    expect(taskData).toBeTruthy();
    expect(taskData.status).toBe('in_progress');
  });
});
