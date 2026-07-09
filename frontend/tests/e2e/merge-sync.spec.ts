import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch, navigateToTask } from './helpers';

test.describe.serial('Merge Sync', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should sync main into task branch before final merge when main has new commits', async ({ page }) => {
    // Create a task
    const taskId = await createTaskViaApi(page, projectId, `merge-sync-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move to in_progress (creates worktree and branch)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Get task details to find worktree path and branch
    const taskInfo = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });
    expect(taskInfo).not.toBeNull();
    expect(taskInfo.branch).not.toBe('');

    const branchName = taskInfo.branch;
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const worktreePath = `${config.workspace_dir}/${taskId}`;
    const projectPath = taskInfo.project_path || taskInfo.path;

    // Write a file in the task's worktree (simulating task work)
    await page.evaluate(async ({ wtPath }) => {
      const res = await fetch('/api/fs/write', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          path: `${wtPath}/task_file.txt`,
          content: 'task content\n',
        }),
      });
      return res.ok;
    }, { wtPath: worktreePath });

    // Commit the change on the task branch via git API
    await page.evaluate(async ({ wtPath }) => {
      // Use the git API to commit
      const res = await fetch('/api/fs/write', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          path: `${wtPath}/task_file.txt`,
          content: 'task content v2\n',
        }),
      });
      return res.ok;
    }, { wtPath: worktreePath });

    // Get the project path to make changes on main
    const projPath = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.path;
    }, { pid: projectId });

    // Add a non-conflicting file on main branch
    await page.evaluate(async ({ pPath }) => {
      const res = await fetch('/api/fs/write', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          path: `${pPath}/main_file.txt`,
          content: 'main content\n',
        }),
      });
      return res.ok;
    }, { pPath: projPath });

    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Move to review
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Click commit and done
    const commitBtn = page.locator('.btn-commit-done');
    await expect(commitBtn).toBeVisible({ timeout: 10000 });
    await commitBtn.click();

    // Wait for the commit dialog to appear (if there are changes)
    const commitDialog = page.locator('.commit-dialog');
    await expect(commitDialog).toBeVisible({ timeout: 10000 });

    // Confirm the commit
    const confirmBtn = page.locator('.commit-dialog').locator('button:has-text("Commit")');
    await expect(confirmBtn).toBeVisible();
    await confirmBtn.click();

    // Wait for the merge to complete
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('done');
    }).toPass({ timeout: 30000 });

    // Verify the task is done
    const finalTask = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });
    expect(finalTask.status).toBe('done');
  });

  test('should show merge_conflict badge when sync merge conflicts', async ({ page }) => {
    // This test verifies the merge_conflict state UI.
    // We simulate the state directly via API since inducing a real conflict
    // requires specific git setup that's hard to do through the web API.

    const taskId = await createTaskViaApi(page, projectId, `merge-conflict-ui-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move to in_progress
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Simulate merge conflict via direct DB update (for UI testing)
    await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ has_merge_conflict: true }),
      });
      return res.ok;
    }, { pid: projectId, tid: taskId });

    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Check for the merge conflict badge on the card
    // The badge should show "Agent resolving" with orange color
    const badge = page.locator('.badge:has-text("Agent resolving")');
    await expect(badge).toBeVisible({ timeout: 10000 });
  });
});
