import { test, expect, type Page } from '@playwright/test';
import {
  apiGetJson, apiPutConfig, clearTasksAfterEach, createTaskViaApi, csrfFetch, isolatedProject, messageRows,
  navigateToTask, ownProjectId, sendTurn, waitForChatForm, waitForConfig, waitForSendButton,
  waitForTaskState, notInFlight,
} from './helpers';

// This file writes /api/config, which belongs to the server rather than to a project, so it runs on the
// isolated lane and puts the preference back when it is done.
isolatedProject();

// A live task is charged against the executor's memory budget until it is deleted, so they go between
// tests, not at the end of the file.
clearTasksAfterEach();

test.describe.configure({ mode: 'serial' });

// What only a browser can settle: with the preference on, the reasoning never reaches it — not on the
// socket, not in the transcript — and the reader gets it back by asking. What each frame means on screen is
// `stream-handler.unit.ts` and `reasoning-block.unit.svelte.ts`.

/** The text the reasoning fixture thinks, and the text its answer says. */
const REASONING = 'Let me think through this step by step.';
const ANSWER = 'my response after reasoning';

/**
 * The frames this page's sockets received, oldest first. Attached before the first navigation, because
 * `page.on('websocket')` only reports sockets opened after it is attached.
 */
function recordFrames(page: Page): string[] {
  const frames: string[] = [];
  page.on('websocket', (ws) => ws.on('framereceived', (frame) => frames.push(frame.payload)));
  return frames;
}

/** The stored assistant row whose text contains `needle`, as the messages route answers it — redacted
 * unless `full` is asked for. Row identity comes from the read, never from a row count. */
async function assistantRow(
    page: Page, projectId: string, taskId: string, needle: string, full: boolean,
): Promise<{ id: string; has_reasoning?: boolean; reasoning_content?: string } | undefined> {
  const rows = await apiGetJson<Array<{ id: string; role: string; content?: string; has_reasoning?: boolean; reasoning_content?: string }>>(
    page,
    `/api/projects/${projectId}/tasks/${taskId}/messages?limit=25${full ? '&full=1' : ''}`,
  );
  return rows?.find((r) => r.role === 'assistant' && (r.content || '').includes(needle));
}

