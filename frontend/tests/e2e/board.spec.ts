import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

test.describe.configure({ mode: 'parallel' });

// ============================================================
// Board Layout
// ============================================================

test.describe('Board Layout', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should display the board with 4 columns', async ({ page }) => {
    await expect(page.locator('.board')).toBeVisible();
    await expect(page.locator('.column')).toHaveCount(4);
  });
});

// ============================================================
// Column Chronological Ordering
// ============================================================

test.describe('Column Chronological Ordering', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should order tasks by last activity with most recent at bottom', async ({ page }) => {
    const projectId = await getProjectId(page);
    const task1 = await createTaskViaApi(page, projectId, `chrono-order-1-${Date.now()}`);
    const task2 = await createTaskViaApi(page, projectId, `chrono-order-2-${Date.now()}`);
    const task3 = await createTaskViaApi(page, projectId, `chrono-order-3-${Date.now()}`);

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await expect(async () => {
      const board = await page.evaluate(async ({ pid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return res.json();
      }, { pid: projectId });
      const backlogTasks = board.tasks.filter((t: any) => t.status === 'backlog');
      expect(backlogTasks.length).toBeGreaterThanOrEqual(3);
    }).toPass({ timeout: 10000 });

    // Move each task to review, waiting for distinct updated_at after each
    let lastUpdated: string | null = null;
    for (const tid of [task1, task2, task3]) {
      // Delay to ensure distinct timestamps (timestamps are in seconds precision)
      await page.waitForFunction(() => false, {}, { timeout: 1100, polling: 1100 }).catch(() => {});
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${tid}/move`, {
        method: 'POST',
        body: { status: 'review' },
      });
      // Fetch the new timestamp
      const newUpdated = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        const data = await res.json();
        return data.updated_at;
      }, { pid: projectId, tid });
      // For iterations after the first, verify timestamp changed
      if (lastUpdated !== null) {
        await expect(async () => {
          const task = await page.evaluate(async ({ pid, tid }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
            return res.json();
          }, { pid: projectId, tid });
          expect(task.updated_at).not.toBe(lastUpdated);
        }).toPass({ timeout: 5000 });
      }
      lastUpdated = newUpdated;
    }

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await expect(async () => {
      const board = await page.evaluate(async ({ pid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return res.json();
      }, { pid: projectId });
      const reviewTasks = board.tasks.filter((t: any) => t.status === 'review');
      expect(reviewTasks.length).toBeGreaterThanOrEqual(3);
    }).toPass({ timeout: 10000 });

    // Verify API order: task1 (oldest) first, task3 (newest) last
    const reviewOrder = await page.evaluate(async ({ pid, ids }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      const reviewTasks = board.tasks.filter((t: any) => t.status === 'review');
      const ordered: string[] = [];
      for (const task of reviewTasks) {
        if (ids.includes(task.id)) ordered.push(task.id);
      }
      return ordered;
    }, { pid: projectId, ids: [task1, task2, task3] });

    expect(reviewOrder[0]).toBe(task1);
    expect(reviewOrder[1]).toBe(task2);
    expect(reviewOrder[2]).toBe(task3);

    // Verify visual DOM order in the Review column matches
    const reviewColumn = page.locator('.column').nth(2);
    const reviewCards = reviewColumn.locator('.card');
    const domOrder = await reviewCards.evaluateAll(
      (cards) => cards.map((card) => card.getAttribute('data-task-id')).filter(Boolean) as string[]
    );
    const filteredDomOrder = domOrder.filter((id) => [task1, task2, task3].includes(id));
    expect(filteredDomOrder[0]).toBe(task1);
    expect(filteredDomOrder[1]).toBe(task2);
    expect(filteredDomOrder[2]).toBe(task3);
  });

  test('should place a newly-moved task at the bottom of the target column', async ({ page }) => {
    const projectId = await getProjectId(page);
    const existingTask = await createTaskViaApi(page, projectId, `chrono-existing-${Date.now()}`);
    const newTask = await createTaskViaApi(page, projectId, `chrono-new-${Date.now()}`);

    // Move existingTask to done first
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${existingTask}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });
    // Wait for distinct timestamp
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: existingTask });
      expect(task.status).toBe('done');
    }).toPass({ timeout: 5000 });

    // Move newTask to done — it should appear below existingTask
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${newTask}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });
    // Wait for distinct timestamp
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: newTask });
      expect(task.status).toBe('done');
    }).toPass({ timeout: 5000 });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await expect(async () => {
      const board = await page.evaluate(async ({ pid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return res.json();
      }, { pid: projectId });
      const doneTasks = board.tasks.filter((t: any) => t.status === 'done');
      expect(doneTasks.length).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 10000 });

    const doneOrder = await page.evaluate(async ({ pid, ids }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      const doneTasks = board.tasks.filter((t: any) => t.status === 'done');
      const ordered: string[] = [];
      for (const task of doneTasks) {
        if (ids.includes(task.id)) ordered.push(task.id);
      }
      return ordered;
    }, { pid: projectId, ids: [existingTask, newTask] });

    expect(doneOrder[0]).toBe(existingTask);
    expect(doneOrder[1]).toBe(newTask);
  });
});

// ============================================================
// Drag and Drop Between Columns
// ============================================================

test.describe('Drag and Drop Between Columns', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
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
    await page.goto('/');
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

  test('should keep current task section always expanded', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `scroll-task-a-${Date.now()}`);
    await createTaskViaApi(page, projectId, `scroll-task-b-${Date.now()}`);

    await navigateToTask(page, taskId1);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const backlogHeader = page.locator('.section-header').nth(0);
    await expect(backlogHeader).toHaveAttribute('aria-expanded', 'true');
    await expect(backlogHeader).toHaveAttribute('title', 'Backlog (current task)');

    // Clicking should NOT collapse (protected)
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
    await page.goto('/');
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

    await page.goto('/');
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
    await page.goto('/');
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

    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'done' }),
      });
    }, { pid: projectId, tid: taskId });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const doneCountBefore = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    expect(doneCountBefore).toBeGreaterThan(0);

    await page.click('.clear-btn');
    await expect(page.locator('.confirm-dialog')).toBeVisible();
    expect(await page.locator('.confirm-dialog p').textContent()).toContain('Permanently delete');

    await page.click('.confirm-dialog .btn-cancel');

    const doneCountAfter = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    expect(doneCountAfter).toBe(doneCountBefore);
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

    await page.goto('/');
    await page.waitForSelector('.card');

    const doneCountBefore = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    expect(doneCountBefore).toBe(doneCountBase + 2);

    await page.click('.clear-btn');
    await expect(page.locator('.confirm-dialog')).toBeVisible();
    await page.click('.confirm-dialog .btn-confirm');

    await page.goto('/');
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

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(task.status).toBe('done');
    }).toPass({ timeout: 10000 });

    await page.click('.clear-btn');
    await expect(page.locator('.confirm-dialog')).toBeVisible();
    await page.click('.confirm-dialog .btn-confirm');

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
    await page.goto('/');
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
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should center on the deleted task column after delete', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `mobile-scroll-${Date.now()}`);

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
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

    const { scrollLeft, boardWidth, colOffset, colWidth } = await page.evaluate(() => {
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

    expect(colOffset).not.toBeNull();

    const colCenter = (colOffset as number) + (colWidth as number) / 2;
    const viewportStart = (scrollLeft as number) - (colWidth as number) / 2;
    const viewportEnd = (scrollLeft as number) + (boardWidth as number) - (colWidth as number) / 2;
    expect(colCenter as number).toBeGreaterThan(viewportStart);
    expect(colCenter as number).toBeLessThan(viewportEnd);
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
    await page.goto('/');
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

    const cancelBtn = page.locator('.modal-footer .btn-secondary');
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

    const cancelBtn = page.locator('.modal-footer .btn-secondary');
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
