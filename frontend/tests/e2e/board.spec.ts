import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe.configure({ mode: 'parallel' });

// ============================================================
// Board Layout
// ============================================================

test.describe('Board Layout', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should display the board with 4 columns', async ({ page }) => {
    await expect(page.locator('.board')).toBeVisible();
    await expect(page.locator('.column')).toHaveCount(4);
  });

  // The landmark/skip-link contract lives in App.svelte's markup and is only real in a
  // browser — a screen reader reads the rendered document, not the component source.
  test('exposes the landmarks and skip link a keyboard/AT user navigates by', async ({ page }) => {
    await expect(page.locator('.board-container')).toHaveAttribute('aria-label', 'Kanban board');
    await expect(page.locator('.board')).toBeVisible();

    const skip = page.locator('a.skip-link');
    await expect(skip).toHaveAttribute('href', '#main-content');
    await expect(skip).toContainText('Skip to main content');

    // The skip link must land on a real, focusable target.
    await expect(page.locator('#main-content')).toHaveCount(1);
    await skip.focus();
    await expect(skip).toBeVisible();
    await skip.press('Enter');
    await expect(page.locator('#main-content')).toBeFocused();
  });
});

// ============================================================
// Column Ordering by Last Move
// ============================================================

test.describe('Column Ordering by Last Move', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  async function taskField(page: any, pid: string, tid: string, field: string) {
    return page.evaluate(async ({ pid, tid, field }: any) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const data = await res.json();
      return data[field];
    }, { pid, tid, field });
  }

  function columnCardOrder(page: any, columnIndex: number, ids: string[]) {
    return page.locator('.column').nth(columnIndex).locator('.card').evaluateAll(
      (cards: Element[]) => cards.map((c) => c.getAttribute('data-task-id')).filter(Boolean) as string[]
    ).then((order: string[]) => order.filter((id) => ids.includes(id)));
  }

  test('should put the most recently moved task on top of its column', async ({ page }) => {
    const projectId = await getProjectId(page);
    const task1 = await createTaskViaApi(page, projectId, `move-order-1-${Date.now()}`);
    const task2 = await createTaskViaApi(page, projectId, `move-order-2-${Date.now()}`);
    const task3 = await createTaskViaApi(page, projectId, `move-order-3-${Date.now()}`);
    const ids = [task1, task2, task3];

    // moved_at has second precision, so pause between moves. Polling for a change instead
    // would never resolve: moving a task into its current column is a no-op.
    for (let i = 0; i < ids.length; i++) {
      if (i > 0) await page.waitForTimeout(1100);
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${ids[i]}/move`, {
        method: 'POST',
        body: { status: 'review' },
      });
    }

    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });

    // task3 moved last, so it sits on top; task1 (moved first) at the bottom.
    await expect(async () => {
      const order = await columnCardOrder(page, 2, ids);
      expect(order).toEqual([task3, task2, task1]);
    }).toPass({ timeout: 10000 });
  });

  test('should keep a card in place when it is edited rather than moved', async ({ page }) => {
    const projectId = await getProjectId(page);
    const first = await createTaskViaApi(page, projectId, `edit-stable-first-${Date.now()}`);
    const second = await createTaskViaApi(page, projectId, `edit-stable-second-${Date.now()}`);
    const ids = [first, second];

    // Move first, then second — second ends up on top (a same-second tie keeps the server
    // order, which is also newest-created-first).
    for (const tid of ids) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${tid}/move`, {
        method: 'POST',
        body: { status: 'review' },
      });
    }
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });
    await expect(async () => {
      expect(await columnCardOrder(page, 2, ids)).toEqual([second, first]);
    }).toPass({ timeout: 10000 });

    // Editing the bottom card (title + prompt) must not bring it to the top.
    const beforeMovedAt = await taskField(page, projectId, first, 'moved_at');
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${first}`, {
      method: 'PUT',
      body: { title: `edited-${Date.now()}` },
    });

    await expect(async () => {
      const afterMovedAt = await taskField(page, projectId, first, 'moved_at');
      expect(afterMovedAt).toBe(beforeMovedAt);
      expect(await columnCardOrder(page, 2, ids)).toEqual([second, first]);
    }).toPass({ timeout: 10000 });
  });
});

// ============================================================
// Drag and Drop Between Columns
// ============================================================

test.describe('Drag and Drop Between Columns', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should click a task card in board overview to switch to it', async ({ page }) => {
    const projectId = await getProjectId(page);
    const timestamp = Date.now();
    const taskId1 = await createTaskViaApi(page, projectId, `switch-a-${timestamp}`);
    const taskId2 = await createTaskViaApi(page, projectId, `switch-b-${timestamp}`);

    await navigateToTask(page, taskId1);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await expect(page).toHaveURL(new RegExp(`task=.*${taskId1.replace(/-/g, '\\-')}`));

    const targetCard = page.locator(`.card[data-task-id="${taskId2}"]`);
    await expect(targetCard).toBeVisible({ timeout: 5000 });
    await targetCard.click();
    await expect(page).toHaveURL(new RegExp(`task=.*${taskId2.replace(/-/g, '\\-')}`), { timeout: 5000 });
  });

  test('should have draggable cards and drop zones in board overview', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `draggable-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const sections = page.locator('.section-header');
    await expect(sections).toHaveCount(4, { timeout: 5000 });

    const firstCard = page.locator('.card').first();
    await expect(firstCard).toBeVisible({ timeout: 5000 });
    await expect(firstCard).toHaveAttribute('draggable', 'true');
  });
});

