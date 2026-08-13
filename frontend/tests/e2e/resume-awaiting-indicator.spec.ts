import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch, navigateToTask } from './helpers';

// ============================================================
// Awaiting LLM indicator after Resume
//
// Regression for stream-handler.ts: `status: running` now arms
// awaitingResponse, so .awaiting-llm shows after ANY resume —
// including paths that never call handleStartTask (card Resume,
// API resume, offline-queue flush, LLM crash retry).
//
// Determinism: "slow-start" prompts make the mock LLM delay the
// first token by 3s, giving a stable window for the indicator.
// ============================================================

test.describe.serial('Resume shows Awaiting LLM indicator', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('API resume on an unarmed open chat shows the awaiting indicator', async ({ page }) => {
    test.setTimeout(60000);
    const projectId = await getProjectId(page);

    // Backlog task → chat mounts UNARMED (onMount only arms for in_progress).
    const taskId = await createTaskViaApi(page, projectId, `slow-start resume-api-${Date.now()}`);
    await navigateToTask(page, taskId);

    // Wait for chat history + WS subscription (prompt message shown).
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Resume exactly like the card Resume button / offline-queue flush —
    // only the backend `status: running` event can surface the indicator.
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/resume`, {
      method: 'POST',
    });
    expect(result.ok).toBe(true);

    const indicator = page.locator('.awaiting-llm');
    await expect(indicator).toBeVisible({ timeout: 10000 });
    await expect(indicator).toContainText('Awaiting');

    // Hides once the mock response streams its first token.
    await expect(indicator).toBeHidden({ timeout: 30000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
  });

  test('ReviewBar Resume shows the awaiting indicator (regression)', async ({ page }) => {
    test.setTimeout(60000);
    const projectId = await getProjectId(page);

    const taskId = await createTaskViaApi(page, projectId, `slow-start resume-review-${Date.now()}`);
    // Review → ReviewBar "Resume" appears (last entry is the user prompt).
    const moved = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    expect(moved.ok).toBe(true);

    await navigateToTask(page, taskId);

    const resumeBtn = page.locator('.btn-commit-done', { hasText: 'Resume' });
    await expect(resumeBtn).toBeVisible({ timeout: 10000 });
    await resumeBtn.click();

    const indicator = page.locator('.awaiting-llm');
    await expect(indicator).toBeVisible({ timeout: 10000 });
    await expect(indicator).toContainText('Awaiting');
    await expect(indicator).toBeHidden({ timeout: 30000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
  });
});
