import { test, expect, Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

test.describe.serial('Sidebar Stats Real-Time Updates', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  /** Get sidebar stat count from the API (source of truth). */
  async function getApiCounts(page: Page): Promise<{ backlog: number; in_progress: number; review: number }> {
    return await page.evaluate(async () => {
      const res = await fetch('/api/projects/counts');
      const allCounts = await res.json();
      // Return counts for the first project
      const firstProjectId = Object.keys(allCounts)[0];
      return allCounts[firstProjectId];
    });
  }

  /** Get sidebar stat count from the UI for the active project. */
  async function getUiCount(page: Page, title: string): Promise<number> {
    const badge = page.locator('.stat-badge[title="' + title + '"]').first();
    const text = await badge.textContent();
    return parseInt(text?.trim() || '0', 10);
  }

  test('should show correct initial counts', async ({ page }) => {
    const counts = await getApiCounts(page);
    expect(counts.backlog).toBeGreaterThanOrEqual(2); // global-setup creates 2 tasks
    // in_progress/review may be non-zero from parallel tests; just verify UI matches API

    // Verify UI matches API
    const uiBacklog = await getUiCount(page, 'Backlog');
    expect(uiBacklog).toBe(counts.backlog);
  });

  test('should update counts when a task is created', async ({ page }) => {
    const projectId = await getProjectId(page);
    const countsBefore = await getApiCounts(page);

    // Create a task via API (triggers BroadcastTaskUpdated → BroadcastProjectCounts)
    const taskId = await createTaskViaApi(page, projectId, `sidebar-stats-create-${Date.now()}`);

    // Wait for WebSocket to deliver the updated counts to the UI
    const countsAfter = await page.waitForFunction(
      (expectedBacklog) => {
        // We check via a stored global that the WS callback updates
        // Instead, poll the API which is the source of truth
        return fetch('/api/projects/counts')
          .then(r => r.json())
          .then(all => {
            const firstId = Object.keys(all)[0];
            return all[firstId].backlog === expectedBacklog;
          });
      },
      countsBefore.backlog + 1,
      { timeout: 5000 }
    );

    // Verify the UI stat badge reflects the new count
    await expect(async () => {
      const uiBacklog = await getUiCount(page, 'Backlog');
      expect(uiBacklog).toBe(countsBefore.backlog + 1);
    }).toPass({ timeout: 5000 });

    // Clean up: delete the test task
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
  });

  test('should update counts when a task is moved to In Progress', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Get an existing backlog task
    const taskId = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      const backlogTask = board.tasks.find((t: any) => t.status === 'backlog');
      return backlogTask?.id || '';
    }, projectId);
    expect(taskId).not.toBe('');

    const countsBefore = await getApiCounts(page);

    // Move task to in_progress via API (CSRF-aware)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Wait for counts to update via WebSocket
    await expect(async () => {
      const counts = await getApiCounts(page);
      expect(counts.backlog).toBe(countsBefore.backlog - 1);
      // in_progress may be 1 or more (if the task auto-started)
      expect(counts.in_progress).toBeGreaterThanOrEqual(1);
    }).toPass({ timeout: 5000 });

    // Verify UI reflects the change
    await expect(async () => {
      const uiBacklog = await getUiCount(page, 'Backlog');
      expect(uiBacklog).toBe(countsBefore.backlog - 1);
    }).toPass({ timeout: 5000 });
  });

  test('should update counts when a task is moved to Review', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Find or create an in_progress task, or use a backlog task and move it
    let taskId = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      const ipTask = board.tasks.find((t: any) => t.status === 'in_progress');
      if (ipTask) return ipTask.id;
      const backlogTask = board.tasks.find((t: any) => t.status === 'backlog');
      return backlogTask?.id || '';
    }, projectId);
    expect(taskId).not.toBe('');

    const countsBefore = await getApiCounts(page);

    // Move task to review via API (CSRF-aware)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Wait for counts to update
    await expect(async () => {
      const counts = await getApiCounts(page);
      expect(counts.review).toBeGreaterThan(countsBefore.review);
    }).toPass({ timeout: 5000 });

    // Verify UI reflects the change
    await expect(async () => {
      const uiReview = await getUiCount(page, 'Review');
      const counts = await getApiCounts(page);
      expect(uiReview).toBe(counts.review);
    }).toPass({ timeout: 5000 });
  });

  test('should update counts when a task is deleted', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create a task, then delete it
    const taskId = await createTaskViaApi(page, projectId, `sidebar-stats-delete-${Date.now()}`);

    const countsBefore = await getApiCounts(page);
    expect(countsBefore.backlog).toBeGreaterThan(0);

    // Delete the task via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });

    // Wait for counts to update
    await expect(async () => {
      const counts = await getApiCounts(page);
      expect(counts.backlog).toBe(countsBefore.backlog - 1);
    }).toPass({ timeout: 5000 });

    // Verify UI reflects the change
    await expect(async () => {
      const uiBacklog = await getUiCount(page, 'Backlog');
      const counts = await getApiCounts(page);
      expect(uiBacklog).toBe(counts.backlog);
    }).toPass({ timeout: 5000 });
  });

  // --- Page title tests ---

  /** Get the project name from the API. */
  async function getProjectName(page: Page): Promise<string> {
    return await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      const projects = await res.json();
      return projects[0]?.name || '';
    });
  }

  test('should show project name in page title without review count when 0', async ({ page }) => {
    const projectName = await getProjectName(page);
    const counts = await getApiCounts(page);

    if (counts.review === 0) {
      await expect(page).toHaveTitle(projectName, { timeout: 5000 });
    } else {
      // If there are review tasks from previous tests, check the format
      // Title format: "🟡 {name} - {count} task(s) awaiting review"
      const title = await page.title();
      expect(title).toMatch(/🟡.*awaiting review/);
    }
  });

  test('should update page title when tasks move to/from Review', async ({ page }) => {
    const projectId = await getProjectId(page);
    const projectName = await getProjectName(page);

    // Ensure review count starts at 0 by finding any review tasks and moving them away
    const reviewTasks = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'review');
    }, projectId);

    for (const task of reviewTasks) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
        method: 'POST',
        body: { status: 'backlog' },
      });
    }

    // Wait for review count to be 0
    await expect(async () => {
      const counts = await getApiCounts(page);
      expect(counts.review).toBe(0);
    }).toPass({ timeout: 5000 });

    // Title should be just the project name (no review count)
    await expect(page).toHaveTitle(projectName, { timeout: 5000 });

    // Move a backlog task to review
    const taskId1 = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      const backlogTask = board.tasks.find((t: any) => t.status === 'backlog');
      return backlogTask?.id || '';
    }, projectId);
    expect(taskId1).not.toBe('');

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId1}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Title should now show review count
    // Format: "🟡 {name} - {count} task(s) awaiting review"
    await expect(async () => {
      const title = await page.title();
      expect(title).toMatch(/🟡.*awaiting review/);
    }).toPass({ timeout: 5000 });

    // Move another backlog task to review
    const taskId2 = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      const backlogTask = board.tasks.find((t: any) => t.status === 'backlog');
      return backlogTask?.id || '';
    }, projectId);

    if (taskId2) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId2}/move`, {
        method: 'POST',
        body: { status: 'review' },
      });

      // Title should still show review count (updated)
      await expect(async () => {
        const title = await page.title();
        expect(title).toMatch(/🟡.*awaiting review/);
      }).toPass({ timeout: 5000 });
    }

    // Delete one review task
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId1}`, { method: 'DELETE' });

    // Title should still show review count (if taskId2 exists)
    if (taskId2) {
      await expect(async () => {
        const title = await page.title();
        expect(title).toMatch(/🟡.*awaiting review/);
      }).toPass({ timeout: 5000 });
    }

    // Delete the remaining review task
    if (taskId2) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId2}`, { method: 'DELETE' });
    }

    // Title should drop the review count back to just project name
    await expect(page).toHaveTitle(projectName, { timeout: 5000 });
  });
});
