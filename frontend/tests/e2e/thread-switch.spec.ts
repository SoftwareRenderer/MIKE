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

  test('should handle rapid thread switches without corruption', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `thread-switch-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);
    await navigateToTask(page, taskId);

    // Create a message to fork from
    const msgResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: 'Fork point message' },
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

    // Rapidly switch between threads via API (simulating rapid user clicks)
    // The server mutex should serialize these requests
    const switchResults = await page.evaluate(async ({ pid, tid, mainId, forkId }) => {
      // Get CSRF token
      const sessionRes = await fetch('/api/auth/session');
      const session = await sessionRes.json();
      const csrf = session.csrf_token || '';
      const results = [];
      const switches = [forkId, mainId, forkId, mainId]; // rapid back-and-forth
      for (const threadId of switches) {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/threads/switch`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            ...(csrf ? { 'X-CSRF-Token': csrf } : {}),
          },
          credentials: 'include',
          body: JSON.stringify({ thread_id: threadId }),
        });
        results.push({ threadId, status: res.status });
      }
      return results;
    }, { pid: projectId, tid: taskId, mainId: mainThreadId, forkId: forkThreadId });

    // All switches should succeed
    for (const result of switchResults) {
      expect(result.status).toBe(200);
    }

    // Verify final state: exactly one active thread
    const finalThreads = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    const activeThreads = finalThreads.filter((t: any) => t.is_active === true);
    expect(activeThreads.length).toBe(1);

    // Verify task's current_thread_id matches the active thread
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(taskData.current_thread_id).toBe(activeThreads[0].id);
  });

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