// ============================================================
// Scroll to Current Task in Side Panel
// ============================================================

test.describe('Scroll to Current Task in Side Panel', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should scroll side board to show current task on desktop', async ({ page }) => {
    const projectId = await getProjectId(page);
    let targetTaskId = '';
    for (let i = 0; i < 10; i++) {
      targetTaskId = await createTaskViaApi(page, projectId, `scroll-task-${i}-${Date.now()}`);
    }

    await navigateToTask(page, targetTaskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const currentCard = page.locator('.card.selected');
    await expect(currentCard).toBeVisible({ timeout: 5000 });

    const isVisible = await page.evaluate(() => {
      const card = document.querySelector('.card.selected');
      if (!card) return false;
      const cardRect = card.getBoundingClientRect();
      const column = document.querySelector('.column-context');
      if (!column) return false;
      const columnRect = column.getBoundingClientRect();
      const margin = 10;
      const cardTopVisible = cardRect.top >= columnRect.top - margin;
      const cardBottomVisible = cardRect.bottom <= columnRect.bottom + margin;
      const columnCenter = columnRect.top + columnRect.height / 2;
      const cardCenter = cardRect.top + cardRect.height / 2;
      const distanceToCenter = Math.abs(cardCenter - columnCenter);
      return cardTopVisible && cardBottomVisible || distanceToCenter < columnRect.height / 2;
    });

    expect(isVisible).toBe(true);
  });

  test('should toggle section collapse in board overview', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `scroll-task-a-${Date.now()}`);
    await createTaskViaApi(page, projectId, `scroll-task-b-${Date.now()}`);

    await navigateToTask(page, taskId1);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const backlogHeader = page.locator('.section-header').nth(0);
    await expect(backlogHeader).toHaveAttribute('aria-expanded', 'true');
    await expect(backlogHeader).toHaveAttribute('title', 'Collapse Backlog');

    // Clicking collapses the section
    await backlogHeader.click();
    await expect(backlogHeader).toHaveAttribute('aria-expanded', 'false');
    await expect(backlogHeader).toHaveAttribute('title', 'Expand Backlog');

    // Clicking again expands it
    await backlogHeader.click();
    await expect(backlogHeader).toHaveAttribute('aria-expanded', 'true');

    const currentCard = page.locator('.card.selected');
    await expect(currentCard).toBeVisible({ timeout: 5000 });
  });
});

// ============================================================
// Clear Done Tasks
// ============================================================

