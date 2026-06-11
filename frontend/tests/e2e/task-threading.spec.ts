import { test, expect } from '@playwright/test';

test.describe.serial('Task Threading (Checkpoints + Forks)', () => {
  let taskId: string = '';
  let projectId: string = '';
  const uniqueId = Date.now();

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should create task, send message, and see checkpoint divider', async ({ page }) => {
    // Get project ID and create task
    const result = await page.evaluate(async (uid: number) => {
      const projects = await (await fetch('/api/projects')).json();
      const pid = projects[0].id;
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: `Test threading ${uid}` }),
      });
      const task = await res.json();
      return { projectId: pid, taskId: task.id };
    }, uniqueId);

    projectId = result.projectId;
    taskId = result.taskId;

    // Navigate to the task
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Click Start Task
    const startBtn = page.locator('button:has-text("Start Task")');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click();
      await page.waitForTimeout(1000);
    }

    // Send a user message
    const input = page.locator('textarea[placeholder]').first();
    await expect(input).toBeVisible({ timeout: 10000 });
    await input.fill(`Test message ${uniqueId}`);
    await input.press('Enter');

    // Wait for message to appear
    await page.waitForTimeout(2000);

    // Check that checkpoint divider exists
    const checkpoint = page.locator('.checkpoint-divider');
    expect(await checkpoint.count()).toBeGreaterThanOrEqual(1);

    // Verify thread data via API
    const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    expect(threads.length).toBeGreaterThanOrEqual(1);
    expect(threads[0].name).toBe('Main');
  });

  test('should fork from checkpoint and verify via API', async ({ page }) => {
    if (!taskId || !projectId) {
      test.skip();
    }

    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Verify we have exactly 1 thread before forking
    const beforeThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(beforeThreads.length).toBeGreaterThanOrEqual(1);

    // Reload to pick up updated task status (prop from parent)
    await page.reload();
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Fork via the message's fork button (edit form)
    const userMsg = page.locator('.chat-message.user').last();
    await userMsg.locator('.action-btn[aria-label*="Fork"]').click();
    await page.locator('.edit-form textarea').fill(`Fork ${Date.now()}`);
    await page.locator('.btn-edit-submit').click();
    await page.waitForTimeout(2000);

    // Verify fork created a new thread via API
    const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    expect(threads.length).toBeGreaterThanOrEqual(2);

    // Verify the new thread is active and named "Fork #N"
    const activeThread = threads.find((t: any) => t.is_active);
    expect(activeThread).toBeDefined();
    expect(activeThread.name).toContain('Fork');

    // Verify the fork has a git branch
    expect(activeThread.git_branch).toBeDefined();
    expect(activeThread.git_branch).toContain('thread/');
  });

  test('should show edited message in fork and trigger LLM response', async ({ page }) => {
    // Create a fresh task for this test to avoid dependencies on prior test state
    const forkUniqueId = Date.now();
    const result = await page.evaluate(async (uid: number) => {
      const projects = await (await fetch('/api/projects')).json();
      const pid = projects[0].id;
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: `Fork test task ${uid}` }),
      });
      const task = await res.json();
      return { projectId: pid, taskId: task.id };
    }, forkUniqueId);

    const forkPid = result.projectId;
    const forkTid = result.taskId;

    // Navigate to the task
    await page.goto(`/?task=${forkTid}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Click Start Task if visible
    const startBtn = page.locator('button:has-text("Start Task")');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click();
      await page.waitForTimeout(2000);
    }

    // Wait for the task prompt (first user message) and LLM response to appear
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-message.assistant').first()).toBeVisible({ timeout: 10000 });

    // Get threads before fork
    const threadsBefore = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: forkPid, tid: forkTid });
    const threadCountBefore = threadsBefore.length;

    // Fork the task prompt message with edited content
    const forkContent = `Edited fork content ${forkUniqueId}`;
    const firstUserMsg = page.locator('.chat-message.user').first();
    await firstUserMsg.locator('.action-btn[aria-label*="Fork"]').click();
    await page.locator('.edit-form textarea').fill(forkContent);
    await page.locator('.btn-edit-submit').click();

    // Wait for fork to complete, history to reload, and LLM to respond
    await page.waitForTimeout(5000);

    // Verify the edited message appears in the chat
    await expect(page.locator('.chat-message.user').filter({ hasText: 'Edited fork content' })).toBeVisible({ timeout: 10000 });

    // Verify a new fork thread was created
    const threadsAfter = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: forkPid, tid: forkTid });
    expect(threadsAfter.length).toBeGreaterThanOrEqual(threadCountBefore + 1);
    const activeThread = threadsAfter.find((t: any) => t.is_active);
    expect(activeThread).toBeDefined();
    expect(activeThread.name).toContain('Fork');

    // Verify the LLM responded (assistant message visible)
    await expect(page.locator('.chat-message.assistant').first()).toBeVisible({ timeout: 10000 });
  });

  test('should switch between threads via API', async ({ page }) => {
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

    const currentActive = threads.find((t: any) => t.is_active);
    const otherThread = threads.find((t: any) => !t.is_active);
    expect(otherThread).toBeDefined();

    // Switch thread via API
    await page.evaluate(async (params: { pid: string; tid: string; targetId: string }) => {
      await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads/switch`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: params.targetId }),
      });
    }, { pid: projectId, tid: taskId, targetId: otherThread.id });

    // Verify switch
    const afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    const newActive = afterThreads.find((t: any) => t.is_active);
    expect(newActive.id).toBe(otherThread.id);
    expect(newActive.id).not.toBe(currentActive.id);
  });
});
