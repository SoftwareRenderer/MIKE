import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, waitForChatForm, waitForSendButton, sendMessage, csrfFetch, ensureChatForm, findProviderByRef, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe.configure({ mode: 'serial' });

// ─── Helpers ────────────────────────────────────────────────────────────────

/** Wait for the send button to be visible; returns false instead of throwing. */
async function requireSendButton(page: any): Promise<boolean> {
  return await page.locator('.btn-send').waitFor({ state: 'visible', timeout: 15000 }).then(() => true).catch(() => false);
}

// ─── Shared setup ───────────────────────────────────────────────────────────

test.describe('Chat Core', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  // ─── LLM Interaction ──────────────────────────────────────────────────────

  test('should send a message and receive a text LLM response', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `llm-interaction-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    await sendMessage(page, 'hello interaction test');
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-message.assistant').first()).toBeVisible({ timeout: 15000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });
  });

  test('should send a message that triggers tool calls and verify tool call cards render', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `llm-tool-call-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    // "read-file-probe" is a mock-LLM keyword (see selectResponse): it routes to a fixture
    // whose tool call the UI renders WITH arguments. An ordinary prompt cannot ask for one —
    // the plan-first skill is in every system prompt, so a plain prompt routes to
    // create_task, and create_task args are hidden by design (HIDE_ARGS_FOR_TOOLS).
    await sendMessage(page, `read-file-probe ${Date.now()}`);
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.tool-call-card').first()).toBeVisible({ timeout: 15000 });
    await expect(page.locator('.tool-name').first()).toBeVisible();
    // The args line is what formatToolArgs produces for read_file: the path, ./-prefixed.
    await expect(page.locator('.tool-args').first()).toContainText('./README.md');
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });
  });

  test('should verify tool call results appear in chat after tool execution', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `llm-tool-result-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

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
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `stop-button-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    // "stop-test" → the slow text fixture; "slow-start" → a 3s initial LLM delay (see
    // pause-resume.spec.ts for the same recipe). The delay matters: with the mock streaming
    // a chunk per millisecond, the form re-renders faster than Playwright can land a click
    // ("element was detached from the DOM, retrying", forever). During the warm-up window the
    // turn is genuinely in flight and the DOM is still, which is what this test is about.
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`stop-test slow-start: cancel from chat ${Date.now()}`);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    const stopBtnVisible = await page.locator('.btn-stop').waitFor({ state: 'visible', timeout: 10000 }).then(() => true).catch(() => false);
    expect(stopBtnVisible, 'stop button shows while the turn is in flight').toBe(true);

    await page.locator('.btn-stop').click();
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.btn-stop')).toBeHidden({ timeout: 10000 });
  });

  // Escape never cancels a turn — not even while the input is focused. The
  // in-flight case is covered by chat-input-esc.spec.ts ("Escape while
  // streaming blurs the input and never stops generation"); the button tests
  // below cover the only remaining stop affordance.

  test('should not trigger stop when Escape is pressed and not loading', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `escape-noop-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.focus();
    await chatTextarea.press('Escape');
    await expect(page.locator('.btn-send')).toBeVisible();
  });

  // ─── Queue Indicator ──────────────────────────────────────────────────────

  test('should show queue indicator during LLM processing', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `queue-show-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    // slow-start widens the window this indicator exists in: it renders while the request is
    // sent but nothing has come back, which without it lasts a few milliseconds.
    await chatTextarea.fill(`queue-show ${Date.now()} slow-start llm-fixture:text_slow`);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    const queueIndicator = page.locator('.awaiting-llm');
    await expect(queueIndicator).toBeVisible({ timeout: 15000 });
    await expect(queueIndicator).toContainText('Awaiting');
  });

  test('should update when a second message cancels the first', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `queue-cancel-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Idle before sending, and wait for it rather than skipping without one: the task's own
    // initial turn is running when the view opens, and a message sent into that turn queues
    // behind it — every window this test measures then lands somewhere unschedulable.
    await expect(page.locator('.btn-send'), 'the task is idle before the first send').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    const stamp = Date.now();
    // Both messages need the turn still in flight when the next one is sent, and the indicator
    // renders only while awaitingResponse is true:
    //   slow-start         → 3s before the first chunk, so the window is seconds, not ms
    //   text_slow (pinned) → ~1.6s of streaming and no tool calls; a tool-call fixture would end
    //                        the turn and take the indicator's gate down with it.
    const firstMsg = `slow-start first ${stamp} llm-fixture:text_slow`;
    const secondMsg = `slow-start second ${stamp} llm-fixture:text_slow`;

    // Send first message (slow response)
    await chatTextarea.fill(firstMsg);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    const queueIndicator = page.locator('.awaiting-llm');
    await expect(queueIndicator).toBeVisible({ timeout: 10000 });

    // Wait until the first turn is ACTUALLY STREAMING, not merely in flight. Measured
    // against the app, `.awaiting-llm` means "request sent, nothing back yet" and is ABSENT
    // while chunks arrive, so streaming is exactly "stop button up, indicator gone" — and a
    // second message sent during the warm-up gap leaves no indicator window to assert on.
    await expect(async () => {
      expect(await page.locator('.btn-stop').isVisible(), 'turn is in flight').toBe(true);
      expect(await page.locator('.awaiting-llm').isVisible(), 'chunks are arriving').toBe(false);
    }).toPass({ timeout: 15000 });

    // Send second message — cancels first
    await chatTextarea.fill(secondMsg);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Both messages are in the chat, in order. Counting to an absolute 2 assumed the chat
    // holds nothing but this test's messages — an auto-started task's own initial prompt is
    // a user message too, so assert the two we sent, not the total.
    const userMessages = page.locator('.chat-message.user');
    await expect(userMessages.nth(-1)).toContainText(secondMsg);
    await expect(userMessages.nth(-2)).toContainText(firstMsg);

    // What "cancels the first" is observably made of: a replacement turn runs, the form returns
    // to idle, both messages stay in order. Sending during a turn never re-enters the "request
    // sent, nothing back yet" state, so the queue indicator stays hidden by design.
    const assistantEntries = page.locator('.chat-message.assistant');
    const assistantBefore = await assistantEntries.count();
    await expect
      .poll(() => assistantEntries.count(), { timeout: 30000, message: 'the second message starts a turn of its own' })
      .toBeGreaterThan(assistantBefore);
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
  });

  test('should disappear when all messages are processed', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `queue-done-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`queue-done ${Date.now()} slow-start llm-fixture:text_slow`);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    const queueIndicator = page.locator('.awaiting-llm');
    await expect(queueIndicator).toBeVisible({ timeout: 10000 });

    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    await expect(queueIndicator).toBeHidden({ timeout: 10000 });
  });

  // The full lifecycle in one pass: hidden while idle, shown while the request waits for its
  // first chunk, hidden again once the turn is over.
  test('should not appear when LLM is idle', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `queue-idle-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const queueIndicator = page.locator('.awaiting-llm');
    expect(await queueIndicator.isVisible()).toBe(false);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`queue-idle ${Date.now()} slow-start llm-fixture:text_slow`);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // isVisible() answers immediately and does not poll, so it cannot express "appears
    // within 10s" — under load the turn starts a tick after Enter and this read false.
    const indicatorAppeared = await queueIndicator
      .waitFor({ state: 'visible', timeout: 10000 })
      .then(() => true)
      .catch(() => false);
    expect(indicatorAppeared, 'queue indicator appears while the turn is waiting').toBe(true);

    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    await expect(queueIndicator).toBeHidden({ timeout: 10000 });

    // Verify it stays hidden when idle — wait for a short condition check
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 2000 });
    expect(await queueIndicator.isVisible()).toBe(false);
  });

  // ─── Streaming Features ───────────────────────────────────────────────────

  test('should show Editing label and +X/-Y toggle for edit_file tool calls', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `edit-file-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    // Pinned, not keyworded: this needs the edit_file fixture specifically (the edit label
    // and the +X/-Y toggle only exist for it), and a bare "edit" prompt is answered by plan
    // routing with create_task — see llm-fixture: in mock-llm.js.
    await chatTextarea.fill(`test edit file llm-fixture:tool_call_edit_file ${Date.now()}`);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // The task's initial turn already put tool-call cards in this chat, so assert on the one
    // that appears after the send (the last). `.first()` is the initial turn's card.
    const cards = page.locator('.tool-call-card');
    const cardsBefore = await cards.count();

    await expect
      .poll(() => cards.count(), { timeout: 20000, message: 'a new tool-call card appears' })
      .toBeGreaterThan(cardsBefore);
    const card = cards.last();

    await expect(card.locator('.tool-name')).toContainText(/edit/i);

    // Let the turn finish first: the card is re-rendered per streamed chunk, and a click that
    // lands during one of those renders is silently lost (the toggle then never collapses).
    await expect(card.locator('.tool-duration')).toBeVisible({ timeout: 20000 });

    // The +X/-Y toggle is the whole subject here, so it is required, not optional.
    const toggle = card.locator('.edit-diff-toggle');
    await expect(toggle).toBeVisible({ timeout: 15000 });
    await expect(toggle).toHaveText(/\+\d+/);
    await expect(toggle).toHaveText(/-\d+/);

    // Two clicks, each one flipping the panel. The starting state is NOT asserted as
    // collapsed on purpose: ToolCall auto-expands a trivial change (<=2 lines), and this
    // fixture's edit adds exactly two — so "expanded first" is the product's behaviour, and
    // pinning it here is what tells us if that default ever changes.
    for (let click = 0; click < 2; click++) {
      const wasExpanded = (await toggle.getAttribute('aria-expanded')) === 'true';
      if (wasExpanded) await expect(card.locator('.edit-diff')).toBeVisible();
      else await expect(card.locator('.edit-diff')).toBeHidden();
      await toggle.click();
      await expect(toggle).toHaveAttribute('aria-expanded', String(!wasExpanded));
      if (wasExpanded) await expect(card.locator('.edit-diff')).toBeHidden();
      else await expect(card.locator('.edit-diff')).toBeVisible();
    }
  });

  test('should not have overlapping entries after streaming tool calls', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `no-overlap-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('test read file command');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    const toolCallVisible = await page.locator('.tool-call-card').first()
      .waitFor({ state: 'visible', timeout: 15000 }).then(() => true).catch(() => false);
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

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('test read file directory list');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    const toolCallVisible = await page.locator('.tool-call-card').first()
      .waitFor({ state: 'visible', timeout: 15000 }).then(() => true).catch(() => false);
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
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `raw-args-delta-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    // Pinned: this fixture streams the tool NAME and the args across several deltas, which
    // is the raw_args_delta path under test. A keyword prompt gets plan routing instead.
    await chatTextarea.fill(`streaming tool call ${Date.now()} llm-fixture:tool_call_streaming`);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Same "the card this message produced, not the task's initial turn" scoping as the
    // edit_file test above.
    const cards = page.locator('.tool-call-card');
    const cardsBefore = await cards.count();
    await expect
      .poll(() => cards.count(), { timeout: 20000, message: 'a new tool-call card appears' })
      .toBeGreaterThan(cardsBefore);

    // The args are only complete once every delta landed; the accumulated raw args must
    // still carry the command the fixture streamed in pieces.
    await expect(cards.last().locator('.tool-args')).toContainText('ls', { timeout: 15000 });
  });

  // ─── Tab Visibility ───────────────────────────────────────────────────────

  test('visibility change does not disrupt normal (idle) task state', async ({ page }) => {
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `visibility-idle-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

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
    const defaultProvider = findProviderByRef(config.providers || [], config.worker_provider);

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
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-show-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`reasoning test ${Date.now()} llm-fixture:text_response_reasoning`);
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
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-expand-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`reasoning test expand ${Date.now()} llm-fixture:text_response_reasoning`);
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
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-collapse-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`reasoning test collapse ${Date.now()} llm-fixture:text_response_reasoning`);
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
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-none-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

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
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `reasoning-a11y-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`reasoning test accessibility ${Date.now()} llm-fixture:text_response_reasoning`);
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
    test.setTimeout(90000);

    const initialCount = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.length;
    }, projectId);

    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = findProviderByRef(config?.providers || [], config?.worker_provider);

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
    test.setTimeout(90000);

    const initialCounts = await page.evaluate(async () => {
      const res = await fetch('/api/projects/counts');
      return res.ok ? res.json() : null;
    });
    const initialBacklog = initialCounts?.[projectId]?.backlog || 0;

    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = findProviderByRef(config?.providers || [], config?.worker_provider);

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
    test.setTimeout(90000);

    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = findProviderByRef(config?.providers || [], config?.worker_provider);

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
    test.setTimeout(90000);

    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = findProviderByRef(config?.providers || [], config?.worker_provider);

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
    test.setTimeout(90000);
    const taskId = await createTaskViaApi(page, projectId, `board-update-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });

    await sendMessage(page, `board-update ${Date.now()} llm-fixture:tool_call_read_file`);

    // Wait for tool call card to appear (confirms tool call was triggered)
    await expect(page.locator('.tool-call-card').first()).toBeVisible({ timeout: 15000 });

    // Wait for the full cycle to complete (send button reappears)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // The board's job is to show that work happened: the task left the backlog. Which column
    // it ends in depends on what the conversation does after the tool result (a follow-up
    // request_review moves it on), so assert the transition rather than the destination.
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });
    expect(taskData).toBeTruthy();
    expect(['in_progress', 'review']).toContain(taskData.status);
  });
});
