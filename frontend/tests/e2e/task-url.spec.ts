import { test, expect } from '@playwright/test';
import {getProjectId, createTaskViaApi, navigateToTask, csrfFetch} from './helpers';

// Helper: wait for URL to match predicate (SPA-aware)
async function waitForUrl(page, predicate: (url: string) => boolean) {
    const startTime = Date.now();
    while (Date.now() - startTime < 5000) {
        const url = page.url();
        if (predicate(url)) return;
        await page.waitForTimeout(100);
    }
    throw new Error(`Timeout waiting for URL to match predicate`);
}

test.describe.configure({ mode: 'parallel' });
test.describe('Task URL Navigation', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should update URL when opening a task', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `url-open-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${taskId}`);
    expect(page.url()).toContain(`project=${projectId}`);
  });

  test('should clear task URL when closing task detail (keep project)', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `url-close-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await page.click('.btn-back');
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    expect(page.url()).not.toContain('task=');
    // Project param should be preserved
    expect(page.url()).toContain(`project=${projectId}`);
  });

  test('should open task from URL on direct navigation', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `url-direct-${Date.now()}`);
    expect(taskId).not.toBeNull();
    await navigateToTask(page, taskId, projectId);
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${taskId}`);
  });

  test('should handle browser back from task detail', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `url-back-${Date.now()}`);

    // Navigate to project first, then click the card (creates history entry via pushState)
    await page.goto(`/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible();

    // Wait for card and click it
    await page.waitForSelector('.card');
    const cards = page.locator('.card');
    const count = await cards.count();
    if (count > 0) {
        await cards.last().click();
    }
    await expect(page.locator('.task-detail-page')).toBeVisible();
    expect(page.url()).toContain('task=');

    // Click browser back — should return to project board
    await page.goBack();
    await waitForUrl(page, url => !url.includes('task='));
    await expect(page.locator('.board')).toBeVisible();
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
  });

  test('should clear URL param when task does not exist', async ({ page }) => {
    await page.goto('/?task=nonexistent-task-id');
    // Should show the board (not the task detail page)
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    // Task URL param should be cleared (wait for async cleanup)
    await expect(async () => {
      expect(page.url()).not.toContain('task=');
    }).toPass({ timeout: 5000 });
  });

  test('should restore project on page refresh', async ({ page }) => {
    // Create or find a second project (CSRF-aware)
    let secondProjectId: string | null = null;
    const projects = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      return res.json();
    });
    const existing = projects.find((p: any) => p.path === '/tmp/kanban-test-repo-2');
    if (existing) {
      secondProjectId = existing.id;
    } else {
      const result = await csrfFetch(page, '/api/projects', {
        method: 'POST',
        body: { name: 'refresh-project', path: '/tmp/kanban-test-repo-2' },
      });
      if (!result.ok) throw new Error(`Failed to create project: ${result.status}`);
      secondProjectId = result.data.id;
    }

    // Navigate to the second project
    await page.goto(`/?project=${secondProjectId}`);
    await page.waitForSelector('.project-item, .board', { timeout: 10000 });
    // Verify we're on the second project (URL should have its ID)
    expect(page.url()).toContain(`project=${secondProjectId}`);

    // Refresh the page
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('.project-item, .board', { timeout: 10000 });
    // Should still be on the second project after refresh
    expect(page.url()).toContain(`project=${secondProjectId}`);
  });

  test('should restore project and task on page refresh', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `refresh-task-${Date.now()}`);

    // Navigate to task with project param
    await navigateToTask(page, taskId, projectId);
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${taskId}`);

    // Refresh the page
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    // Should restore both project and task
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).toContain(`task=${taskId}`);
  });
});