test.describe('a collapsed reasoning stream', () => {
  test.afterEach(async ({ page }) => {
    // Each test states the answer it needs; this only cleans up after one that failed halfway.
    await apiPutConfig(page, { collapse_reasoning: false });
    await waitForConfig(page, (c) => c.collapse_reasoning === false);
  });

  test('the reasoning stays in the server until the reader opens it', async ({ page }) => {
    test.setTimeout(120_000);

    await apiPutConfig(page, { collapse_reasoning: true });
    await waitForConfig(page, (c) => c.collapse_reasoning === true);

    // The task's own turn answers plainly, so the reasoned answer below is the only reasoned one here.
    const taskId = await createTaskViaApi(page, ownProjectId(), `collapsed-${Date.now()} llm-fixture:text`);
    const frames = recordFrames(page);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);
    await waitForTaskState(page, ownProjectId(), taskId, notInFlight, 60_000);
    await waitForSendButton(page);

    await sendTurn(page, ownProjectId(), taskId, `collapse this ${Date.now()} llm-fixture:text_response_reasoning`);

    const rows = await messageRows(page, ownProjectId(), taskId);
    expect(rows.some((r) => r.role === 'assistant'), 'the reasoned turn wrote no assistant row').toBe(true);

    // The rate arrived on the socket, and none of the reasoning did.
    expect(frames.some((f) => f.includes('"reasoning_progress"'))).toBe(true);
    expect(frames.some((f) => f.includes(REASONING))).toBe(false);

    // The row holds the reasoning and the read withheld it, so what follows cannot be a turn that had
    // nothing to say.
    const stored = await assistantRow(page, ownProjectId(), taskId, ANSWER, false);
    expect(stored, 'the reasoned answer never reached the store').toBeTruthy();
    expect(stored?.has_reasoning).toBe(true);
    expect(stored?.reasoning_content).toBe('[HIDDEN]');

    // On screen: the answer, and a toggle where the reasoning would have streamed in.
    const entry = page.locator('.chat-entry', { hasText: ANSWER }).first();
    await expect(entry).toContainText(ANSWER);
    await expect(entry.locator('.reasoning-content')).toHaveCount(0);
    await expect(entry.locator('.reasoning-toggle')).toBeVisible();

    // Asking is the only path the words have: the click is what goes to `?full=1`.
    await entry.locator('.reasoning-toggle').click();
    await expect(entry.locator('.reasoning-content')).toContainText(REASONING, { timeout: 10_000 });
    const revealed = await assistantRow(page, ownProjectId(), taskId, ANSWER, true);
    expect(revealed?.reasoning_content).toContain(REASONING);
  });

  test('an ordinary turn still streams its reasoning', async ({ page }) => {
    test.setTimeout(120_000);

    await apiPutConfig(page, { collapse_reasoning: false });
    await waitForConfig(page, (c) => c.collapse_reasoning === false);

    const taskId = await createTaskViaApi(page, ownProjectId(), `streamed-${Date.now()} llm-fixture:text`);
    const frames = recordFrames(page);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);
    await waitForTaskState(page, ownProjectId(), taskId, notInFlight, 60_000);
    await waitForSendButton(page);

    await sendTurn(page, ownProjectId(), taskId, `stream this ${Date.now()} llm-fixture:text_response_reasoning`);

    // Without the preference the words come as they are written and no count frame is owed.
    expect(frames.some((f) => f.includes(REASONING))).toBe(true);
    expect(frames.some((f) => f.includes('"reasoning_progress"'))).toBe(false);
  });

  // The mid-stream half: the count is all the browser is given, and the notice is all the reader gets from
  // it. The fast fixture answers before a screen can be read, so this pins the same chunks at one per 100ms.
  test('says the call is thinking while its reasoning is still arriving', async ({ page }) => {
    test.setTimeout(120_000);

    await apiPutConfig(page, { collapse_reasoning: true });
    await waitForConfig(page, (c) => c.collapse_reasoning === true);

    const taskId = await createTaskViaApi(page, ownProjectId(), `thinking-${Date.now()} llm-fixture:text`);
    const frames = recordFrames(page);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);
    await waitForTaskState(page, ownProjectId(), taskId, notInFlight, 60_000);
    await waitForSendButton(page);

    const seen = frames.length;
    // `sendTurn` waits for the answer, and this test reads the screen before the answer exists.
    const sent = await csrfFetch(page, `/api/projects/${ownProjectId()}/tasks/${taskId}/send`, {
      method: 'POST', body: { content: `think about this ${Date.now()} llm-fixture:text_response_reasoning_slow` },
    });
    expect(sent.ok).toBe(true);

    await expect
      .poll(() => frames.slice(seen).filter((f) => f.includes('"reasoning_progress"')).length, { timeout: 30_000 })
      .toBeGreaterThan(0);

    const notice = page.locator('.reasoning-pending');
    await expect(notice).toBeVisible();
    await expect(notice).toContainText('Processing');
    // Whether this turn earns a figure is the product's answer — six chunks at 100ms may not span the window
    // `formatReasoningRate` prices — so asserting either way would ride on machine speed. The figure and its
    // clock are `reasoning-block.unit.svelte.ts`'s, on a clock it controls.
    await expect(notice).toHaveText(/^\s*Processing( · [\d.,]+ tok\/s)?\s*$/);
    // A toggle over reasoning that has not finished arriving is the same claim made twice.
    await expect(page.locator('.reasoning-toggle')).toHaveCount(0);

    await waitForTaskState(page, ownProjectId(), taskId, notInFlight, 60_000);
    expect((await messageRows(page, ownProjectId(), taskId)).some((r) => r.role === 'assistant')).toBe(true);
  });
});
