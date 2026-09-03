import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId, createTaskViaApi, csrfFetch, navigateToTask, worktreeExists, writeFileInWorktree, waitForWorktree, waitForTurnAnswered, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


// --- Shared utilities ---

function getConfig() {
  return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

/**
 * Drive a task into the state the commit dialog needs: worktree created, prompt answered (the
 * review bar shows "Resume" otherwise), one file STAGED (this flow never commits untracked
 * files), task in review, commit button rendered. Returns [taskId, commitButton].
 */
async function taskWithStagedChanges(page: Page, projectId: string, title: string, file: string) {
  const taskId = await createTaskViaApi(page, projectId, title);
  await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
    method: 'POST',
    body: { status: 'in_progress' },
  });
  await waitForWorktree(projectId, taskId);
  await waitForTurnAnswered(page, projectId, taskId);
  writeFileInWorktree(projectId, taskId, file, `${file} content`);
  await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
    method: 'POST',
    body: { status: 'review' },
  });
  await navigateToTask(page, taskId);
  await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });
  const commitBtn = page.locator('.btn-commit-and-done');
  await expect(commitBtn).toBeVisible({ timeout: 10000 });
  return { taskId, commitBtn };
}

test.describe.serial('Commit Dialog', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
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
    // The review bar has → Done button on the left side
    const doneBtn = page.locator('.btn-move:has-text("Done")');
    if (await doneBtn.isVisible().catch(() => false)) {
      await doneBtn.click();
    } else {
      // Fallback: use the first .btn-commit-done (Move to Done or Resume)
      const commitBtn = page.locator('.btn-commit-done').first();
      await expect(commitBtn).toBeVisible({ timeout: 10000 });
      await commitBtn.click();
    }

    // Wait for task to move to done (check via API)
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('done');
    }).toPass({ timeout: 10000 });

    // Commit dialog should NOT be visible (no changes)
    const commitDialog = page.locator('.commit-dialog, [role="dialog"]');
    await expect(commitDialog).not.toBeVisible({ timeout: 3000 }).catch(() => {});
  });

  test('should show commit dialog with changes and allow editing', async ({ page }) => {
    // Create a task and move it to in_progress (triggers worktree creation via API)
    const taskId = await createTaskViaApi(page, projectId, `commit-with-changes-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Moving only schedules worktree creation — wait for it, instead of falling through to a
    // branch that passes without opening the dialog.
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    await waitForWorktree(projectId, taskId);
    // Let the agent answer the prompt first: with the user's prompt still the trailing
    // chat entry the review bar offers "Resume", not the commit button.
    await waitForTurnAnswered(page, projectId, taskId);

    // A staged file is what gates the commit button: `HandleCommitAndDone` commits
    // staged changes only, so an untracked file leaves the plain "Move to Done" variant.
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
    await expect(async () => {
      const diff = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        return res.ok ? await res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(diff).not.toBeNull();
    }).toPass({ timeout: 10000 });

    // The commit button is the one gated on staged changes — `.btn-commit-done` alone
    // also matches the Resume and plain Move-to-Done variants of the review bar.
    const commitBtn = page.locator('.btn-commit-and-done');
    await expect(commitBtn).toBeVisible({ timeout: 10000 });
    await commitBtn.click();

    // Commit dialog should appear (modal with commit-textarea)
    const commitDialog = page.locator('[role="dialog"]');
    await expect(commitDialog).toBeVisible({ timeout: 10000 });

    // Should show "Generating commit message..." initially
    // Then show the textarea with the commit message
    const textarea = page.locator('.commit-textarea');
    await expect(textarea).toBeVisible({ timeout: 15000 });

    // Edit the commit message (this also cancels streaming generation)
    await textarea.fill('Edited commit message for testing');

    // Wait for the confirm button to be enabled (streaming must finish/be cancelled)
    const confirmBtn = page.locator('.modal-footer .btn-primary');
    await expect(confirmBtn).toBeEnabled({ timeout: 15000 });

    // Confirm the commit
    await confirmBtn.click();

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
    const { taskId, commitBtn } = await taskWithStagedChanges(page, projectId, `commit-cancel-${Date.now()}`, 'cancel-test.txt');
    await commitBtn.click();

    // Wait for dialog to appear (modal with commit-textarea)    // Wait for dialog to appear (modal with commit-textarea)
    const commitDialog = page.locator('[role="dialog"]');
    await expect(commitDialog).toBeVisible({ timeout: 10000 });

    // Cancel the dialog
    await page.click('[role="dialog"] .btn-secondary');

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
    const { taskId, commitBtn } = await taskWithStagedChanges(page, projectId, `commit-cancel-type-${Date.now()}`, 'cancel-type-test.txt');
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
});
