import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  csrfFetch,
  navigateToTask,
  getTaskData,
} from './helpers';

// ─── Review Bar (serial — shares projectId across tests) ───

test.describe.serial('Review Bar', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should show review bar when task is in review state', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `review-bar-appears-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move task to review via API
    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    expect(moveResult.ok).toBe(true);

    // Navigate to the task detail page
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for the review bar to appear
    const reviewBar = page.locator('.chat-review-bar');
    await expect(reviewBar).toBeVisible({ timeout: 10000 });

    // Should contain the "→ Done" move button
    const doneBtn = reviewBar.locator('.btn-move:has-text("Done")');
    await expect(doneBtn).toBeVisible();

    // Should contain the "→ Backlog" move button
    const backlogBtn = reviewBar.locator('.btn-move:has-text("Backlog")');
    await expect(backlogBtn).toBeVisible();

    // Should contain the action button (Resume or Move to Done depending on entries)
    const actionBtn = reviewBar.locator('.btn-commit-done');
    await expect(actionBtn).toBeVisible();
  });

  test('should move task to Done via → Done button in review bar', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `review-move-to-done-${Date.now()}`);
    expect(taskId).not.toBe('');

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for the review bar
    await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

    // Click "→ Done" button (in the move-buttons section)
    const doneBtn = page.locator('.btn-move:has-text("Done")');
    await expect(doneBtn).toBeVisible();
    await doneBtn.click();

    // Verify task moved to done via API
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('done');
    }).toPass({ timeout: 10000 });

    // The review bar should now show done-state buttons (→ Review, → Backlog)
    const reviewBar = page.locator('.chat-review-bar');
    await expect(reviewBar).toBeVisible();
    const toReviewBtn = reviewBar.locator('.btn-move:has-text("Review")');
    await expect(toReviewBtn).toBeVisible();
  });

  test('should move task to Backlog via → Backlog button in review bar', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `review-move-to-backlog-${Date.now()}`);
    expect(taskId).not.toBe('');

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for the review bar
    await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

    // Click "→ Backlog" button
    const backlogBtn = page.locator('.btn-move:has-text("Backlog")');
    await expect(backlogBtn).toBeVisible();
    await backlogBtn.click();

    // Verify task moved to backlog via API
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('backlog');
    }).toPass({ timeout: 10000 });
  });

  test('should not show review bar in backlog state', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `review-hidden-backlog-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Navigate to the task (created in backlog state)
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Review bar should NOT be visible in backlog state
    await expect(page.locator('.chat-review-bar')).not.toBeVisible({ timeout: 5000 });

    // Also verify no review-specific buttons appear
    const doneBtn = page.locator('.btn-move:has-text("Done")');
    const reviewBtn = page.locator('.btn-move:has-text("Review")');
    await expect(doneBtn).not.toBeVisible({ timeout: 3000 });
    await expect(reviewBtn).not.toBeVisible({ timeout: 3000 });
  });
});

// ─── Stats Modal (parallel — independent tests) ───

/**
 * Helper: close the stats modal via the close button.
 */
async function closeStatsModal(page: test.Page): Promise<void> {
  await page.locator('.close-btn').click();
  await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
}

test.describe.parallel('Stats Modal', () => {
  test('should show empty state when no token usage data exists', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);

    // First verify via API that the project has no token stats.
    // This avoids race conditions with parallel tests that create token data.
    const stats = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/stats`);
      return res.ok ? res.json() : null;
    }, projectId);

    // If there are existing stats from parallel tests, skip the empty-state assertion
    // and instead verify the modal opens and loads properly.
    const hasStats = stats?.by_model && stats.by_model.length > 0;

    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Verify the top bar is visible (required for the stats button)
    const topBar = page.locator('.top-bar');
    await expect(topBar).toBeVisible({ timeout: 5000 });

    // Click the stats button in the top bar (desktop only)
    const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
    await expect(statsBtn).toBeVisible({ timeout: 5000 });
    await statsBtn.click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.modal-header h2')).toContainText('Token Usage');

    // Wait for loading to finish
    await expect(page.locator('.loading')).not.toBeVisible({ timeout: 5000 }).catch(() => {});

    if (!hasStats) {
      // With no LLM messages, the empty state message should appear
      await expect(page.locator('.empty')).toContainText('No token usage data yet');
    } else {
      // When stats exist from parallel tests, verify the summary card is shown
      await expect(page.locator('.summary-card')).toBeVisible({ timeout: 5000 });
    }

    await closeStatsModal(page);
  });

  test('should close the modal when clicking the close button', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Verify the top bar is visible
    const topBar = page.locator('.top-bar');
    await expect(topBar).toBeVisible({ timeout: 5000 });

    // Click the stats button
    const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
    await expect(statsBtn).toBeVisible({ timeout: 5000 });
    await statsBtn.click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    await closeStatsModal(page);
  });

  test('should close the modal when pressing Escape', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Verify the top bar is visible
    const topBar = page.locator('.top-bar');
    await expect(topBar).toBeVisible({ timeout: 5000 });

    // Click the stats button
    const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
    await expect(statsBtn).toBeVisible({ timeout: 5000 });
    await statsBtn.click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Press Escape to close
    await page.keyboard.press('Escape');
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should show per-provider breakdown in the model table', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `stats-provider-${Date.now()}`);

    // Create an assistant message directly via API to populate token stats
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: {
        role: 'assistant',
        content: 'This is a test response for stats.',
        model_name: 'mock-model',
        provider_name: 'mock',
        prompt_tokens: 45,
        predicted_tokens: 8,
        cache_tokens: 12,
        prompt_ms: 320,
        predicted_ms: 180,
      },
    });
    expect(result.ok).toBe(true);

    // Navigate to project view and open stats modal
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Verify the top bar is visible
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });

    // Click the stats button
    const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
    await expect(statsBtn).toBeVisible({ timeout: 5000 });
    await statsBtn.click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Wait for loading to finish
    await expect(page.locator('.loading')).not.toBeVisible({ timeout: 5000 }).catch(() => {});

    // Model table should be visible
    const table = page.locator('.model-table');
    await expect(table).toBeVisible();

    // Table should have at least one data row
    const rows = table.locator('tbody tr');
    await expect(rows.first()).toBeVisible();

    // The model label should contain the provider/model info
    const modelLabel = rows.first().locator('.model-label');
    await expect(modelLabel).toBeVisible();
    const labelText = await modelLabel.textContent();
    expect(labelText).not.toBeNull();
    expect(labelText!.length).toBeGreaterThan(0);

    // Verify table columns exist (use exact match to avoid "Prompt Speed" matching "Prompt")
    await expect(table.locator('thead th').filter({ hasText: 'Model' })).toBeVisible();
    await expect(table.locator('thead th').filter({ hasText: 'Tasks' })).toBeVisible();
    await expect(table.locator('thead th').filter({ hasText: 'Cached' })).toBeVisible();
    await expect(table.locator('thead th').filter({ hasText: /^Prompt$/ })).toBeVisible();
    await expect(table.locator('thead th').filter({ hasText: 'Generated' })).toBeVisible();

    await closeStatsModal(page);
  });
});

