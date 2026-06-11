import { test, expect } from '@playwright/test';

test.describe.serial('Thread URL Navigation', () => {
  let taskId: string = '';
  let projectId: string = '';
  let threadIds: string[] = [];
  const uniqueId = Date.now();

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should create task, fork, and update URL with thread param', async ({ page }) => {
    // Create task
    const result = await page.evaluate(async (uid: number) => {
      const projects = await (await fetch('/api/projects')).json();
      const pid = projects[0].id;
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: `Test thread URL ${uid}` }),
      });
      const task = await res.json();
      return { projectId: pid, taskId: task.id };
    }, uniqueId);

    projectId = result.projectId;
    taskId = result.taskId;

    // Navigate to the task
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Click Start Task if visible
    const startBtn = page.locator('button:has-text("Start Task")');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click();
      await page.waitForTimeout(1000);
    }

    // Send a user message to create a checkpoint
    const input = page.locator('textarea[placeholder]').first();
    await expect(input).toBeVisible({ timeout: 10000 });
    await input.fill(`Test message ${uniqueId}`);
    await input.press('Enter');
    await page.waitForTimeout(2000);

    // Verify checkpoint divider exists
    const checkpoint = page.locator('.checkpoint-divider');
    expect(await checkpoint.count()).toBeGreaterThanOrEqual(1);

    // Get threads before fork
    const beforeThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    threadIds = beforeThreads.map((t: any) => t.id);
    expect(threadIds.length).toBeGreaterThanOrEqual(1);

    // Reload to pick up updated task status (prop from parent)
    await page.reload();
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Fork via the message's fork button (edit form)
    const userMsg = page.locator('.chat-message.user').last();
    await userMsg.locator('.action-btn[aria-label*="Fork"]').click();
    await page.locator('.edit-form textarea').fill(`Fork ${Date.now()}`);
    await page.locator('.btn-edit-submit').click();
    await page.waitForTimeout(2000);

    // Verify fork created a new thread
    const afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(afterThreads.length).toBeGreaterThanOrEqual(2);

    // Find the new fork thread (the one that's active and not in the original list)
    const forkThread = afterThreads.find((t: any) => t.is_active && !threadIds.includes(t.id));
    expect(forkThread).toBeDefined();
    const forkThreadId = forkThread.id;

    // Verify URL was updated with thread param
    const url = page.url();
    expect(url).toContain(`thread=${forkThreadId}`);

    // Verify messages exist in the fork thread via API
    const forkMessages = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/messages`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(forkMessages.length).toBeGreaterThanOrEqual(1);

    // Verify conversation is NOT empty — messages from the fork should be visible
    const emptyState = page.locator('.chat-empty');
    await expect(emptyState).not.toBeVisible({ timeout: 5000 });
    // Should have user message entries
    const userMessages = page.locator('.chat-message.user');
    expect(await userMessages.count()).toBeGreaterThanOrEqual(1);
  });

  test('should update URL when switching threads via dropdown', async ({ page }) => {
    if (!taskId || !projectId) {
      test.skip();
    }

    // Navigate to the task (without thread param to load active thread)
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Send a message to ensure there are user messages with checkpoint dividers
    const input = page.locator('textarea[placeholder]').first();
    await expect(input).toBeVisible({ timeout: 10000 });
    await input.fill(`Follow-up message ${uniqueId}`);
    await input.press('Enter');
    await page.waitForTimeout(2000);

    // Wait for checkpoint divider to appear
    await page.waitForSelector('.checkpoint-divider', { timeout: 10000 });

    // Get all threads
    const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    if (threads.length < 2) {
      test.skip();
    }

    const currentActive = threads.find((t: any) => t.is_active);
    const otherThread = threads.find((t: any) => !t.is_active);
    expect(otherThread).toBeDefined();

    // Wait for thread button to appear (requires threads loaded + user messages present)
    await page.waitForSelector('.thread-btn', { timeout: 10000 });

    // Click the thread switch button to open dropdown
    await page.locator('.thread-btn').first().click();
    await page.waitForTimeout(500);

    // Click the other thread in the menu
    await page.locator('.thread-menu-item').filter({ hasText: otherThread.name }).click();
    await page.waitForTimeout(2000);

    // Verify URL was updated with the new thread param
    const url = page.url();
    expect(url).toContain(`thread=${otherThread.id}`);

    // Verify the switch took effect via API
    const afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const newActive = afterThreads.find((t: any) => t.is_active);
    expect(newActive.id).toBe(otherThread.id);
  });

  test('should load specified thread when navigating to URL with thread param', async ({ page }) => {
    if (!taskId || !projectId) {
      test.skip();
    }

    // Get all threads
    const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    if (threads.length < 2) {
      test.skip();
    }

    // Pick a non-active thread to navigate to
    const nonActiveThread = threads.find((t: any) => !t.is_active);
    if (!nonActiveThread) {
      test.skip();
    }

    // Navigate directly to the URL with thread param
    await page.goto(`/?task=${taskId}&thread=${nonActiveThread.id}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });
    await page.waitForTimeout(3000);

    // Verify the thread was switched via API
    const afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const newActive = afterThreads.find((t: any) => t.is_active);
    expect(newActive.id).toBe(nonActiveThread.id);
  });

  test('should restore previous thread on browser Back navigation', async ({ page }) => {
    if (!taskId || !projectId) {
      test.skip();
    }

    // Get all threads
    const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    if (threads.length < 2) {
      test.skip();
    }

    // Navigate to first thread
    const threadA = threads[0];
    await page.goto(`/?task=${taskId}&thread=${threadA.id}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Send a message so we have checkpoint dividers for the thread switch button
    const input = page.locator('textarea[placeholder]').first();
    await expect(input).toBeVisible({ timeout: 10000 });
    await input.fill(`Back nav message ${uniqueId}`);
    await input.press('Enter');
    await page.waitForTimeout(2000);

    // Wait for thread button to appear
    await page.waitForSelector('.thread-btn', { timeout: 10000 });

    // Verify thread A is active
    let afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    let activeThread = afterThreads.find((t: any) => t.is_active);
    expect(activeThread.id).toBe(threadA.id);

    // Switch to another thread via dropdown
    const threadB = threads.find((t: any) => t.id !== threadA.id);
    await page.locator('.thread-btn').first().click();
    await page.waitForTimeout(500);
    await page.locator('.thread-menu-item').filter({ hasText: threadB.name }).click();
    await page.waitForTimeout(2000);

    // Verify thread B is active and URL updated
    expect(page.url()).toContain(`thread=${threadB.id}`);
    afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    activeThread = afterThreads.find((t: any) => t.is_active);
    expect(activeThread.id).toBe(threadB.id);

    // Go back in browser history
    await page.goBack();
    await page.waitForTimeout(3000);

    // Verify URL reflects thread A
    expect(page.url()).toContain(`thread=${threadA.id}`);

    // Verify thread A is active again
    afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    activeThread = afterThreads.find((t: any) => t.is_active);
    expect(activeThread.id).toBe(threadA.id);
  });
});
