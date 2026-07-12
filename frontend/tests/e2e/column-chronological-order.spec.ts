import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

test.describe.serial('Column Chronological Order', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should order tasks by last activity with most recent at bottom', async ({ page }) => {
    // Create 3 tasks in sequence (task1 oldest, task3 newest)
    const task1 = await createTaskViaApi(page, projectId, `chrono-order-1-${Date.now()}`);
    const task2 = await createTaskViaApi(page, projectId, `chrono-order-2-${Date.now()}`);
    const task3 = await createTaskViaApi(page, projectId, `chrono-order-3-${Date.now()}`);

    // Navigate to board and wait for cards to render
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Wait for all 3 tasks to appear in the board API
    await expect(async () => {
      const board = await page.evaluate(async ({ pid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return res.json();
      }, { pid: projectId });
      const backlogTasks = board.tasks.filter((t: any) => t.status === 'backlog');
      expect(backlogTasks.length).toBeGreaterThanOrEqual(3);
    }).toPass({ timeout: 10000 });

    // Move task1 (oldest) to review first, then task2, then task3 — each with a delay
    // so their updated_at timestamps are distinct
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${task1}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Wait 1.1s for distinct second-level timestamps
    await page.waitForTimeout(1100);

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${task2}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    await page.waitForTimeout(1100);

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${task3}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Navigate to board and wait for cards to re-render in the Review column
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Wait for the Review column to have at least 3 tasks
    await expect(async () => {
      const board = await page.evaluate(async ({ pid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return res.json();
      }, { pid: projectId });
      const reviewTasks = board.tasks.filter((t: any) => t.status === 'review');
      expect(reviewTasks.length).toBeGreaterThanOrEqual(3);
    }).toPass({ timeout: 10000 });

    // Verify the API returns tasks sorted by updated_at (task1 first, task3 last among the three)
    const reviewOrder = await page.evaluate(async ({ pid, ids }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      const reviewTasks = board.tasks.filter((t: any) => t.status === 'review');
      // Find the indices of our 3 tasks within the review column
      const ordered: string[] = [];
      for (const task of reviewTasks) {
        if (ids.includes(task.id)) {
          ordered.push(task.id);
        }
      }
      return ordered;
    }, { pid: projectId, ids: [task1, task2, task3] });

    // task1 was moved first (oldest updated_at), task3 was moved last (newest)
    // Expected order: task1 at top (index 0), task2 middle (index 1), task3 bottom (index 2)
    expect(reviewOrder[0]).toBe(task1);
    expect(reviewOrder[1]).toBe(task2);
    expect(reviewOrder[2]).toBe(task3);

    // Also verify the visual DOM order in the Review column matches
    const reviewColumn = page.locator('.column').nth(2); // Review is the 3rd column (0-indexed)
    const reviewCards = reviewColumn.locator('.card');

    // Get the visible card order in the DOM
    const domOrder = await reviewCards.evaluateAll(
      (cards) => cards.map((card) => card.getAttribute('data-task-id')).filter(Boolean) as string[]
    );

    // Our 3 tasks should appear in chronological order within the DOM
    const filteredDomOrder = domOrder.filter((id) => [task1, task2, task3].includes(id));
    expect(filteredDomOrder[0]).toBe(task1);
    expect(filteredDomOrder[1]).toBe(task2);
    expect(filteredDomOrder[2]).toBe(task3);
  });

  test('should place a newly-moved task at the bottom of the target column', async ({ page }) => {
    // Create 2 tasks
    const existingTask = await createTaskViaApi(page, projectId, `chrono-existing-${Date.now()}`);
    const newTask = await createTaskViaApi(page, projectId, `chrono-new-${Date.now()}`);

    // Move existingTask to done first
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${existingTask}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });

    // Wait for distinct timestamp
    await page.waitForTimeout(1100);

    // Move newTask to done — it should appear below existingTask
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${newTask}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });

    // Navigate to board
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Wait for both tasks to be in done
    await expect(async () => {
      const board = await page.evaluate(async ({ pid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return res.json();
      }, { pid: projectId });
      const doneTasks = board.tasks.filter((t: any) => t.status === 'done');
      expect(doneTasks.length).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 10000 });

    // Verify API order: existingTask (older) before newTask (newer)
    const doneOrder = await page.evaluate(async ({ pid, ids }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      const doneTasks = board.tasks.filter((t: any) => t.status === 'done');
      const ordered: string[] = [];
      for (const task of doneTasks) {
        if (ids.includes(task.id)) {
          ordered.push(task.id);
        }
      }
      return ordered;
    }, { pid: projectId, ids: [existingTask, newTask] });

    expect(doneOrder[0]).toBe(existingTask);
    expect(doneOrder[1]).toBe(newTask);
  });
});
