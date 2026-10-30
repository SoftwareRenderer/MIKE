import { test, expect } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, csrfFetch, navigateToTask, isolatedProject, gotoProject,
} from './helpers';
// Each test file gets its own project, because it starts a turn: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe.serial('Merge Sync', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  // The sync itself is not measured here and cannot be reached from this body: nothing was committed on
  // main (the fixture only wrote an untracked file there), and the test read one fact — the task reached
  // `done`. Reaching done through the review bar, and the staged work landing in the project checkout,
  // belong to commit-dialog.spec.ts and commit-and-done-unstaged.spec.ts; syncing the target branch into
  // the task branch before the final merge — clean, conflicting, or with the worktree gone — is held by
  // TestSyncTargetIntoTask_CleanMerge and its siblings (backend/exec/sync_merge_test.go) over real git.

  test('should show merge_conflict badge when sync merge conflicts', async ({ page }) => {
    // This test verifies the merge_conflict state UI.
    // We simulate the state directly via API since inducing a real conflict
    // requires specific git setup that's hard to do through the web API.
    // We keep the task in backlog to avoid the executor overwriting our flag.

    const taskId = await createTaskViaApi(page, projectId, `merge-conflict-ui-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Set has_merge_conflict via API (task stays in backlog, no executor interference)
    const updateResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
      method: 'PUT',
      body: { has_merge_conflict: true },
    });
    expect(updateResult.ok).toBe(true);
    expect(updateResult.data?.has_merge_conflict).toBe(true);

    // Navigate to the task (full reload picks up the updated task data)
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for the side panel to render
    await page.waitForSelector('.column-context .card', { timeout: 10000 }).catch(() => {});

    // Check for the merge conflict badge on the card in the side panel
    // The badge should show "Resolving conflict" with orange color
    const badge = page.locator('.column-context .badge:has-text("Resolving conflict")');
    await expect(badge).toBeVisible({ timeout: 15000 });
  });
});
