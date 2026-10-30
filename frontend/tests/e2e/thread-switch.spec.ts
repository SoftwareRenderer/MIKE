import { test, expect } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted, isolatedProject, gotoProject,
} from './helpers';
// Each test file gets its own project, because it starts a turn: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe.configure({ mode: 'parallel' });
test.describe('Thread Switch', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  // Every switch answering 200 and leaving exactly one active thread is the handler's serialisation, and
  // `TestThreadSwitchConcurrent` (backend/server/thread_handler_test.go) holds it: Main and a fork, a
  // switch to each thrown at the same mutex, the same status check, the same `is_active` count and
  // `current_thread_id` assertion. This body drove those calls through `page.evaluate` and read nothing
  // the screen drew, so it added neither a git fact nor a browser one. What the Go test cannot read is the
  // conversation the browser draws for the thread it landed on, which is the spec below.

  test('should switch thread via UI and update chat entries', async ({ page }) => {
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
    const forkThreadId = forkResult.data?.id;

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
