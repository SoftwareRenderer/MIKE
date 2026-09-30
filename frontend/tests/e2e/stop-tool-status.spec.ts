import { test, expect } from '@playwright/test';
import {
  createTaskViaApi, navigateToTask, waitForChatForm, waitForTaskState,
  isolatedProject, csrfFetch, getProjectId, gotoProject,
  PENDING_TOOL_STATUS as PENDING,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

// The slow-tool-probe fixture streams one bash call argument-slice by argument-slice (~4s), which
// is the window a stop must land in: the client has seen the tool_call frame, and the cancelled
// turn will never execute it, persist it or answer it.

test.describe.configure({ mode: 'serial' });

async function startStreamingToolCall(page: any, projectId: string, label: string): Promise<string> {
  const taskId = await createTaskViaApi(page, projectId, `slow-tool-probe ${label} ${Date.now()}`);
  await navigateToTask(page, taskId, projectId);
  await waitForChatForm(page);
  await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 25000 });
  await expect(page.locator(PENDING)).toBeVisible({ timeout: 25000 });
  return taskId;
}

test.describe('Stop and the tool card', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('a stop this chat did not start leaves no card running', async ({ page }) => {
    test.setTimeout(60000);
    const taskId = await startStreamingToolCall(page, projectId, 'remote');

    // Nothing in this chat acts on the stop: only the WS frames arrive.
    const res = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' });
    expect(res.ok).toBe(true);
    await waitForTaskState(page, projectId, taskId, (t) => t?.execution_state === 'paused');

    await expect(page.locator(PENDING), 'a stopped turn cannot answer its tool call').toHaveCount(0);
    // And it does not come back when the abandoned turn's replay buffer is consulted.
    await page.waitForTimeout(2000);
    await expect(page.locator(PENDING)).toHaveCount(0);
  });

  test('the chat\'s own Stop leaves no card running', async ({ page }) => {
    test.setTimeout(60000);
    const taskId = await startStreamingToolCall(page, projectId, 'chat');

    await page.locator('.btn-stop').click();
    await waitForTaskState(page, projectId, taskId, (t) => t?.execution_state === 'paused');

    await expect(page.locator(PENDING), 'a stopped turn cannot answer its tool call').toHaveCount(0);
  });

  test('a task that is still running keeps the card of its live call', async ({ page }) => {
    test.setTimeout(60000);
    const taskId = await startStreamingToolCall(page, projectId, 'live');

    // The other half of the rule: an unanswered call on a task that IS in flight is live work.
    await page.waitForTimeout(1500);
    await expect(page.locator(PENDING)).toHaveCount(1);
    await expect(page.locator('.tool-call-card').first()).toContainText('Execute');

    // Cleanup: leave the task paused so no gate slot stays held.
    await page.locator('.btn-stop').click();
    await waitForTaskState(page, projectId, taskId, (t) => t?.execution_state === 'paused');
  });
});