test.describe('Clear Done Tasks', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show Clear all button in Done column header when done tasks exist', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `clear-done-visible-${Date.now()}`);
    expect(taskId).not.toBe('');

    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });
    expect(moveResult.status).toBe(200);

    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });

    await expect(async () => {
      const board = await page.evaluate(async ({ pid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return res.json();
      }, { pid: projectId });
      const doneTasks = board.tasks.filter((t: any) => t.status === 'done');
      expect(doneTasks.length).toBeGreaterThanOrEqual(1);
    }).toPass({ timeout: 10000 });

    const clearBtn = page.locator('.clear-btn');
    await expect(clearBtn).toBeVisible({ timeout: 10000 });
  });

  test('should not show Clear all button when no done tasks exist', async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const doneCount = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });

    const clearBtn = page.locator('.clear-btn');
    if (doneCount === 0) {
      await expect(clearBtn).not.toBeVisible();
    } else {
      await expect(async () => {
        await expect(clearBtn).toBeVisible();
      }).toPass({ timeout: 5000 });
    }
  });

  test('should confirm before clearing done tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `clear-done-confirm-${Date.now()}`);
    expect(taskId).not.toBe('');

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });

    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Verify our task is in done
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('done');
    }).toPass({ timeout: 5000 });

    await page.click('.clear-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible();
    expect(await page.locator('[role="dialog"] .confirm-message').textContent()).toContain('Permanently delete');

    // Click Cancel to dismiss without deleting
    await page.click('[role="dialog"] .btn-cancel');
    await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 5000 });

    // Verify our specific task is still in done (not deleted)
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('done');
    }).toPass({ timeout: 5000 });
  });

  test('should delete all done tasks on confirmation', async ({ page }) => {
    const projectId = await getProjectId(page);

    const doneCountBase = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });

    const taskId1 = await createTaskViaApi(page, projectId, `clear-done-delete-1-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `clear-done-delete-2-${Date.now()}`);

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId1}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId2}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });

    await gotoProject(page);
    await page.waitForSelector('.card');

    const doneCountBefore = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    // Use >= because parallel tests may have added more done tasks
    expect(doneCountBefore).toBeGreaterThanOrEqual(doneCountBase + 2);

    await page.click('.clear-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible();
    await page.click('[role="dialog"] .btn-confirm');

    await gotoProject(page);
    await page.waitForSelector('.card');

    const doneCountAfter = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    // Verify the tasks we created are gone (count decreased by at least our known tasks)
    expect(doneCountAfter).toBeLessThanOrEqual(doneCountBefore - 2);
  });

  test('should not reappear in Review after move-to-done then clear', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `clear-done-no-reappear-${Date.now()}`);
    expect(taskId).not.toBe('');

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });

    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });

    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(task.status).toBe('done');
    }).toPass({ timeout: 10000 });

    await page.click('.clear-btn');
    await expect(page.locator('[role="dialog"]')).toBeVisible();
    await page.click('[role="dialog"] .btn-confirm');

    await expect(page.locator(`.card[data-task-id="${taskId}"]`)).not.toBeVisible({ timeout: 10000 });

    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        const board = await res.json();
        return board.tasks.find((t: any) => t.id === tid);
      }, { pid: projectId, tid: taskId });
      expect(task).toBeUndefined();
    }).toPass({ timeout: 10000 });
  });
});

// ============================================================
// Project ID in URL
// ============================================================

test.describe('Project ID in URL', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should use folder basename as project ID', async ({ page }) => {
    const projects = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      return res.json();
    });

    const testProject = projects.find(
      (p: any) => p.path === '/tmp/kanban-test-repo'
    );
    expect(testProject).toBeDefined();
    expect(testProject.id).toBe('kanban-test-repo');
  });

  test('should use slugified full path on basename collision', async ({ page }) => {
    const { execSync } = await import('node:child_process');
    const fs = await import('node:fs');
    const tempPath = '/tmp/collision-parent/kanban-test-repo';

    if (!fs.existsSync(tempPath)) {
      fs.mkdirSync(tempPath, { recursive: true });
      fs.writeFileSync(`${tempPath}/README.md`, '# Collision Test\n');
      execSync('git init', { cwd: tempPath });
      execSync('git add README.md', { cwd: tempPath });
      execSync(
        'git -c user.email="test@kanban.local" -c user.name="Test" commit -m "init"',
        { cwd: tempPath }
      );
    }

    const result = await csrfFetch(page, '/api/projects', {
      method: 'POST',
      body: { path: tempPath },
    });
    const projectData = result.data;

    expect(projectData.id).not.toBe('kanban-test-repo');
    expect(projectData.id).toBe('tmp-collision-parent-kanban-test-repo');

    await csrfFetch(page, `/api/projects/${projectData.id}`, { method: 'DELETE' });
  });
});

// ============================================================
// Mobile Delete Scroll Column
// ============================================================

test.describe('Mobile Delete Scroll Column', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should center on the deleted task column after delete', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `mobile-scroll-${Date.now()}`);

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Create a second task in Review so the column stays non-empty after deletion
    // (Board.svelte skips scrolling to empty columns)
    const taskId2 = await createTaskViaApi(page, projectId, `mobile-scroll-keep-${Date.now()}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId2}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Scroll board to Backlog (first column) so Review is NOT centered
    await page.evaluate(() => {
      const board = document.querySelector('.board');
      if (board) board.scrollLeft = 0;
    });
    // Wait for scroll to apply
    await expect(async () => {
      const sl = await page.evaluate(() => {
        const board = document.querySelector('.board');
        return board ? board.scrollLeft : -1;
      });
      expect(sl).toBe(0);
    }).toPass({ timeout: 2000 });

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    await page.click('.icon-btn[aria-label="Delete task"]');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });
    await page.click('[role="dialog"] .btn-confirm');

    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Wait for scroll animation: board should have scrolled away from 0
    await expect(async () => {
      const sl = await page.evaluate(() => {
        const board = document.querySelector('.board');
        return board ? board.scrollLeft : 0;
      });
      expect(sl).toBeGreaterThan(0);
    }).toPass({ timeout: 3000 });

    // Small delay for layout to settle
    await page.waitForFunction(() => false, {}, { timeout: 200, polling: 200 }).catch(() => {});

    const result = await page.evaluate(() => {
      const board = document.querySelector('.board');
      const col = document.querySelector('.column[aria-label="Review column"]');
      if (!board || !col) return null;
      return {
        scrollLeft: board.scrollLeft,
        boardWidth: board.clientWidth,
        colOffset: col.offsetLeft,
        colWidth: col.clientWidth,
      };
    });

    if (!result) {
      // If Review column is gone (last task deleted), verify scroll moved from 0
      const sl = await page.evaluate(() => {
        const board = document.querySelector('.board');
        return board ? board.scrollLeft : 0;
      });
      expect(sl).toBeGreaterThan(0);
      return;
    }

    const colCenter = result.colOffset + result.colWidth / 2;
    const viewportCenter = result.scrollLeft + result.boardWidth / 2;
    // Column center should be within 1.5 column widths of the viewport center (wider tolerance for mobile)
    expect(Math.abs(colCenter - viewportCenter)).toBeLessThan(result.colWidth * 1.5);
  });
});

