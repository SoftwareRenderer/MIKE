import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForSendButton, csrfFetch } from './helpers';

test.describe.configure({ mode: 'parallel' });

// "New Thread" is the submit action of an edited, non-last user message: it forks the conversation there,
// replaces the forked user message with the edited text and sends it. The thread has to end up active AND
// the agent has to answer — a fork that leaves the task idle is a dead end the user has to notice.
test.describe('New Thread from an edited message', () => {
  test('forks, activates the thread and starts the agent', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `new-thread-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);
    await navigateToTask(page, taskId);
    await waitForSendButton(page);

    // Two more turns: the message in the middle is neither the first nor the last, which is what makes
    // the submit button a "New Thread" instead of a plain "Send".
    for (const ask of ['second', 'third']) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
        method: 'POST',
        body: { content: `${ask} ask ${Date.now()}` },
      });
      await waitForSendButton(page);
    }

    const threadsBefore = (await page.evaluate(async ({ pid, tid }) =>
      (await (await fetch(`/api/projects/${pid}/tasks/${tid}/threads`)).json()), { pid: projectId, tid: taskId }) as any[]).length;

    await page.locator('.chat-message.user').nth(1).locator('[aria-label="Start new thread from this message"]').click();
    const newText = `branched ask ${Date.now()}`;
    await page.locator('.edit-form textarea').fill(newText);
    await expect(page.locator('.btn-edit-submit')).toHaveAttribute('title', 'New Thread');
    const forkedAt = new Date();
    await page.locator('.btn-edit-submit').click();

    await expect(async () => {
      const threads = await page.evaluate(async ({ pid, tid }) =>
        (await (await fetch(`/api/projects/${pid}/tasks/${tid}/threads`)).json()), { pid: projectId, tid: taskId }) as any[];
      expect(threads.length).toBe(threadsBefore + 1);
      const active = threads.find(t => t.is_active);
      expect(active?.parent_thread_id).toBeTruthy();
      expect(await page.evaluate(async ({ pid, tid }) =>
        (await (await fetch(`/api/projects/${pid}/tasks/${tid}`)).json()).current_thread_id,
        { pid: projectId, tid: taskId })).toBe(active.id);
    }).toPass({ timeout: 15000 });

    // The edited text is the fork's live user message, not just the copied one.
    await expect(page.locator('.chat-message.user', { hasText: newText }).first()).toBeVisible({ timeout: 15000 });

    // The agent answers the forked question. Counting rows would be wrong — a fork carries the copied
    // history with it — so look for anything the agent said in this thread since the click.
    const since = forkedAt.toISOString();
    await expect
      .poll(async () => {
        const msgs = await page.evaluate(async ({ pid, tid }) => {
          const threads = await (await fetch(`/api/projects/${pid}/tasks/${tid}/threads`)).json();
          const active = threads.find(t => t.is_active);
          return active ? (await (await fetch(`/api/projects/${pid}/tasks/${tid}/messages?thread_id=${active.id}`)).json()) : [];
        }, { pid: projectId, tid: taskId }) as any[];
        return msgs.some(m => (m.role === 'assistant' || m.role === 'tool_call') && m.created_at >= since);
      }, { timeout: 30000, message: 'the forked question was never answered — the thread stayed idle' })
      .toBe(true);

    const task = await page.evaluate(async ({ pid, tid }) =>
      (await (await fetch(`/api/projects/${pid}/tasks/${tid}`)).json()), { pid: projectId, tid: taskId }) as any;
    expect(task.execution_state).toBe('idle');
  });
});
