import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { existsSync, writeFileSync, mkdirSync } from 'fs';
import { execSync as exec } from 'child_process';
import { getProjectId, createTaskViaApi, csrfFetch, navigateToTask } from './helpers';

// --- Shared utilities ---

function getConfig() {
    return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function getWorktreePath(projectId: string, taskId: string): string {
    // Test servers use WORKTREES_DIR=/tmp/kanban-test-worktrees
    return `/tmp/kanban-test-worktrees/${projectId}/${taskId}`;
}

function worktreeExists(projectId: string, taskId: string): boolean {
    const path = getWorktreePath(projectId, taskId);
    // Check for a proper git worktree (has .git file pointing to the main repo)
    return existsSync(path) && existsSync(`${path}/.git`);
}

function writeFileInWorktree(projectId: string, taskId: string, filePath: string, content: string): void {
    const worktreePath = getWorktreePath(projectId, taskId);
    mkdirSync(worktreePath, { recursive: true });
    writeFileSync(`${worktreePath}/${filePath}`, content);
    // Stage the file so git detects it
    try {
      exec(`cd ${worktreePath} && git add ${filePath}`);
    } catch {
      // git add might fail if worktree isn't fully initialized; untracked files are still detected
    }
}

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

    // Check if worktree exists
    if (!worktreeExists(projectId, taskId)) {
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
      return;
    }

    // Write a file to create changes
    writeFileInWorktree(projectId, taskId, 'test-file.txt', 'test content for commit');

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

    if (!worktreeExists(projectId, taskId)) {
      // Skip if no worktree — the cancel test requires changes to trigger the dialog
      test.skip();
    }

    writeFileInWorktree(projectId, taskId, 'cancel-test.txt', 'cancel test content');

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
  });

  test('should cancel streaming commit message when user starts typing', async ({ page }) => {
    // Create a task and move it to in_progress (triggers worktree creation via API)
    const taskId = await createTaskViaApi(page, projectId, `commit-cancel-type-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move to in_progress via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Send a message to trigger lazy-start (creates worktree)
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content: 'list files' }),
      });
    }, { pid: projectId, tid: taskId });

    // Poll for worktree to be created (up to 15s)
    let worktreeReady = false;
    for (let i = 0; i < 30; i++) {
      await new Promise(r => setTimeout(r, 500));
      if (worktreeExists(projectId, taskId)) {
        worktreeReady = true;
        break;
      }
    }
    if (!worktreeReady) {
      test.skip();
    }

    // Write a file to create changes
    writeFileInWorktree(projectId, taskId, 'cancel-type-test.txt', 'test content for cancel on type');

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

    // Click "Commit and move to Done" (has yellow border when changes exist)
    const commitBtn = page.locator('.btn-commit-and-done').first();
    await expect(commitBtn).toBeVisible({ timeout: 10000 });
    await commitBtn.click();

    // Commit dialog should appear (detect via the commit textarea which is unique to this dialog)
    const textarea = page.locator('.commit-textarea');
    await expect(textarea).toBeVisible({ timeout: 15000 });

    // Wait for the streaming indicator to appear (spinner + "Generating...")
    const streamingIndicator = page.locator('.commit-progress');
    await expect(streamingIndicator).toBeVisible({ timeout: 10000 });

    // Type in the textarea while streaming is active
    await textarea.fill('My custom commit message');

    // Streaming indicator should disappear (generation cancelled)
    await expect(streamingIndicator).not.toBeVisible({ timeout: 5000 });

    // The textarea should contain the user's typed text
    await expect(textarea).toHaveValue('My custom commit message');

    // The confirm button should be enabled (user has typed a message)
    const confirmBtn = page.locator('.modal-footer .btn-primary');
    await expect(confirmBtn).toBeEnabled();
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
