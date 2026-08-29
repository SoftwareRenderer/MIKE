import { test, expect, type Browser, type Page } from '@playwright/test';
import { rmSync, mkdirSync, writeFileSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch, navigateToTask, getProjectId } from './helpers';

// Regression: the Task View's side board column must not scroll on its own while
// the task detail is open.
//
// The board overview centers the open task's card. That reveal used to key on the
// currentTask object, and App.svelte replaces `selectedTask` with a fresh object on
// every `task_updated` frame — the executor sends several per turn — so each frame
// re-centered the column and yanked the reader back to the running task. The reveal
// now keys on the task id and leaves a card that is already on screen alone.
//
// Isolation: a DEDICATED repo + project (/tmp/kanban-test-repo-board-scroll) holds
// its own pile of backlog cards, so overflowing the column cannot disturb other
// specs running in parallel.

const SCROLL_REPO = '/tmp/kanban-test-repo-board-scroll';
const TASK_COUNT = 20;

/** Metrics for the board column and one of its cards, in viewport coordinates. */
interface ColumnMetrics {
  scrollTop: number;
  clientHeight: number;
  scrollHeight: number;
  cardTop: number | null;
  cardBottom: number | null;
  colTop: number;
  colBottom: number;
}

function metrics(page: Page, taskId: string): Promise<ColumnMetrics> {
  return page.evaluate((id) => {
    const col = document.querySelector<HTMLElement>('.column-context');
    const card = col?.querySelector<HTMLElement>(`.card[data-task-id="${id}"]`);
    if (!col) throw new Error('no .column-context (task view not open?)');
    const c = col.getBoundingClientRect();
    const k = card?.getBoundingClientRect();
    return {
      scrollTop: col.scrollTop,
      clientHeight: col.clientHeight,
      scrollHeight: col.scrollHeight,
      cardTop: k ? k.top : null,
      cardBottom: k ? k.bottom : null,
      colTop: c.top,
      colBottom: c.bottom,
    };
  }, taskId);
}

/**
 * Open an arbitrary task, then return the id of the LAST card rendered in the
 * board column — the one furthest below the fold, whatever the card heights or
 * sort order happen to be.
 */
async function pickBottomCard(page: Page, projectId: string): Promise<string> {
  const board = await page.evaluate(async (pid) => (await fetch(`/api/projects/${pid}/board`)).json(), projectId);
  await navigateToTask(page, board.tasks[0].id, projectId);
  await expect(page.locator('.column-context .card').first()).toBeVisible();
  const targetId: string = await page.evaluate(() => {
    const cards = Array.from(document.querySelectorAll<HTMLElement>('.column-context .card[data-task-id]'));
    return cards[cards.length - 1].getAttribute('data-task-id') as string;
  });
  expect(targetId).toBeTruthy();
  return targetId;
}

async function setupPage(browser: Browser) {
  const context = await browser.newContext({ storageState: '/tmp/kanban-test-storage-state.json' });
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

test.describe.serial('Task View board column auto-scroll', () => {
  let projectId = '';

  test.beforeAll(async ({ browser }) => {
    rmSync(SCROLL_REPO, { recursive: true, force: true });
    mkdirSync(SCROLL_REPO, { recursive: true });
    writeFileSync(join(SCROLL_REPO, 'README.md'), 'board-scroll fixture repo\n');
    execSync('git init', { cwd: SCROLL_REPO, stdio: 'ignore' });
    execSync('git add README.md', { cwd: SCROLL_REPO, stdio: 'ignore' });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Initial commit"', {
      cwd: SCROLL_REPO, stdio: 'ignore',
    });

    const { context, page } = await setupPage(browser);
    try {
      const res = await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: SCROLL_REPO } });
      if (!res.ok) throw new Error(`create board-scroll project: ${JSON.stringify(res.data)}`);
      projectId = res.data.id;

      // Backlog is the first section and newest-created sorts to the top, so 20
      // tasks give the column plenty of overflow.
      for (let i = 0; i < TASK_COUNT; i++) {
        const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
          method: 'POST',
          body: { prompt: `board-scroll task ${i} ${Date.now()}` },
        });
        if (!created.ok) throw new Error(`create task ${i}: ${JSON.stringify(created.data)}`);
      }
      await expect(async () => {
        const board = await page.evaluate(async (pid) => (await fetch(`/api/projects/${pid}/board`)).json(), projectId);
        expect(board.tasks.length).toBe(TASK_COUNT);
      }).toPass({ timeout: 10_000 });
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    const { context, page } = await setupPage(browser);
    try {
      if (projectId) await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
    } finally {
      await context.close();
    }
    rmSync(SCROLL_REPO, { recursive: true, force: true });
  });

  test('opens on a card below the fold and centers it', async ({ page }) => {
    await page.goto('/');
    if (!projectId) projectId = await getProjectId(page);

    const targetId = await pickBottomCard(page, projectId);
    await navigateToTask(page, targetId, projectId);
    const card = page.locator(`.column-context .card[data-task-id="${targetId}"]`);
    await expect(card).toBeVisible();

    await expect
      .poll(async () => {
        const m = await metrics(page, targetId);
        return m.scrollHeight > m.clientHeight && m.scrollTop > 0 && m.cardTop! >= m.colTop && m.cardBottom! <= m.colBottom;
      }, { message: 'the open task\'s card should be scrolled fully into view' })
      .toBe(true);
  });

  test('a task_updated push does not move the column', async ({ page }) => {
    await page.goto('/');
    // The bottom-most card: centering has to scroll to reach it.
    const targetId = await pickBottomCard(page, projectId);
    await navigateToTask(page, targetId, projectId);
    await expect(page.locator(`.column-context .card[data-task-id="${targetId}"]`)).toBeVisible();

    // Read the list from the top, away from the open task's card.
    await page.evaluate(() => { document.querySelector<HTMLElement>('.column-context')!.scrollTop = 0; });
    const before = (await metrics(page, targetId)).scrollTop;
    expect(before).toBe(0);

    // PUT broadcasts task_updated for the open task — the same frame the executor
    // sends several times per turn. Only the title changes, so no re-ranking.
    const newTitle = `renamed-${Date.now()}`;
    const res = await csrfFetch(page, `/api/projects/${projectId}/tasks/${targetId}`, {
      method: 'PUT',
      body: { title: newTitle },
    });
    expect(res.ok).toBe(true);

    // The push has landed when the side board card shows the new title.
    await expect(page.locator(`.column-context .card[data-task-id="${targetId}"] .card-title-text`)).toHaveText(newTitle);
    // Allow any rAF-scheduled scroll to fire.
    await page.waitForTimeout(400);

    const m = await metrics(page, targetId);
    expect(m.scrollTop).toBe(before);
  });
});
