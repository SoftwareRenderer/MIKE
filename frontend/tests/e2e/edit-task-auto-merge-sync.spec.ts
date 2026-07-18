import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  csrfFetch,
  navigateToTask,
  getTaskData,
} from './helpers';

/**
 * Test that the Edit Task Modal reflects live server-side changes to auto_merge.
 *
 * When a parent task's subtasks_auto_merge is toggled, the server propagates
 * the change to all descendant tasks' auto_merge values. The edit modal should
 * pick up these changes in real-time via WebSocket, even while open.
 */
test.describe.serial('Edit Task Modal — Auto-merge live sync', () => {
  let projectId: string;
  let parentTaskId: string;
  let childTaskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should reflect server-side auto_merge change while modal is open', async ({ page }) => {
    // 1. Create a parent task with subtasks_auto_merge = true
    const parentResult = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `parent-task-${Date.now()}`,
        auto_merge: true,
        subtasks_auto_merge: true,
      },
    });
    expect(parentResult.ok).toBe(true);
    parentTaskId = parentResult.data?.id;
    expect(parentTaskId).not.toBe('');

    // 2. Create a child task (inherits auto_merge from parent's subtasks_auto_merge)
    const childResult = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `child-task-${Date.now()}`,
        parent_task_id: parentTaskId,
      },
    });
    expect(childResult.ok).toBe(true);
    childTaskId = childResult.data?.id;
    expect(childTaskId).not.toBe('');

    // Verify child has auto_merge = true (inherited from parent)
    let childData = await getTaskData(page, projectId, childTaskId);
    expect(childData.auto_merge).toBe(true);

    // 3. Navigate to the child task and open the edit modal
    await navigateToTask(page, childTaskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    const card = page.locator('.card.selected');
    await expect(card).toBeVisible({ timeout: 5000 });

    const editButton = card.locator('button[aria-label="Edit task"]');
    await expect(editButton).toBeVisible();
    await editButton.click();

    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // 4. Verify the auto-merge toggle is ON (reflecting inherited value)
    const autoMergeToggle = modal.locator('button[aria-label="Auto-merge on completion"]');
    await expect(autoMergeToggle).toBeVisible();
    // The toggle should have the "active" class when checked
    await expect(autoMergeToggle).toHaveClass(/active/);

    // 5. While modal is open, change parent's subtasks_auto_merge to false via API
    // This triggers server-side propagation to all descendants
    const parentUpdateResult = await csrfFetch(
      page,
      `/api/projects/${projectId}/tasks/${parentTaskId}`,
      {
        method: 'PUT',
        body: {
          ...(await getTaskData(page, projectId, parentTaskId)),
          subtasks_auto_merge: false,
        },
      }
    );
    expect(parentUpdateResult.ok).toBe(true);

    // 6. Wait for the WebSocket task_updated event to propagate and the modal to reflect it
    // The toggle should now be OFF (auto_merge propagated to false)
    await expect(async () => {
      const toggleClass = await autoMergeToggle.getAttribute('class');
      // When the toggle is OFF, it should NOT have the "active" class
      expect(toggleClass).not.toMatch(/active/);
    }).toPass({ timeout: 5000 });

    // 7. Verify via API that the child's auto_merge was actually updated by the server
    childData = await getTaskData(page, projectId, childTaskId);
    expect(childData.auto_merge).toBe(false);

    // 8. Cancel the modal (don't save)
    const cancelButton = page.locator('.modal-footer .btn-secondary');
    await cancelButton.click();
    await expect(modal).not.toBeVisible({ timeout: 5000 });

    // 9. Verify the child's auto_merge is still false (server state is authoritative)
    childData = await getTaskData(page, projectId, childTaskId);
    expect(childData.auto_merge).toBe(false);
  });

  test('should preserve user edits over live-sync updates', async ({ page }) => {
    // Navigate to the child task from the previous test
    await navigateToTask(page, childTaskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Current state: child auto_merge = false (from parent propagation in previous test)
    let childData = await getTaskData(page, projectId, childTaskId);
    expect(childData.auto_merge).toBe(false);

    // Open the edit modal
    const card = page.locator('.card.selected');
    await expect(card).toBeVisible({ timeout: 5000 });

    const editButton = card.locator('button[aria-label="Edit task"]');
    await editButton.click();

    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Verify toggle starts as OFF (auto_merge = false)
    const autoMergeToggle = modal.locator('button[aria-label="Auto-merge on completion"]');
    await expect(autoMergeToggle).toBeVisible();
    await expect(autoMergeToggle).not.toHaveClass(/active/);

    // User manually toggles auto-merge ON
    await autoMergeToggle.click();
    await expect(autoMergeToggle).toHaveClass(/active/);

    // While modal is open, change parent's subtasks_auto_merge back to true
    // This would normally propagate auto_merge=true to the child
    const parentUpdateResult = await csrfFetch(
      page,
      `/api/projects/${projectId}/tasks/${parentTaskId}`,
      {
        method: 'PUT',
        body: {
          ...(await getTaskData(page, projectId, parentTaskId)),
          subtasks_auto_merge: true,
        },
      }
    );
    expect(parentUpdateResult.ok).toBe(true);

    // Give the WebSocket event time to arrive
    await page.waitForTimeout(500);

    // The toggle should STILL be ON because the user already touched it
    // (user edits take priority over live-sync)
    await expect(autoMergeToggle).toHaveClass(/active/);

    // Save the changes
    const saveButton = page.locator('.modal-footer .btn-primary');
    await saveButton.click();
    await expect(modal).not.toBeVisible({ timeout: 5000 });

    // Verify the child's auto_merge is true (user's saved value)
    childData = await getTaskData(page, projectId, childTaskId);
    expect(childData.auto_merge).toBe(true);
  });
});