// ─── Toast Notifications (parallel — independent tests) ───

test.describe.parallel('Toast Notifications', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    // Click on the project to open the board
    await page.click('.project-item');
    await page.waitForSelector('.board', { timeout: 10000 });
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should display error toast when action fails', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `toast-err-a-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `toast-err-b-${Date.now()}`);

    await expect(page.locator(`[data-task-id="${taskId1}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-task-id="${taskId2}"]`)).toBeVisible({ timeout: 5000 });

    // Intercept start task API calls and return 500 errors
    await page.route(
      `**/api/projects/${projectId}/tasks/${taskId1}/start`,
      (route) => {
        route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'Internal server error' }),
        });
      }
    );
    await page.route(
      `**/api/projects/${projectId}/tasks/${taskId2}/start`,
      (route) => {
        route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'Internal server error' }),
        });
      }
    );

    // Multi-select both cards via Ctrl+Click
    await page.locator(`[data-task-id="${taskId1}"]`).click({ modifiers: ['Control'] });
    await page.locator(`[data-task-id="${taskId2}"]`).click({ modifiers: ['Control'] });

    // Wait for the multi-select bar to appear
    await expect(page.locator('.multi-select-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.bar-btn-start')).toBeVisible({ timeout: 5000 });

    // Click the Start button
    await page.locator('.bar-btn-start').click();

    // Wait for the confirmation dialog (uses .confirm-message from ConfirmationDialog)
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();

    // Wait for the error toast to appear
    await expect(page.locator('.toast')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.toast')).toHaveAttribute('role', 'alert');
  });

  test('should display success toast when tasks are started', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `toast-suc-a-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `toast-suc-b-${Date.now()}`);

    await expect(page.locator(`[data-task-id="${taskId1}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-task-id="${taskId2}"]`)).toBeVisible({ timeout: 5000 });

    // Multi-select both cards
    await page.locator(`[data-task-id="${taskId1}"]`).click({ modifiers: ['Control'] });
    await page.locator(`[data-task-id="${taskId2}"]`).click({ modifiers: ['Control'] });

    await expect(page.locator('.multi-select-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.bar-btn-start')).toBeVisible({ timeout: 5000 });
    await page.locator('.bar-btn-start').click();

    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();

    // Wait for the success toast to appear
    await expect(page.locator('.toast')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.toast')).toHaveAttribute('role', 'alert');
    const toastText = await page.locator('.toast').textContent();
    expect(toastText).toContain('task');
    expect(toastText).toContain('started');
  });
});

// ─── Edit Task Modal (serial — shares projectId/taskId across tests) ───

