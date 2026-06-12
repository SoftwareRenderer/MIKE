import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, sendViaWebSocket } from './helpers';
import fs from 'node:fs';
import path from 'node:path';

const TEST_WORKTREES = '/tmp/kanban-test-worktrees';

test.describe.serial('Diff Stats Real-Time Polling', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should update diff stats on task card in real time via WebSocket polling', async ({ page }) => {
    const projectId = await getProjectId(page);
    const uniqueId = Date.now();

    // Create a task via API
    const taskId = await createTaskViaApi(page, projectId, `diff-stats-${uniqueId}`);

    // Trigger lazy-start via WebSocket (this creates the worktree and moves task to in_progress)
    await sendViaWebSocket(page, projectId, taskId, `Create a test file for diff stats ${uniqueId}`);

    // Wait for the executor to create the worktree
    const worktreePath = path.join(TEST_WORKTREES, taskId);
    await expect(async () => {
      expect(fs.existsSync(worktreePath)).toBe(true);
    }).toPass({ timeout: 15000 });

    // Refresh board to see the task in in_progress column
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    // Scope to the In Progress column and use the last card (our task)
    const inProgressColumn = page.locator('.column[aria-label="In Progress column"]');
    const taskCard = inProgressColumn.locator('.card').last();
    await expect(taskCard).toBeVisible({ timeout: 10000 });

    // Write a file to the worktree to create a diff
    const testFilePath = path.join(worktreePath, `test-file-${uniqueId}.txt`);
    fs.writeFileSync(testFilePath, 'line1\nline2\nline3\n');

    // Wait for the poller (2s interval) to detect the change and broadcast it
    await expect(async () => {
      const diffStatsEl = taskCard.locator('.diff-stats');
      const isVisible = await diffStatsEl.isVisible();
      expect(isVisible).toBe(true);
    }).toPass({ timeout: 10000 });

    // Verify the diff stats show the correct values
    const diffStatsText = await taskCard.locator('.diff-stats').textContent();
    expect(diffStatsText).toContain('1 file');
    expect(diffStatsText).toContain('+3');

    // Modify the file to add more lines
    fs.writeFileSync(testFilePath, 'line1\nline2\nline3\nline4\nline5\nline6\nline7\n');

    // Wait for poller to detect the updated stats
    await expect(async () => {
      const text = await taskCard.locator('.diff-stats').textContent();
      return text?.includes('+7');
    }).toPass({ timeout: 10000 });
  });

  test('should not poll diff stats for backlog tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const uniqueId = Date.now();

    // Create a task via API (defaults to backlog status, no worktree)
    await createTaskViaApi(page, projectId, `backlog-no-poll-${uniqueId}`);

    // Refresh board
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    // Scope to the Backlog column and use the last card (our newly created task)
    const backlogColumn = page.locator('.column[aria-label="Backlog column"]');
    const taskCard = backlogColumn.locator('.card').last();
    await expect(taskCard).toBeVisible({ timeout: 10000 });

    // No diff stats should appear for a backlog task (no worktree exists)
    // Wait a few poll cycles (2s each) and verify no stats appear
    await page.waitForTimeout(5000);

    const diffStatsEl = taskCard.locator('.diff-stats');
    const isVisible = await diffStatsEl.isVisible().catch(() => false);
    expect(isVisible).toBe(false);
  });
});