test.describe.serial('Task URL Navigation — Task Switching', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should switch to a different task from the CardDetail sidebar', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `switch-task-1-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `switch-task-2-${Date.now()}`);

    // Open first task
    await page.goto(`/?project=${projectId}&task=${taskId1}`);
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${taskId1}`);

    // Click the second task in the CardDetail sidebar
    const secondTaskBtn = page.locator('.column-context .card').filter({ hasText: 'switch-task-2' });
    await expect(secondTaskBtn).toBeVisible();
    await secondTaskBtn.click();

    // URL should update to the new task (project param preserved)
    expect(page.url()).toContain(`task=${taskId2}`);
    expect(page.url()).toContain(`project=${projectId}`);
    // Task detail should still be visible (not navigated away)
    await expect(page.locator('.task-detail-page')).toBeVisible();
  });
});

test.describe.serial('Task URL Navigation — Project Switch', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show board (not task detail) when switching projects', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `proj-switch-${Date.now()}`);

    // Open a task in the first project
    await navigateToTask(page, taskId, projectId);
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${taskId}`);

    // Close task detail (sidebar is hidden in task-detail mode)
    await page.click('.btn-back');
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // Create a second project (may already exist from other parallel tests, CSRF-aware)
    let secondProjectId: string | null = null;
    const projects2 = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      return res.json();
    });
    const existing2 = projects2.find((p: any) => p.path === '/tmp/kanban-test-repo-2');
    if (existing2) {
      secondProjectId = existing2.id;
    } else {
      const result2 = await csrfFetch(page, '/api/projects', {
        method: 'POST',
        body: { name: 'second-project', path: '/tmp/kanban-test-repo-2' },
      });
      if (!result2.ok) throw new Error(`Failed to create project: ${result2.status}`);
      secondProjectId = result2.data.id;
    }
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('.project-item, .task-detail-page', { timeout: 10000 });

    // Wait for at least two project items (parallel tests may add more)
    const projectItems = page.locator('.project-item');
    await expect(async () => {
        expect(await projectItems.count()).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 5000 });

    // Click the last project (the one we just added)
    const projectCount = await projectItems.count();
    await projectItems.nth(projectCount - 1).click();

    // Should show the board with no task detail open
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible({ timeout: 5000 });
    // URL should have the new project param, no task param
    expect(page.url()).not.toContain('task=');
    expect(page.url()).toContain('project=');
  });
});

test.describe.serial('Task URL Navigation — Stale Param Cleanup', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should remove thread param when clicking Back to board', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `stale-thread-back-${Date.now()}`);

    // Navigate to task with a thread param (simulating chat thread navigation)
    await page.goto(`/?project=${projectId}&task=${taskId}&thread=some-thread-id`);
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain('thread=some-thread-id');

    // Click Back to board
    await page.click('.btn-back');
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // URL should have project param but NOT task or thread params
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');
    expect(page.url()).not.toContain('thread=');
  });

  test('should remove thread param when switching tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `stale-thread-switch-1-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `stale-thread-switch-2-${Date.now()}`);

    // Navigate to first task with a thread param
    await page.goto(`/?project=${projectId}&task=${taskId1}&thread=some-thread-id`);
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain('thread=some-thread-id');

    // Click the second task in the CardDetail sidebar
    const secondTaskBtn = page.locator('.column-context .card').filter({ hasText: 'stale-thread-switch-2' });
    await expect(secondTaskBtn).toBeVisible();
    await secondTaskBtn.click();

    // URL should have the new task and project, but NOT the old thread param
    expect(page.url()).toContain(`task=${taskId2}`);
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('thread=');
  });

  test('should remove thread param when switching projects', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `stale-thread-proj-${Date.now()}`);

    // Navigate to task with a thread param
    await page.goto(`/?project=${projectId}&task=${taskId}&thread=some-thread-id`);
    await page.waitForSelector('.task-detail-page, .board', { timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain('thread=some-thread-id');

    // Close task detail first
    await page.click('.btn-back');
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // Create and switch to a second project (CSRF-aware)
    let secondProjectId: string | null = null;
    const projects3 = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      return res.json();
    });
    const existing3 = projects3.find((p: any) => p.path === '/tmp/kanban-test-repo-2');
    if (existing3) {
      secondProjectId = existing3.id;
    } else {
      const result3 = await csrfFetch(page, '/api/projects', {
        method: 'POST',
        body: { name: 'stale-param-project', path: '/tmp/kanban-test-repo-2' },
      });
      if (!result3.ok) throw new Error(`Failed: ${result3.status}`);
      secondProjectId = result3.data.id;
    }
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('.project-item', { timeout: 10000 });

    const projectItems = page.locator('.project-item');
    await expect(async () => {
      expect(await projectItems.count()).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 5000 });

    // Click the last project
    const projectCount = await projectItems.count();
    await projectItems.nth(projectCount - 1).click();

    // URL should have the new project param, no task or thread params
    expect(page.url()).not.toContain('task=');
    expect(page.url()).not.toContain('thread=');
    expect(page.url()).toContain('project=');
  });
});
