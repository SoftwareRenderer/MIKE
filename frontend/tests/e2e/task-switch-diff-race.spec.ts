import { test, expect } from '@playwright/test';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import {
  isolatedProject, ownProjectId, createTaskViaApi, ensureTaskStarted,
  getWorktreePath, worktreeExists, gotoProject, serverApi,
} from './helpers';

// A diff belongs to the task that asked for it. The panel is the only place a user reads "what this task
// changed", so a response that arrives after they switched must not paint the task they left into the panel
// that now belongs to another task — that is not a stale frame, it is a wrong answer about the wrong task.
//
// Thread switches inside one task are already held by diff-viewer.spec.ts; this is the task-to-task switch,
// with the first task's response deliberately held open past it. Client-side routing (card clicks), because
// a page load cannot produce this race — it drops the in-flight request instead of landing it late.

isolatedProject();

async function waitForWorktree(projectId: string, taskId: string): Promise<string> {
  await expect(async () => {
    expect(worktreeExists(projectId, taskId), 'worktree not created').toBe(true);
  }).toPass({ timeout: 45_000 });
  return getWorktreePath(projectId, taskId);
}

test.describe('Task switch with a diff in flight', () => {
  test('the task you left never paints the task you are on', async ({ page, baseURL }) => {
    test.setTimeout(150_000);
    const base = baseURL || 'http://localhost:3000';
    const projectId = ownProjectId();
    const stamp = Date.now();

    const taskA = await createTaskViaApi(page, projectId, `race-left-${stamp}`);
    const taskB = await createTaskViaApi(page, projectId, `race-current-${stamp}`);
    await ensureTaskStarted(page, projectId, taskA);
    await ensureTaskStarted(page, projectId, taskB);

    // One untracked file per worktree: the diff panel lists untracked files, so each task's panel has
    // exactly one name that the other task's panel must never contain.
    const fileA = `race-left-${stamp}.txt`;
    const fileB = `race-current-${stamp}.txt`;
    writeFileSync(join(await waitForWorktree(projectId, taskA), fileA), 'belongs to the task you left\n');
    writeFileSync(join(await waitForWorktree(projectId, taskB), fileB), 'belongs to the task you are on\n');

    // Hold the first task's diff response open long enough that it lands after the switch.
    let held = 0;
    await page.route(`**/api/projects/${projectId}/tasks/${taskA}/diff**`, async (route) => {
      held++;
      await new Promise((r) => setTimeout(r, 2500));
      await route.continue();
    });

    // The held response lands 2.5s after it starts; this promise is the moment it actually arrives.
    const heldLanded = page.waitForResponse(
      (r) => r.url().includes(`/api/projects/${projectId}/tasks/${taskA}/diff`), { timeout: 30_000 });

    await gotoProject(page);
    await page.locator('.card', { hasText: `race-left-${stamp}` }).first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15_000 });

    // Switch while that request is still in flight.
    await page.goBack();
    await expect(page.locator('.board')).toBeVisible({ timeout: 15_000 });
    await page.locator('.card', { hasText: `race-current-${stamp}` }).first().click();

    await expect(page.locator('.diff-content')).toContainText(fileB, { timeout: 40_000 });

    // Now the held response has landed, whatever the client did with it (drop it on unmount, or answer
    // it and discard it — both are correct; painting it is not).
    await heldLanded;
    const shown = (await page.locator('.diff-content').allTextContents()).join('\n');
    expect(shown, `the panel shows the diff of the task left behind (${fileA})`).not.toContain(fileA);
    expect(shown).toContain(fileB);

    // And the panel agrees with the server about the task it is showing.
    const serverDiff = await serverApi(base, `/api/projects/${projectId}/tasks/${taskB}/diff`);
    const serverBody = JSON.stringify(serverDiff.data);
    expect(serverBody).toContain(fileB);
    expect(serverBody).not.toContain(fileA);

    // The hold must have run, or the spec tested a race that never happened.
    expect(held, 'the diff request was never intercepted — the switch happened after it completed').toBeGreaterThan(0);
  });
});