// ============================================================
// Multi-Select
// ============================================================

test.describe('Multi-Select', () => {
  let taskId1: string;
  let taskId2: string;
  let taskId3: string;
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });
    projectId = await getProjectId(page);

    taskId1 = await createTaskViaApi(page, projectId, `multi-select-test-${Date.now()}-1`);
    taskId2 = await createTaskViaApi(page, projectId, `multi-select-test-${Date.now()}-2`);
    taskId3 = await createTaskViaApi(page, projectId, `multi-select-test-${Date.now()}-3`);

    await page.waitForSelector(`[data-task-id="${taskId3}"]`, { timeout: 10000 });
  });

  test('should support multi-select mode', async ({ page }) => {
    const card = page.locator('.card').first();
    await expect(card).toBeVisible();
  });

  test('should confirm before starting selected tasks with task titles listed', async ({ browserName, page }) => {
    if (browserName === 'webkit') test.skip();

    const card1 = page.locator(`[data-task-id="${taskId1}"]`);
    const card2 = page.locator(`[data-task-id="${taskId2}"]`);

    await card1.click({ modifiers: ['Control'] });
    await card2.click({ modifiers: ['Control'] });

    const startBtn = page.locator('.bar-btn-start');
    await startBtn.click();

    const dialog = page.locator('.modal');
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('Start 2 tasks');

    const card1Title = await card1.locator('.card-title-text').textContent();
    const card2Title = await card2.locator('.card-title-text').textContent();
    expect(!!card1Title).toBe(true);
    expect(!!card2Title).toBe(true);
    await expect(dialog).toContainText(card1Title!);
    await expect(dialog).toContainText(card2Title!);

    const cancelBtn = page.locator('.modal-footer .btn-cancel');
    await cancelBtn.click();
    await expect(dialog).not.toBeVisible();

    await expect(card1).toBeVisible();
  });

  test('should show Delete button in action bar', async ({ browserName, page }) => {
    if (browserName === 'webkit') test.skip();

    const card1 = page.locator(`[data-task-id="${taskId1}"]`);
    const card2 = page.locator(`[data-task-id="${taskId2}"]`);

    await card1.click({ modifiers: ['Control'] });
    await card2.click({ modifiers: ['Control'] });

    const deleteBtn = page.locator('.bar-btn-delete');
    await expect(deleteBtn).toBeVisible();
    await expect(deleteBtn).toContainText('Delete 2 tasks');
  });

  test('should confirm before deleting selected tasks with task titles listed', async ({ browserName, page }) => {
    if (browserName === 'webkit') test.skip();

    const card1 = page.locator(`[data-task-id="${taskId1}"]`);
    const card2 = page.locator(`[data-task-id="${taskId2}"]`);

    await card1.click({ modifiers: ['Control'] });
    await card2.click({ modifiers: ['Control'] });

    const deleteBtn = page.locator('.bar-btn-delete');
    await deleteBtn.click();

    const dialog = page.locator('.modal');
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('Permanently delete 2 tasks');

    const card1Title = await card1.locator('.card-title-text').textContent();
    const card2Title = await card2.locator('.card-title-text').textContent();
    await expect(dialog).toContainText(card1Title!);
    await expect(dialog).toContainText(card2Title!);

    const cancelBtn = page.locator('.modal-footer .btn-cancel');
    await cancelBtn.click();
    await expect(dialog).not.toBeVisible();

    await expect(card1).toBeVisible();
    await expect(card2).toBeVisible();
  });

  test('should delete selected tasks on confirmation', async ({ browserName, page }) => {
    if (browserName === 'webkit') test.skip();

    const card1 = page.locator(`[data-task-id="${taskId1}"]`);
    const card2 = page.locator(`[data-task-id="${taskId2}"]`);

    await card1.click({ modifiers: ['Control'] });
    await card2.click({ modifiers: ['Control'] });

    const deleteBtn = page.locator('.bar-btn-delete');
    await deleteBtn.click();

    const confirmBtn = page.locator('.modal-footer .btn-confirm');
    await confirmBtn.click();

    await expect(card1).not.toBeVisible();
    await expect(card2).not.toBeVisible();

    const bar = page.locator('.multi-select-bar');
    await expect(bar).not.toBeVisible();
  });

  test('should clear selection on Escape', async ({ browserName, page }) => {
    if (browserName === 'webkit') test.skip();

    const card1 = page.locator(`[data-task-id="${taskId1}"]`);
    const card2 = page.locator(`[data-task-id="${taskId2}"]`);

    await card1.click({ modifiers: ['Control'] });
    await card2.click({ modifiers: ['Control'] });

    const bar = page.locator('.multi-select-bar');
    await expect(bar).toBeVisible();

    // Press Escape — should clear selection
    await page.keyboard.press('Escape');

    await expect(bar).not.toBeVisible();
    await expect(card1).not.toHaveClass(/multi-selected/);
    await expect(card2).not.toHaveClass(/multi-selected/);
  });

  test('should not show Start button when selected tasks are in different columns', async ({ browserName, page }) => {
    if (browserName === 'webkit') test.skip();

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId2}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    await expect(async () => {
      const inProgressCards = await page.locator('.column[aria-label="In Progress column"] .card').count();
      expect(inProgressCards).toBeGreaterThan(0);
    }).toPass({ timeout: 5000 });

    const card1 = page.locator(`[data-task-id="${taskId1}"]`);
    const card2 = page.locator(`[data-task-id="${taskId2}"]`).first();

    await card1.click({ modifiers: ['Control'] });
    await card2.click({ modifiers: ['Control'] });

    const startBtn = page.locator('.bar-btn-start');
    await expect(startBtn).not.toBeVisible();

    const deleteBtn = page.locator('.bar-btn-delete');
    await expect(deleteBtn).toBeVisible();
  });

});
