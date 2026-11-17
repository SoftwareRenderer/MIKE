import { test, expect } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, apiGetJson, isolatedProject, gotoProject,
  clearTasksAfterEach,
} from './helpers';
// Each test file gets its own project, because it starts a turn: see isolatedProject() in helpers.ts.
isolatedProject();
// Every test here creates a task, and a task that still exists holds its container against the
// executor's admission gate: released per test, not at the end of the file.
clearTasksAfterEach();

// One decision per test, and one test per decision. What a card click and a browser back press owe the
// reader is the same claim read four ways — the URL, the board, the project view's chrome and the sidebar's
// active row — so it is read in one place; the in-app Back button is a different control with its own
// handler, and the two history-entry counts below measure entries the app *did not* add.
//
// A queued write that came back with the connection is `offline-send-queue.spec.ts`'s, and the queue's own
// branches (a create of another project, a queued row on a mirror board, a replay the server refused) are
// `src/lib/api/__tests__/write-or-queue.unit.ts`, `queue-mirror-landing.unit.ts` and
// `queue-refused.unit.ts`.

// ─── Browser Navigation (serial — navigation state is shared) ───

test.describe.serial('Browser Navigation (Back/Forward)', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('back from a card returns to the board of the project it came from', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The sidebar renders `sortedProjects`, so ".project-name first" is whichever row sorts first — not the
    // project this test navigates to. Read its name from the API instead of guessing it from the DOM order.
    const projectName = (await apiGetJson<any[]>(page, '/api/projects'))
      .find((p: any) => p.id === projectId)?.name;
    expect(projectName).toBeTruthy();

    const taskTitle = `nav-back-${Date.now()}`;
    await createTaskViaApi(page, projectId, taskTitle);

    // The board first, so the back press has a board to return to, then the card opens through its own link.
    await gotoProject(page, `/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.top-bar')).toBeVisible();
    await expect(page.locator('.top-bar [aria-label="Token usage stats"]')).toBeVisible();
    const activeBefore = await page.locator('.project-item-wrapper.active .project-name').textContent();
    expect(activeBefore?.trim()).toBe(projectName);

    const historyBefore = await page.evaluate(() => history.length);
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });
    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).toContain('task=');
    // Opening a task is one entry on top of the board the reader came from.
    expect(await page.evaluate(() => history.length)).toBeGreaterThan(historyBefore);
    // The top bar is the project view's chrome: a task detail has no room for it.
    await expect(page.locator('.top-bar')).not.toBeVisible();

    await page.goBack();
    // Condition-based wait: URL no longer contains task=
    await expect(async () => {
      expect(page.url()).not.toContain('task=');
    }).toPass({ timeout: 5000 });

    expect(page.url()).toContain(`project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.column')).toHaveCount(4); // backlog, in_progress, review, done
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    // The FULL project view comes back: the bar over the board and the control at its end.
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.top-bar [aria-label="Token usage stats"]')).toBeVisible({ timeout: 5000 });
    // And the sidebar still holds the project the reader was in.
    const activeAfter = await page.locator('.project-item-wrapper.active .project-name').textContent();
    expect(activeAfter?.trim()).toBe(projectName);
  });

  test('close task detail via in-app back button returns to project board', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `nav-btn-${Date.now()}`);

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Click the in-app back button
    await page.click('.btn-back');

    // Verify page CONTENTS: board is visible with columns
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.column')).toHaveCount(4);
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    await expect(page.locator('.top-bar')).toBeVisible();
    expect(page.url()).not.toContain('task=');
    expect(page.url()).toContain(`project=${projectId}`);
  });

  // The installed PWA launches at the bare root (`start_url: '/'` in the manifest), so the launch
  // entry is the one a back press leaves the app on: landing on a project must rewrite that entry,
  // never add one, or the entry underneath is a board-less URL.
  test('launch at the bare root rewrites its own history entry', async ({ page }) => {
    // A fresh tab: this test measures the app's FIRST navigation, and the file's beforeEach has already put
    // one on this page's history. A tab that landed on `?project=` is not the launch the PWA performs.
    const launch = await page.context().newPage();
    await launch.goto('/');
    await expect(launch.locator('.board')).toBeVisible({ timeout: 10000 });
    expect(new URL(launch.url()).searchParams.get('project')).toBeTruthy();
    // A Playwright page starts on about:blank, so one goto is already entry 2; the app must have
    // added nothing on top of it. Landing with a push made this 3.
    expect(await launch.evaluate(() => history.length)).toBe(2);
    await launch.close();
  });

  test('back twice from a task keeps the project board', async ({ page, context }) => {
    // A fresh tab, because this test counts the app's own entries and the harness made one before it: the
    // first API call a page makes goes through `getOrigin`, which launches this context at `/` and lets the
    // app land it on whatever project a browser with no last-project preference lands on. Back reaching that
    // launch is the browser working, so the measurement starts at the tab's own first navigation.
    const projectId = await getProjectId(page);
    const tab = await context.newPage();
    await tab.goto(`/?project=${projectId}`);
    await expect(tab.locator('.board')).toBeVisible({ timeout: 10000 });
    // [about:blank, this board] — a landing that pushed instead of replacing would answer 3.
    expect(await tab.evaluate(() => history.length)).toBe(2);

    const taskTitle = `nav-double-back-${Date.now()}`;
    await createTaskViaApi(page, projectId, taskTitle);
    await tab.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });
    await tab.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(tab.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    // The task added one entry and nothing else added any.
    expect(await tab.evaluate(() => history.length)).toBe(3);

    await tab.goBack();
    await expect(async () => {
      expect(tab.url()).not.toContain('task=');
    }).toPass({ timeout: 5000 });
    await expect(tab.locator('.board')).toBeVisible({ timeout: 5000 });
    expect(new URL(tab.url()).searchParams.get('project')).toBe(projectId);

    // Nothing is left under the app's own entries: in an installed PWA back closes the window,
    // here it lands on the blank page the tab started with. Either way, never the board-less
    // panel while projects exist.
    await tab.goBack().catch(() => {});
    await expect(tab.locator('#main-content > .empty-state')).toHaveCount(0);
    if (await tab.locator('#main-content').count() > 0) {
        await expect(tab.locator('.board')).toBeVisible();
        expect(new URL(tab.url()).searchParams.get('project')).toBeTruthy();
    }
    await tab.close();
  });
});

