import { test, expect } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, apiGetJson, isolatedProject, gotoProject,
} from './helpers';
// Each test file gets its own project, because it starts a turn: see isolatedProject() in helpers.ts.
isolatedProject();

// ─── Browser Navigation (serial — navigation state is shared) ───

test.describe.serial('Browser Navigation (Back/Forward)', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('back from task detail (direct URL) returns to project board', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `nav-direct-${Date.now()}`);

    // Navigate to project first, then click the card (creates history entry via pushState)
    await gotoProject(page, `/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible();

    // Wait for card and click it
    await page.waitForSelector('.card');
    const cards = page.locator('.card');
    const count = await cards.count();
    if (count > 0) {
        await cards.last().click();
    }
    await expect(page.locator('.task-detail-page')).toBeVisible();
    expect(page.url()).toContain('task=');

    // Click browser back — should return to project board
    await page.goBack();
    // Condition-based wait: URL no longer contains task=
    await expect(async () => {
      expect(page.url()).not.toContain('task=');
    }).toPass({ timeout: 5000 });

    // Verify URL state
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');

    // Verify page CONTENTS: board is visible with columns
    await expect(page.locator('.board')).toBeVisible();
    await expect(page.locator('.column')).toHaveCount(4); // backlog, in_progress, review, done
    // Verify task detail is gone
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    // Verify top bar is visible (only shown in project view, not task detail)
    await expect(page.locator('.top-bar')).toBeVisible();
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

  test('back from task shows full project view with top-bar buttons', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Navigate to project view
    await gotoProject(page, `/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Verify top-bar and Token usage button are visible in project view
    await expect(page.locator('.top-bar')).toBeVisible();
    await expect(page.locator('.top-bar [aria-label="Token usage stats"]')).toBeVisible();

    // Create and open a task
    const taskTitle = `nav-topbar-${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, taskTitle);
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });
    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify top-bar is NOT visible in task detail
    await expect(page.locator('.top-bar')).not.toBeVisible();

    // Browser back
    await page.goBack();
    // Condition-based wait: URL no longer contains task=
    await expect(async () => {
      expect(page.url()).not.toContain('task=');
    }).toPass({ timeout: 5000 });

    // Verify FULL project view is restored: board, top-bar, AND Token usage button
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.top-bar [aria-label="Token usage stats"]')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');
  });

  test('clicking card in project view opens task and back works', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Navigate to project view first
    await gotoProject(page, `/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    const taskTitle = `nav-card-${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, taskTitle);

    // Wait for the card to appear
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });

    // Check history length
    const historyBefore = await page.evaluate(() => history.length);

    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Check history length after
    const historyAfter = await page.evaluate(() => history.length);
    expect(historyAfter).toBeGreaterThan(historyBefore);

    // Browser back should work
    await page.goBack();
    // Condition-based wait: URL no longer contains task=
    await expect(async () => {
      expect(page.url()).not.toContain('task=');
    }).toPass({ timeout: 5000 });

    // Verify page CONTENTS: board is visible with columns
    expect(page.url()).not.toContain('task=');
    expect(page.url()).toContain(`project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.column')).toHaveCount(4);
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    await expect(page.locator('.top-bar')).toBeVisible();
  });

  test('back from task preserves correct project in sidebar', async ({ page }) => {
    // Get project info
    const projectId = await getProjectId(page);
    // The sidebar renders `sortedProjects`, so ".project-name first" is whichever row
    // sorts first — not the project this test navigates to. Read the name of the project
    // under test from the API instead of guessing it from the DOM order.
    const projectName = (await apiGetJson<any[]>(page, '/api/projects'))
      .find((p: any) => p.id === projectId)?.name;
    expect(projectName).toBeTruthy();

    // Navigate to project view
    await gotoProject(page, `/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Verify sidebar shows this project as active
    const activeProjectBefore = await page.locator('.project-item-wrapper.active .project-name').textContent();
    expect(activeProjectBefore?.trim()).toBe(projectName);

    // Create and open a task
    const taskTitle = `nav-sidebar-${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, taskTitle);
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });
    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Browser back
    await page.goBack();
    // Condition-based wait: URL no longer contains task=
    await expect(async () => {
      expect(page.url()).not.toContain('task=');
    }).toPass({ timeout: 5000 });
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // Verify sidebar STILL shows the SAME project as active
    const activeProjectAfter = await page.locator('.project-item-wrapper.active .project-name').textContent();
    expect(activeProjectAfter?.trim()).toBe(projectName);
    expect(page.url()).toContain(`project=${projectId}`);
  });
  test('switch project via sidebar, open task, back preserves new project', async ({ page}) => {
    const projectId = await getProjectId(page);
    // The name of the project under test, from the API: the sidebar renders
    // `sortedProjects`, so ".project-name first" names whichever row sorts first.
    const projectName = (await apiGetJson<any[]>(page, '/api/projects'))
      .find((p: any) => p.id === projectId)?.name;
    expect(projectName).toBeTruthy();

    // Create a unique task in the project
    const taskTitle = `nav-switch-${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, taskTitle);

    // Navigate to project view
    await gotoProject(page, `/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Wait for our specific card and click it
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });
    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify URL has the correct project and task
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).toContain(`task=${taskId}`);

    // Browser back
    await page.goBack();
    // Condition-based wait: URL no longer contains task=
    await expect(async () => {
      expect(page.url()).not.toContain('task=');
    }).toPass({ timeout: 5000 });
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // Verify sidebar shows the SAME project (not switched)
    const activeProjectAfter = await page.locator('.project-item-wrapper.active .project-name').textContent();
    expect(activeProjectAfter?.trim()).toBe(projectName);
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');
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

