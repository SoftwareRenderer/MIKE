import { test, expect } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForSendButton, csrfFetch,
  isolatedProject, gotoProject,
} from './helpers';
// Each test file gets its own project, because it starts a turn: see isolatedProject() in helpers.ts.
isolatedProject();

// Which conversation the task view draws: the thread the task is on, and no other. Both directions of that
// one routing decision are driven here — a fork the click made becomes the conversation on screen and starts
// answering, and a switch back to Main draws Main's rows and not the fork's.
//
// The handler's half is Go's: `TestThreadSwitchConcurrent` (one active thread),
// `TestForkRefusesAForkPointFromAnotherThread`, `backend/store/threads_test.go` (the copied rows) and
// `backend/exec/duplicate_send_test.go` (a regenerate is never a duplicate submit). What only a browser
// reads is the transcript the screen draws and the button that becomes "New Thread".

test.describe.configure({ mode: 'parallel' });

test.describe('New Thread from an edited message', () => {
  test('forks, activates the thread and starts the agent', async ({ page }) => {
    // Four sequential executor turns; the default budget covers one.
    test.setTimeout(240_000);

    await gotoProject(page);

    const projectId = await getProjectId(page);
    // The creation prompt is pinned like the two below: a task auto-starts, and with the plan-first skill
    // installed an unpinned prompt is a plan that keeps starting work, so no wait for the first idle is
    // big enough.
    const taskId = await createTaskViaApi(page, projectId, `new-thread-${Date.now()} llm-fixture:text`);
    await ensureTaskStarted(page, projectId, taskId);
    await navigateToTask(page, taskId, projectId);
    await waitForSendButton(page, 75_000);

    // Two more turns: the message in the middle is neither the first nor the last, which is what makes
    // the submit button a "New Thread" instead of a plain "Send".
    for (const ask of ['second', 'third']) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
        method: 'POST',
        // Pinned to a plain text answer: unpinned, the plan route answers with a create_task plan,
        // which keeps starting work and never returns the task to idle.
        body: { content: `${ask} ask ${Date.now()} llm-fixture:text` },
      });
      // Turn budget: other workers hold turns on this server too, and the queue decides how long
      // "idle" takes.
      await waitForSendButton(page, 75_000);
    }

    const threadsBefore = (await page.evaluate(async ({ pid, tid }) =>
      (await (await fetch(`/api/projects/${pid}/tasks/${tid}/threads`)).json()), { pid: projectId, tid: taskId }) as any[]).length;

    await page.locator('.chat-message.user').nth(1).locator('[aria-label="Start new thread from this message"]').click();
    const newText = `branched ask ${Date.now()} llm-fixture:text`;
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

test.describe('Switching to another thread', () => {
  test('draws the thread it landed on, and not the one it left', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `thread-ui-switch-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);
    await navigateToTask(page, taskId);

    // Wait for chat form to appear
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

    // Create a message to fork from
    const msgResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: 'Fork point for UI test' },
    });
    expect(msgResult.ok).toBe(true);
    const msgId = msgResult.data?.id;

    // Get the main thread
    const threadsResult = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const mainThreadId = threadsResult[0]?.id;

    // Fork to create a second thread
    const forkResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/fork`, {
      method: 'POST',
      body: { from_thread_id: mainThreadId, from_message_id: msgId },
    });
    expect(forkResult.ok).toBe(true);

    // Add a unique message to the fork thread
    const forkMsgResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: 'Unique message in fork thread' },
    });
    expect(forkMsgResult.ok).toBe(true);

    // Switch back to main thread via API
    const switchResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: mainThreadId },
    });
    expect(switchResult.ok).toBe(true);

    // Reload the page to verify thread state persisted
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The fork thread's unique message should NOT appear in main thread's messages
    await expect(page.locator('text=Unique message in fork thread')).not.toBeVisible();
  });
});
