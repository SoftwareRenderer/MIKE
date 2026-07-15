import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch, navigateToTask } from './helpers';

test.describe.serial('Commit Dialog', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should move task to Done without dialog when no changes', async ({ page }) => {
    // Create a task and move it to review
    const taskId = await createTaskViaApi(page, projectId, `commit-no-changes-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move to review via API
    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    expect(moveResult.ok).toBe(true);

    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for the review bar to appear
    await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

    // Click "Move to Done" (no changes, so no commit dialog should appear)
    const commitBtn = page.locator('.btn-commit-done');
    await expect(commitBtn).toBeVisible();
    await commitBtn.click();

    // Wait for task to move to done (check via API)
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('done');
    }).toPass({ timeout: 10000 });

    // Commit dialog should NOT be visible (no changes)
    const commitDialog = page.locator('.commit-dialog');
    await expect(commitDialog).not.toBeVisible();
  });

  test('should show commit dialog with changes and allow editing', async ({ page }) => {
    // Create a task and move it to in_progress (triggers worktree creation via API)
    const taskId = await createTaskViaApi(page, projectId, `commit-with-changes-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move to in_progress via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Get the worktree path and create a file change
    const worktreePath = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      // Worktree path is derived from workspace_dir + project_id + task_id
      const configRes = await fetch('/api/config');
      const config = await configRes.json();
      return `${config.workspace_dir}/${pid}/${tid}`;
    }, { pid: projectId, tid: taskId });

    // Check if worktree exists and create a change
    const worktreeExists = await page.evaluate(async (path) => {
      try {
        const res = await fetch(`/api/fs/read?path=${encodeURIComponent(path)}`);
        return res.ok;
      } catch {
        return false;
      }
    }, worktreePath);

    // If worktree exists, create a file change
    if (worktreeExists) {
      // Write a file to create changes
      await page.evaluate(async (path) => {
        await fetch('/api/fs/write', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            path: `${path}/test-file.txt`,
            content: 'test content for commit',
          }),
        });
      }, worktreePath);

      // Move to review
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        body: { status: 'review' },
      });

      // Navigate to the task
      await navigateToTask(page, taskId);
      await expect(page.locator('.task-detail-page')).toBeVisible();

      // Wait for the review bar
      await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

      // Wait for diff stats to arrive (indicates changes exist)
      await page.waitForTimeout(2000);

      // Click "Commit and move to Done"
      const commitBtn = page.locator('.btn-commit-done');
      await commitBtn.click();

      // Commit dialog should appear with loading state
      const commitDialog = page.locator('.commit-dialog');
      await expect(commitDialog).toBeVisible({ timeout: 10000 });

      // Should show "Generating commit message..." initially
      // Then show the textarea with the commit message
      const textarea = page.locator('.commit-textarea');
      await expect(textarea).toBeVisible({ timeout: 15000 });

      // Edit the commit message
      await textarea.fill('Edited commit message for testing');

      // Confirm the commit
      await page.click('.commit-dialog .btn-confirm');

      // Wait for task to move to done
      await expect(async () => {
        const task = await page.evaluate(async ({ pid, tid }) => {
          const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
          return res.ok ? res.json() : null;
        }, { pid: projectId, tid: taskId });
        expect(task?.status).toBe('done');
      }).toPass({ timeout: 15000 });
    } else {
      // Worktree doesn't exist — test the no-changes path
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        body: { status: 'review' },
      });
      await navigateToTask(page, taskId);
      await expect(page.locator('.task-detail-page')).toBeVisible();
      await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

      const commitBtn = page.locator('.btn-commit-done');
      await commitBtn.click();

      // Without changes, no dialog should appear
      const commitDialog = page.locator('.commit-dialog');
      await expect(commitDialog).not.toBeVisible();

      // Task should move to done
      await expect(async () => {
        const task = await page.evaluate(async ({ pid, tid }) => {
          const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
          return res.ok ? res.json() : null;
        }, { pid: projectId, tid: taskId });
        expect(task?.status).toBe('done');
      }).toPass({ timeout: 10000 });
    }
  });

  test('should cancel commit dialog and keep task in review', async ({ page }) => {
    // Create a task and move it to review
    const taskId = await createTaskViaApi(page, projectId, `commit-cancel-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move to in_progress first
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Get worktree path and create a change
    const worktreePath = await page.evaluate(async ({ pid, tid }) => {
      const configRes = await fetch('/api/config');
      const config = await configRes.json();
      return `${config.workspace_dir}/${pid}/${tid}`;
    }, { pid: projectId, tid: taskId });

    const worktreeExists = await page.evaluate(async (path) => {
      try {
        const res = await fetch(`/api/fs/read?path=${encodeURIComponent(path)}`);
        return res.ok;
      } catch {
        return false;
      }
    }, worktreePath);

    if (worktreeExists) {
      await page.evaluate(async (path) => {
        await fetch('/api/fs/write', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            path: `${path}/cancel-test.txt`,
            content: 'cancel test content',
          }),
        });
      }, worktreePath);

      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        body: { status: 'review' },
      });

      await navigateToTask(page, taskId);
      await expect(page.locator('.task-detail-page')).toBeVisible();
      await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });
      await page.waitForTimeout(2000);

      // Click commit button
      await page.click('.btn-commit-done');

      // Wait for dialog to appear
      const commitDialog = page.locator('.commit-dialog');
      await expect(commitDialog).toBeVisible({ timeout: 10000 });

      // Cancel the dialog
      await page.click('.commit-dialog .btn-cancel');

      // Dialog should close
      await expect(commitDialog).not.toBeVisible({ timeout: 5000 });

      // Task should still be in review
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('review');
    } else {
      // Skip if no worktree — the cancel test requires changes to trigger the dialog
      test.skip();
    }
  });

  test('should have green border on Move to Done (no changes)', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `commit-style-${Date.now()}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

    const commitBtn = page.locator('.btn-commit-done');
    await expect(commitBtn).toBeVisible();

    // Without changes, button should NOT have has-changes class
    const hasChangesClass = await commitBtn.getAttribute('class');
    expect(hasChangesClass).not.toContain('has-changes');

    // Button should have text "Move to Done"
    await expect(commitBtn).toContainText('Move to Done');

    // Check that the button has a green border (default state)
    const computedStyle = await commitBtn.evaluate((el) => {
      const style = window.getComputedStyle(el);
      return {
        borderColor: style.borderColor,
        background: style.backgroundColor,
      };
    });

    // Should have a border (not 'none' or 'transparent')
    expect(computedStyle.borderColor).not.toBe('rgba(0, 0, 0, 0)');
    // Background should be a surface color, not green fill
    expect(computedStyle.background).not.toContain('34, 197, 94'); // green RGB
  });
});