// ─── Offline Cache (serial — route interceptions and IndexedDB state) ───

test.describe.serial('Offline Cache', () => {
  test('offline queue persists and merges with cached board', async ({ page, context }) => {
      // Ensure we're online (previous test may have set offline mode)
      await context.setOffline(false);

      // 1. Navigate to the board and let it load (populates cache)
      await gotoProject(page);
      await page.waitForSelector('.card', { timeout: 10000 });

      const cardsBefore = await page.locator('.card').count();

      // 2. Block ALL API requests (true offline simulation)
      await page.route('/api/**', async (route) => {
          await route.abort('failed');
      });

      // 3. Create a task via the UI while offline
      // Click the "+ Create Task" button
      const createBtn = page.getByText('+ Create Task').first();
      await expect(createBtn).toBeVisible();
      await createBtn.click();

      // Wait for the modal to appear
      const modal = page.locator('[role="dialog"]:has-text("Create Task")');
      await expect(modal).toBeVisible({ timeout: 10000 });

      // Fill in the task prompt
      await modal.locator('textarea').fill(`offline-task-${Date.now()}`);
      await modal.locator('.btn-primary').click();
      await expect(modal).not.toBeVisible({ timeout: 10000 });

      // Wait for the queue to be saved to IndexedDB (fire-and-forget catch handler)
      await expect(async () => {
          const queueLength = await page.evaluate(() => {
              return new Promise<number>((resolve) => {
                  const db = indexedDB.open('kanban-storage');
                  db.onsuccess = () => {
                      const s = db.result.transaction('offline-queue', 'readonly').objectStore('offline-queue');
                      const req = s.get('actions');
                      req.onsuccess = () => resolve(req.result?.length || 0);
                      req.onerror = () => resolve(0);
                  };
                  db.onerror = () => resolve(0);
              });
          });
          expect(queueLength).toBeGreaterThan(0);
      }).toPass({ timeout: 10000 });

      // 4. Restore API and reload — server should serve fresh data
      // The queue will be flushed, and the task will be created on the server
      await page.route('/api/**', async (route) => {
          await route.continue();
      });

      // 5. Reload — queue flushes, task is created on server
      await gotoProject(page);
      await page.waitForSelector('.card', { timeout: 10000 });

      // 6. Verify the task now exists on the server
      await expect(async () => {
          const cards = await page.locator('.card').count();
          expect(cards).toBeGreaterThanOrEqual(cardsBefore + 1);
      }).toPass({ timeout: 15000 });
  });
});

// ─── Terminal (parallel — independent test) ───

test.describe.parallel('Terminal', () => {
  test('terminal panel opens when clicking terminal button in task header', async ({ page }) => {
      // 1. Navigate to the board and let it load
      await gotoProject(page);
      await page.waitForSelector('.card', { timeout: 10000 });

      // 2. Create a task and start it (needs a branch for terminal button to be enabled)
      const projectId = await getProjectId(page);
      const taskId = await createTaskViaApi(page, projectId, `terminal-test-${Date.now()}`);
      const started = await ensureTaskStarted(page, projectId, taskId);
      test.skip(!started, 'Could not start task');

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