// ─── Terminal ───

test.describe.parallel('Terminal', () => {
  test('terminal panel opens when clicking terminal button in task header', async ({ page }) => {
      // 1. Navigate to the board and let it load
      await gotoProject(page);
      await page.waitForSelector('.card', { timeout: 10000 });

      // 2. Create a task and start it (needs a branch for terminal button to be enabled)
      const projectId = await getProjectId(page);
      const taskId = await createTaskViaApi(page, projectId, `terminal-test-${Date.now()}`);
      // A task that will not start is a product regression, not an environment prerequisite: the terminal
      // button is enabled by the branch a started task has, so this is the test's own precondition.
      expect(await ensureTaskStarted(page, projectId, taskId),
        'the task never reached In Progress, so it has no branch and no terminal button').toBe(true);

      // 3. Navigate to the task detail page
      await navigateToTask(page, taskId, projectId);

      // 4. Wait for the task detail page to load
      await expect(page.locator('.task-detail-page')).toBeVisible();

      // 5. Find the terminal button in the task header (aria-label is stable;
      //    the title now includes the keyboard shortcut)
      const terminalButton = page.locator('[aria-label="Open terminal"]');
      await expect(terminalButton).toBeVisible();

      // 6. Verify terminal is not visible before clicking
      await expect(page.locator('.terminal-panel')).not.toBeVisible();

      // 7. Click the terminal button
      await terminalButton.click();

      // 8. Verify the terminal panel appears (lazy-loaded chunk + showTerminal flag)
      await expect(page.locator('.terminal-panel')).toBeVisible({ timeout: 10000 });
  });
});