test.describe.serial('Edit Task Modal', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should edit task title and save', async ({ page }) => {
    const originalTitle = `edit-title-${Date.now()}`;
    taskId = await createTaskViaApi(page, projectId, originalTitle);
    expect(taskId).not.toBe('');

    // Navigate to the task detail page
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Find the card for this task in the side panel (it will be selected)
    const card = page.locator('.card.selected');
    await expect(card).toBeVisible({ timeout: 5000 });

    // Click the edit button (pencil icon with aria-label="Edit task")
    const editButton = card.locator('button[aria-label="Edit task"]');
    await expect(editButton).toBeVisible();
    await editButton.click();

    // Verify the EditTaskModal appears
    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Verify modal title
    const modalTitle = page.locator('#modal-title');
    await expect(modalTitle).toContainText('Edit Task');

    // Verify the title input is pre-filled with the original task name
    const titleInput = page.locator('#edit-task-title-input');
    await expect(titleInput).toBeVisible();
    await expect(titleInput).toHaveValue(originalTitle);

    // Change the title
    const newTitle = `renamed-task-${Date.now()}`;
    await titleInput.clear();
    await titleInput.fill(newTitle);

    // Click Save
    const saveButton = page.locator('.modal-footer .btn-primary');
    await expect(saveButton).toBeVisible();
    await saveButton.click();

    // Modal should close
    await expect(modal).not.toBeVisible({ timeout: 5000 });

    // Wait for the card to update with the new title
    const cardTitle = page.locator('.card.selected .card-title-text');
    await expect(cardTitle).toContainText(newTitle, { timeout: 5000 });

    // Verify the new title via API
    const taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.title).toBe(newTitle);
  });

  test('should edit task prompt and save', async ({ page }) => {
    // Navigate to the task (created in previous test)
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Open the edit modal
    const card = page.locator('.card.selected');
    await expect(card).toBeVisible({ timeout: 5000 });

    const editButton = card.locator('button[aria-label="Edit task"]');
    await editButton.click();

    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Get the current prompt for later comparison
    const currentData = await getTaskData(page, projectId, taskId);
    const currentPrompt = currentData.prompt as string;

    // Verify the prompt textarea is pre-filled
    const promptInput = page.locator('#edit-task-prompt-input');
    await expect(promptInput).toBeVisible();
    await expect(promptInput).toHaveValue(currentPrompt);

    // Change the prompt
    const newPrompt = `updated prompt ${Date.now()}`;
    await promptInput.clear();
    await promptInput.fill(newPrompt);

    // Click Save
    const saveButton = page.locator('.modal-footer .btn-primary');
    await expect(saveButton).toBeVisible();
    await saveButton.click();

    // Modal should close
    await expect(modal).not.toBeVisible({ timeout: 5000 });

    // Verify the new prompt via API
    const taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.prompt).toBe(newPrompt);
  });

  test('should cancel without saving and reject empty name', async ({ page }) => {
    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Get the current title from API (should be the renamed title from previous test)
    const currentData = await getTaskData(page, projectId, taskId);
    const currentTitle = currentData.title as string;

    // Open the edit modal
    const card = page.locator('.card.selected');
    await expect(card).toBeVisible({ timeout: 5000 });

    const editButton = card.locator('button[aria-label="Edit task"]');
    await editButton.click();

    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Change the title to something different
    const titleInput = page.locator('#edit-task-title-input');
    await titleInput.clear();
    await titleInput.fill(`cancelled-change-${Date.now()}`);

    // Click Cancel
    const cancelButton = page.locator('.modal-footer .btn-secondary');
    await expect(cancelButton).toBeVisible();
    await cancelButton.click();

    // Modal should close
    await expect(modal).not.toBeVisible({ timeout: 5000 });

    // Verify the card title is unchanged (still the renamed title, not the cancelled change)
    const cardTitle = page.locator('.card.selected .card-title-text');
    await expect(cardTitle).toContainText(currentTitle, { timeout: 5000 });

    // Verify via API that the title was not changed
    const taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.title).toBe(currentTitle);

    // --- Now test empty name rejection ---
    // Re-open the edit modal
    const editButton2 = card.locator('button[aria-label="Edit task"]');
    await editButton2.click();

    const modal2 = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal2).toBeVisible({ timeout: 5000 });

    // Clear the title input completely
    const titleInput2 = page.locator('#edit-task-title-input');
    await titleInput2.clear();

    // Verify the save button is disabled when title is empty
    const saveButton = page.locator('.modal-footer .btn-primary');
    await expect(saveButton).toBeDisabled();

    // Try to save with empty title by pressing Enter in the input
    await titleInput2.press('Enter');

    // Modal should still be open (save was rejected)
    await expect(modal2).toBeVisible();

    // Verify the task title was not changed via API
    const taskData2 = await getTaskData(page, projectId, taskId);
    expect(taskData2.title).toBe(currentTitle);

    // Clean up: cancel the modal
    const cancelButton2 = page.locator('.modal-footer .btn-secondary');
    await cancelButton2.click();
    await expect(modal2).not.toBeVisible({ timeout: 5000 });
  });
});
