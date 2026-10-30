import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, waitForChatForm, waitForSendButton, sendMessage, isolatedProject, clearTasksAfterEach, gotoProject, waitForWorktree, writeFileInWorktree, waitForTaskState, getTaskData, messageRows, notInFlight } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

// One task per test, each with its own container: a live task is charged against the executor's memory
// budget until it is deleted, so they go between tests, not at the end of the file.
// See clearTasksAfterEach() in helpers.ts.
clearTasksAfterEach();


test.describe.configure({ mode: 'serial' });

// ─── Helpers ────────────────────────────────────────────────────────────────

/** The ids this task's message window holds, for a later read that asks what arrived since. */
async function rowIds(page: Page, projectId: string, taskId: string): Promise<Set<string>> {
  return new Set((await messageRows(page, projectId, taskId)).map(r => r.id));
}

/**
 * What the store holds of a send that has not finished, as the value a failing poll reports: the task's own
 * `execution_state` and the roles newer than `before`. Rows are matched by id because the window slides. A turn
 * parked at the memory gate, a send absorbed by the turn before it and a turn that answered with the wrong kind
 * of row then read as three different things, which a card count reports as one.
 */
function storeSinceSend(page: Page, projectId: string, taskId: string, before: Set<string>) {
  return async () => {
    const task = await getTaskData(page, projectId, taskId);
    const fresh = (await messageRows(page, projectId, taskId)).filter(r => !before.has(r.id));
    return `execution_state=${String(task?.execution_state)} new_rows=${fresh.map(r => r.role).join(',') || 'none'}`;
  };
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

  // ─── Queue Indicator ──────────────────────────────────────────────────────
  // Stop/Cancel and the idle Escape live in `pause-resume.spec.ts`,
  // `stop-tool-status.spec.ts` and `chat-input-esc.spec.ts`.

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
    test.setTimeout(120000);
    // Pinned to text, not merely keyword-free: "edit-file" matches the mock's `\bedit\b` route, so the
    // initial turn would answer with this very fixture — same tool-call id, and the second
    // card then replaces the first instead of adding to it.
    const taskId = await createTaskViaApi(page, projectId, `seed-turn-${Date.now()} llm-fixture:text`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Idle by the store's answer, not by the send button: docs/testing.md → Reading a turn from the store.
    await waitForTaskState(page, projectId, taskId, notInFlight);
    await waitForSendButton(page);

    // Seed the file the fixture edits. The e2e repo is a README only, so an unseeded
    // edit_file fails with "no such file or directory" and the card never appears.
    await waitForWorktree(projectId, taskId);
    writeFileInWorktree(projectId, taskId, 'src/lib/utils/format.ts', 'func one\nfunc two\n');

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    // Pinned, not keyworded: this needs the edit_file fixture specifically (the edit label
    // and the +X/-Y toggle only exist for it), and a bare "edit" prompt is answered by plan
    // routing with create_task — see llm-fixture: in mock-llm.js.
    const before = await rowIds(page, projectId, taskId);
    await chatTextarea.fill(`test edit file llm-fixture:tool_call_edit_file ${Date.now()}`);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect
      .poll(storeSinceSend(page, projectId, taskId, before), {
        timeout: 60000,
        message: 'the send wrote no tool_call row of its own',
      })
      .toContain('tool_call');

    // Named by its tool rather than by its position: the turn answers with a card of its own after this one,
    // and a positional locator follows that. The initial turn is pinned to text, so this card is this send's.
    const card = page.locator('.tool-call-card', { has: page.locator('.tool-name', { hasText: /edit/i }) }).last();

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

  test('should accumulate raw_args_delta for streaming tool calls', async ({ page }) => {
    test.setTimeout(120000);
    // Pinned to text: unpinned, the auto-started turn takes the plan route and answers with create_task cards
    // of its own, which is what made "a card appeared" answerable by counting with no new turn behind it.
    const taskId = await createTaskViaApi(page, projectId, `raw-args-delta-${Date.now()} llm-fixture:text`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    // Idle by the store's answer, not by the send button: docs/testing.md → Reading a turn from the store.
    await waitForTaskState(page, projectId, taskId, notInFlight);
    await waitForSendButton(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    const before = await rowIds(page, projectId, taskId);
    // Pinned: this fixture streams the tool NAME and the args across several deltas, which
    // is the raw_args_delta path under test. A keyword prompt gets plan routing instead.
    await chatTextarea.fill(`streaming tool call ${Date.now()} llm-fixture:tool_call_streaming`);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    await expect
      .poll(storeSinceSend(page, projectId, taskId, before), {
        timeout: 60000,
        message: 'the send wrote no tool_call row of its own',
      })
      .toContain('tool_call');

    // The args are only complete once every delta landed, so the card must still carry the command the fixture
    // streamed in pieces — `{"command":"ls ` then ` -la"}`. Only the browser that streamed them can be asked,
    // which is why this one assertion stays in the DOM.
    const card = page.locator('.tool-call-card', { has: page.locator('.tool-args', { hasText: 'ls' }) }).last();
    await expect(card.locator('.tool-args')).toContainText('ls -la', { timeout: 15000 });
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

  // ─── Reasoning Block ──────────────────────────────────────────────────────

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

  // ─── Tool Call Task Board Update ──────────────────────────────────────────

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
