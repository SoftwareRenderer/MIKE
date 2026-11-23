import { test, expect, type Page } from '@playwright/test';
import {
  apiPutConfig, clearTasksAfterEach, createTaskViaApi, csrfFetch, isolatedProject,
  navigateToTask, ownProjectId, waitForChatForm, waitForConfig, waitForSendButton,
  waitForTaskState, notInFlight,
} from './helpers';

// This file writes /api/config, which belongs to the server rather than to a project, so it runs on the
// isolated lane and puts the preference back when it is done.
isolatedProject();

// A live task is charged against the executor's memory budget until it is deleted, so they go between
// tests, not at the end of the file.
clearTasksAfterEach();

test.describe.configure({ mode: 'serial' });

// What only a browser and a real stop can settle: that the notice saying the model is thinking goes away
// with the run that raised it, and is not put back by what that run says on its way out. Which frame ends
// it is `stream-handler.unit.ts`'s claim; which two fields of an entry draw it is
// `reasoning-block.unit.svelte.ts`'s.

const NOTICE = '.reasoning-pending';

/**
 * A turn pinned to the reasoning fixture slowed until a stop can land inside its reasoning phase: the
 * notice must be on screen first, because a stop issued after the call stopped reasoning proves nothing
 * about the notice.
 */
async function startThinking(page: Page, label: string): Promise<string> {
  const projectId = ownProjectId();
  const taskId = await createTaskViaApi(page, projectId, `stop-thinking ${label} ${Date.now()} llm-fixture:text`);
  await navigateToTask(page, taskId, projectId);
  await waitForChatForm(page);
  await waitForTaskState(page, projectId, taskId, notInFlight, 60_000);
  await waitForSendButton(page);

  // `sendTurn` waits for the answer; this test stops before one exists.
  const sent = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
    method: 'POST',
    body: { content: `think about this ${Date.now()} llm-fixture:text_response_reasoning_very_slow` },
  });
  expect(sent.ok).toBe(true);

  await expect(page.locator(NOTICE)).toBeVisible({ timeout: 30_000 });
  return taskId;
}

/** The stopped task's chat holds nothing that claims a run is in flight. */
async function expectNothingStanding(page: Page, projectId: string, taskId: string): Promise<void> {
  await waitForTaskState(page, projectId, taskId, (t) => t?.execution_state === 'paused');

  await expect(page.locator(NOTICE)).toHaveCount(0);
  // The cancelled call is still writing while the transition lands, and a count from that flush would
  // repaint the notice and put the card's Running badge back.
  await page.waitForTimeout(2000);
  await expect(page.locator(NOTICE)).toHaveCount(0);
  // The form is back to asking for a message, which is the screen's own answer to "is it still running".
  await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15_000 });
}

test.describe('a stop and the thinking notice', () => {
  test.beforeEach(async ({ page }) => {
    await apiPutConfig(page, { collapse_reasoning: true });
    await waitForConfig(page, (c) => c.collapse_reasoning === true);
  });

  test.afterEach(async ({ page }) => {
    // Each test states the answer it needs; this only cleans up after one that failed halfway.
    await apiPutConfig(page, { collapse_reasoning: false });
    await waitForConfig(page, (c) => c.collapse_reasoning === false);
  });

  test('the chat\'s own Stop leaves nothing drawn as thinking', async ({ page }) => {
    test.setTimeout(120_000);
    const projectId = ownProjectId();
    const taskId = await startThinking(page, 'chat');

    await page.locator('.btn-stop').click();
    await expectNothingStanding(page, projectId, taskId);
  });

  // Nothing in this chat acts on this stop: only the WS frames arrive, which is the path a second browser
  // or the board's own Stop button takes.
  test('a stop this chat did not start leaves nothing drawn as thinking', async ({ page }) => {
    test.setTimeout(120_000);
    const projectId = ownProjectId();
    const taskId = await startThinking(page, 'remote');

    const res = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' });
    expect(res.ok).toBe(true);
    await expectNothingStanding(page, projectId, taskId);
  });
});
