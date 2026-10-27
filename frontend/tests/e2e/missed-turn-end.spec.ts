import { test, expect, type Page } from '@playwright/test';
import {
  createTaskViaApi, csrfFetch, ensureTaskStarted, gotoProject, isolatedProject, notInFlight, ownProjectId,
} from './helpers';

isolatedProject();

/**
 * Hold up the answer to this task's own row read: the request goes out when the screen asks, and the bytes the
 * server produced then land when the spec says. That is the only way to arrange the ordering under test, and
 * the out-of-band reads below use `page.request` because a read through the page would wait on this one.
 */
function landTaskRowLate(page: Page, projectId: string, taskId: string): Promise<() => void> {
  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => { release = resolve; });
  return page
    .route((url) => url.pathname === `/api/projects/${projectId}/tasks/${taskId}`, async (route) => {
      const response = await route.fetch();
      await held;
      await route.fulfill({ response });
    })
    .then(() => release);
}

async function taskRow(page: Page, projectId: string, taskId: string): Promise<any> {
  const res = await page.request.get(`/api/projects/${projectId}/tasks/${taskId}`);
  expect(res.ok(), `the task row is unreadable: ${res.status()}`).toBe(true);
  return res.json();
}

const pollRow = (page: Page, projectId: string, taskId: string, ask: (t: any) => boolean, timeout: number) =>
  expect.poll(async () => ask(await taskRow(page, projectId, taskId)), { timeout }).toBe(true);

test.describe('A turn that ended while the screen was opening', () => {
  test('the chat stops saying the agent is responding once the turn has ended', async ({ page }) => {
    test.setTimeout(180_000);
    await gotoProject(page);
    const projectId = ownProjectId();
    const stamp = Date.now();
    const prompt = `late row read ${stamp} llm-fixture:text`;
    const taskId = await createTaskViaApi(page, projectId, prompt);
    await ensureTaskStarted(page, projectId, taskId);
    await pollRow(page, projectId, taskId, notInFlight, 90_000);

    const releaseRowRead = await landTaskRowLate(page, projectId, taskId);
    await gotoProject(page);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST', body: { content: `second turn long-running-probe ${stamp}` },
    });
    await pollRow(page, projectId, taskId, (t) => t.execution_state === 'running', 90_000);

    // Opened from the card, never by URL: a reload re-reads the board and the race starts over clean.
    await page.locator('.card', { hasText: prompt }).first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    await pollRow(page, projectId, taskId, notInFlight, 120_000);
    releaseRowRead();

    await expect(page.locator('.chat-message.user', { hasText: `second turn long-running-probe ${stamp}` }),
      'the transcript arrived, so this is a chat that opened onto the task').toBeVisible();
    await expect(page.locator('.btn-send'),
      'the turn is over and the chat still offers Stop: it kept its own late reading of the task over the '
      + 'board copy, and nothing else is coming to tell it the turn ended').toBeVisible({ timeout: 15_000 });
  });
});
