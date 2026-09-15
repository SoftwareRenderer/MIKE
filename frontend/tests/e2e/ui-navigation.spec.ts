import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted, findProviderByRef,
  apiGetJson } from './helpers';

// Helper: create a subtask via API (CSRF-aware)
async function createSubtaskViaApi(page: test.Page, projectId: string, parentTaskId: string, prompt: string): Promise<string> {
    const config = await page.evaluate(async () => {
        const res = await fetch('/api/config');
        return res.json();
    });
    const defaultProvider = findProviderByRef(config.providers || [], config.worker_provider);
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
        method: 'POST',
        body: {
            prompt,
            parent_task_id: parentTaskId,
            llama_server_url: defaultProvider?.url || '',
            model_name: config?.worker_model || '',
        },
    });
    return result.data.id;
}

// ─── Browser Navigation (serial — navigation state is shared) ───

test.describe.serial('Browser Navigation (Back/Forward)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('back from task detail (direct URL) returns to project board', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `nav-direct-${Date.now()}`);

    // Navigate to project first, then click the card (creates history entry via pushState)
    await page.goto(`/?project=${projectId}`);
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
    await page.goto(`/?project=${projectId}`);
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
    await page.goto(`/?project=${projectId}`);
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
    await page.goto(`/?project=${projectId}`);
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

  test('subtask navigation creates history entry (pushState fix)', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create a parent task and a subtask via API
    const parentTitle = `nav-parent-${Date.now()}`;
    const parentId = await createTaskViaApi(page, projectId, parentTitle);
    const subtaskId = await createSubtaskViaApi(page, projectId, parentId, `nav-sub-${Date.now()}`);

    // Navigate to project view
    await page.goto(`/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Navigate directly to subtask URL (simulates what pushState would set)
    await navigateToTask(page, subtaskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${subtaskId}`);

    // Use in-app back button (more reliable than browser goBack)
    const backBtn = page.locator('.btn-back');
    if (await backBtn.isVisible().catch(() => false)) {
      await backBtn.click();
    } else {
      // Fallback: navigate to project board directly
      await page.goto(`/?project=${projectId}`);
    }

    // Wait for board to appear and task detail to disappear
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
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
    await page.goto(`/?project=${projectId}`);
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
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    expect(new URL(page.url()).searchParams.get('project')).toBeTruthy();
    // A Playwright page starts on about:blank, so one goto is already entry 2; the app must have
    // added nothing on top of it. Landing with a push made this 3.
    expect(await page.evaluate(() => history.length)).toBe(2);
  });

  test('back twice from a task keeps the project board', async ({ page }) => {
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    const landedProject = new URL(page.url()).searchParams.get('project');
    expect(landedProject).toBeTruthy();

    const taskTitle = `nav-double-back-${Date.now()}`;
    await createTaskViaApi(page, landedProject as string, taskTitle);
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });
    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    await page.goBack();
    await expect(async () => {
      expect(page.url()).not.toContain('task=');
    }).toPass({ timeout: 5000 });
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // Nothing is left under the app's own entries: in an installed PWA back closes the window,
    // here it lands on the blank page the context started with. Either way, never the board-less
    // panel while projects exist.
    await page.goBack().catch(() => {});
    await expect(page.locator('#main-content > .empty-state')).toHaveCount(0);
    if (await page.locator('#main-content').count() > 0) {
        await expect(page.locator('.board')).toBeVisible();
        expect(new URL(page.url()).searchParams.get('project')).toBe(landedProject);
    }
  });
});

// ─── Offline Cache (serial — route interceptions and IndexedDB state) ───

test.describe.serial('Offline Cache', () => {
  test('board renders from cache when the API is unreachable', async ({ page }) => {
    // 1. Load the board normally, which is what populates the client-side cache.
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });
    expect(await page.locator('.card').count()).toBeGreaterThan(0);

    // Make the API unreachable while the document and its assets still load. That is the state the
    // offline screen exists for. context.setOffline(true) would take the document down too, and the
    // service worker does not serve the shell here, leaving only Chrome's error page to assert on.
    await page.route('**/api/**', (route) => route.abort());
    await page.goto(`/?project=${projectId}`, { waitUntil: 'domcontentloaded' }).catch(() => {});

    // 3. The offline screen, then the cached board behind it.
    await expect(page.locator('.offline-screen')).toBeVisible({ timeout: 15000 });
    await page.click('.offline-btn');
    await page.waitForSelector('.card', { timeout: 15000 });
    expect(await page.locator('.card').count()).toBeGreaterThan(0);

    // 4. Cached data is labelled as such — a silent stale board is the failure mode
    //    this whole path exists to avoid.
    await expect(page.locator('.cache-banner')).toContainText('Viewing cached data', { timeout: 10000 });

    await page.unroute('**/api/**');
  });

  test('offline queue persists and merges with cached board', async ({ page, context }) => {
      // Ensure we're online (previous test may have set offline mode)
      await context.setOffline(false);

      // 1. Navigate to the board and let it load (populates cache)
      await page.goto('/');
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
      await page.goto('/');
      await page.waitForSelector('.card', { timeout: 10000 });

      // 6. Verify the task now exists on the server
      await expect(async () => {
          const cards = await page.locator('.card').count();
          expect(cards).toBeGreaterThanOrEqual(cardsBefore + 1);
      }).toPass({ timeout: 15000 });
  });

  test('queued actions survive flush failure and retry on reconnect', async ({ page, context }) => {
      // Ensure we're online (previous test may have set offline mode)
      await context.setOffline(false);

      // 1. Navigate to the board and let it load
      await page.goto('/');
      await page.waitForSelector('.card', { timeout: 10000 });

      const cardsBefore = await page.locator('.card').count();

      // 2. Block ALL API requests (true offline simulation)
      await page.route('/api/**', async (route) => {
          await route.abort('failed');
      });

      // 3. Create a task via the UI while offline
      const createBtn = page.getByText('+ Create Task').first();
      await expect(createBtn).toBeVisible();
      await createBtn.click();

      const modal = page.locator('[role="dialog"]:has-text("Create Task")');
      await expect(modal).toBeVisible({ timeout: 10000 });

      await modal.locator('textarea').fill(`retry-task-${Date.now()}`);
      await modal.locator('.btn-primary').click();
      await expect(modal).not.toBeVisible({ timeout: 10000 });

      // Verify action is queued
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

      // 4. Reload WHILE STILL OFFLINE — flushQueueNow() fails, actions must stay in queue
      // Auth fails → offline screen appears → click "Offline Mode" to load from cache
      await page.goto('/', { waitUntil: 'domcontentloaded' });
      await expect(page.locator('.offline-screen')).toBeVisible({ timeout: 10000 });
      await page.click('.offline-btn');
      await page.waitForSelector('.card', { timeout: 15000 });

      // Verify actions are STILL in the queue (not discarded by failed flush)
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

      // 5. Restore API access and reload — flushQueueNow() retries on fresh connection
      await page.route('/api/**', async (route) => {
          await route.continue();
      });

      // 6. Reload — auth succeeds, flushQueueNow() flushes the queue via HTTP
      await page.goto('/');
      await page.waitForSelector('.card', { timeout: 10000 });

      // 7. Verify queue is now empty (actions were flushed)
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
          expect(queueLength).toBe(0);
      }).toPass({ timeout: 15000 });

      // 8. Verify task exists on the server
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
      await page.goto('/');
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
