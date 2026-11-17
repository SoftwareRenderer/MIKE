import { test, expect, type Page } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, navigateToTask, waitForChatForm, waitForSendButton, sendMessage,
  isolatedProject, clearTasksAfterEach, gotoProject, waitForTaskState, getTaskData, messageRows, notInFlight,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

// One task per test, each with its own container: a live task is charged against the executor's memory
// budget until it is deleted, so they go between tests, not at the end of the file.
// See clearTasksAfterEach() in helpers.ts.
clearTasksAfterEach();


test.describe.configure({ mode: 'serial' });

// What this file owns is the chat speaking to a real executor over a real socket: a turn sent, a turn
// absorbed, and the rows a turn wrote reaching the transcript. A component's own state is not asked here —
// the tool card, its diff toggle, the reasoning block, the form's caret and the copy button are vitest's,
// and the specs beside this one hold what only a browser decides (docs/testing.md → E2E conventions).
// Stop/Cancel and the idle Escape live in `pause-resume.spec.ts`, `stop-tool-status.spec.ts` and
// `chat-input-esc.spec.ts`.

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

  // ─── The wait for a first chunk, and the send that lands during a turn ────

  // The full lifecycle of `.awaiting-llm` in one turn, and the absorbed send inside it: hidden while idle,
  // shown while the request waits for its first chunk, absent while chunks arrive, and never re-entered by a
  // send that interrupts a live stream.
  test('the queue indicator marks the wait for a first chunk, and a send during a turn is absorbed', async ({ page }) => {
    test.setTimeout(120000);
    const taskId = await createTaskViaApi(page, projectId, `queue-indicator-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Idle before sending, and waited for rather than skipped without one: the task's own
    // initial turn is running when the view opens, and a message sent into that turn queues
    // behind it — every window this test measures then lands somewhere unschedulable.
    await expect(page.locator('.btn-send'), 'the task is idle before the first send').toBeVisible({ timeout: 60000 });
    const queueIndicator = page.locator('.awaiting-llm');
    expect(await queueIndicator.isVisible(), 'the indicator is up on an idle task').toBe(false);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    const stamp = Date.now();
    // Both messages need the turn still in flight when the next one is sent, and the indicator
    // renders only while awaitingResponse is true:
    //   slow-start         → 3s before the first chunk, so the window is seconds, not ms
    //   text_slow (pinned) → ~1.6s of streaming and no tool calls; a tool-call fixture would end
    //                        the turn and take the indicator's gate down with it.
    const firstMsg = `slow-start first ${stamp} llm-fixture:text_slow`;
    const secondMsg = `slow-start second ${stamp} llm-fixture:text_slow`;

    await chatTextarea.fill(firstMsg);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // isVisible() answers immediately and does not poll, so it cannot express "appears
    // within 10s" — under load the turn starts a tick after Enter and this read false.
    const indicatorAppeared = await queueIndicator
      .waitFor({ state: 'visible', timeout: 10000 })
      .then(() => true)
      .catch(() => false);
    expect(indicatorAppeared, 'the indicator does not appear while the turn waits for its first chunk').toBe(true);

    // Wait until the first turn is ACTUALLY STREAMING, not merely in flight. Measured
    // against the app, `.awaiting-llm` means "request sent, nothing back yet" and is ABSENT
    // while chunks arrive, so streaming is exactly "stop button up, indicator gone" — and a
    // second message sent during the warm-up gap leaves no indicator window to assert on.
    await expect(async () => {
      expect(await page.locator('.btn-stop').isVisible(), 'turn is in flight').toBe(true);
      expect(await page.locator('.awaiting-llm').isVisible(), 'chunks are arriving').toBe(false);
    }).toPass({ timeout: 15000 });

    // Send second message mid-turn: absorbed, not rejected, and it starts a turn of its own.
    const before = await rowIds(page, projectId, taskId);
    await chatTextarea.fill(secondMsg);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Both messages are in the chat, in order. Counting to an absolute 2 assumed the chat
    // holds nothing but this test's messages — an auto-started task's own initial prompt is
    // a user message too, so assert the two we sent, not the total.
    const userMessages = page.locator('.chat-message.user');
    await expect(userMessages.nth(-1)).toContainText(secondMsg);
    await expect(userMessages.nth(-2)).toContainText(firstMsg);

    // A replacement turn runs — a row of its own in the store, not a card count on screen — the
    // form returns to idle, and the indicator stays hidden by design: sending during a turn never
    // re-enters the "request sent, nothing back yet" state.
    await expect
      .poll(storeSinceSend(page, projectId, taskId, before), {
        timeout: 60000,
        message: 'the absorbed send started no turn of its own',
      })
      .toContain('assistant');
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    await expect(queueIndicator).toBeHidden({ timeout: 10000 });
  });

  // ─── Reasoning Block ──────────────────────────────────────────────────────

  // One turn answers with reasoning and one without, so the whole toggle — expand, collapse, and the
  // absence of a block on an answer that reasoned about nothing — is decided on one transcript.
  test('the reasoning block expands, collapses, and is absent where there was no reasoning', async ({ page }) => {
    test.setTimeout(90000);
    // Pinned: the task's own turn must be an answer with nothing to think about, so this transcript
    // holds one reasoned answer and one plain one.
    const taskId = await createTaskViaApi(page, projectId, `reasoning-${Date.now()} llm-fixture:text`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);

    // Not an early return: a skipped check is a green test that checks nothing. Wait for
    // the task to be idle (its own initial turn may still be running) and fail if it never is.
    await waitForTaskState(page, projectId, taskId, notInFlight, 60000);
    await waitForSendButton(page);

    // The plain answer the task wrote for its own prompt: without this the absence below would be the
    // absence of an answer, which proves nothing.
    await expect(page.locator('.chat-message.assistant').first()).toContainText('Hello', { timeout: 15000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`reasoning test expand ${Date.now()} llm-fixture:text_response_reasoning`);
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // Scoped to the entry that reasoned, because a turn may answer more than once and every answer of
    // this fixture reasons: a page-wide locator would be a strict-mode violation over answers that all
    // behave the same.
    const reasonedEntry = page.locator('.chat-entry', { hasText: 'my response after reasoning' }).first();
    await expect(reasonedEntry, 'the send never answered with the reasoning fixture').toBeVisible({ timeout: 15000 });
    const reasoningToggle = reasonedEntry.locator('.reasoning-toggle');
    const reasoningContent = reasonedEntry.locator('.reasoning-content');

    await expect(reasoningToggle).toBeVisible({ timeout: 10000 });
    await reasoningToggle.click();
    await expect(reasoningContent).toBeVisible({ timeout: 5000 });
    await expect(reasoningContent).toContainText('think');
    await expect(reasoningContent).toContainText('step by step');
    await expect(reasoningToggle).toContainText('Hide');

    await reasoningToggle.click();
    await expect(reasoningContent).toBeHidden({ timeout: 5000 });
    await expect(reasoningToggle).toContainText('Show');

    // The plain answer the task wrote for its own prompt carries no block at all: `ReasoningBlock`
    // renders nothing where there was no reasoning, so the row that reasoned about nothing holds
    // neither the toggle nor the content. Asked of that entry, because the reasoned turn may answer
    // more than once and a page-wide count would be a count of the mock's calls.
    const plainEntry = page.locator('.chat-entry', { hasText: 'Hello! How can I help you today?' }).first();
    await expect(plainEntry).toBeVisible();
    await expect(plainEntry.locator('.reasoning-toggle')).toHaveCount(0);
    await expect(plainEntry.locator('.reasoning-content')).toHaveCount(0);
  });
});
