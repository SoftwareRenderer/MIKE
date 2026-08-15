import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

// ============================================================
// BOARD ARROW-KEY NAVIGATION (cards + project list)
// ============================================================
// Arrows move real DOM focus around the 5-column ring
// [Projects][Backlog][In Progress][Review][Done]; Down/Up wrap within a
// column, first Down anchors to the first backlog card, Enter opens the
// focused card, Alt+Arrow still moves tasks. Chromium project (no config
// mutation).

const colIdx = (page: Page) => page.evaluate(() => {
    const card = document.activeElement as HTMLElement | null;
    const col = card?.closest('.column');
    const cols = Array.from(document.querySelectorAll('.board .column'));
    const cardsInCol = Array.from(col?.querySelectorAll(':scope > .card-list > .task-wrapper > .card') ?? []);
    return { col: cols.indexOf(col as Element), idx: cardsInCol.indexOf(card) };
});

async function moveTask(page: Page, projectId: string, taskId: string, status: string) {
    const res = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        body: { status },
    });
    if (!res.ok) throw new Error(`moveTask: ${JSON.stringify(res.data)}`);
}

test.describe.serial('Board arrow-key navigation', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.board .column', { timeout: 10000 });
  });

  test('first Down focuses the first backlog card without opening it', async ({ page }) => {
    await page.evaluate(() => document.body.focus());
    await page.keyboard.press('ArrowDown');
    const pos = await colIdx(page);
    expect(pos).toEqual({ col: 0, idx: 0 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
  });

  test('Down/Up move to next/prev card and wrap at both ends', async ({ page }) => {
    const cards = page.locator('.board .column').first().locator('.task-wrapper > .card');
    expect(await cards.count()).toBeGreaterThanOrEqual(2);
    const firstId = await cards.nth(0).getAttribute('data-task-id');
    const secondId = await cards.nth(1).getAttribute('data-task-id');

    await cards.nth(0).focus();
    await page.keyboard.press('ArrowDown');
    expect(await colIdx(page)).toEqual({ col: 0, idx: 1 });
    expect(await page.evaluate(() => (document.activeElement as HTMLElement).dataset?.taskId)).toBe(secondId);

    await page.keyboard.press('ArrowUp');
    expect(await page.evaluate(() => (document.activeElement as HTMLElement).dataset?.taskId)).toBe(firstId);

    // Wrap: Down from the last card -> first card
    const last = cards.nth(await cards.count() - 1);
    await last.focus();
    await page.keyboard.press('ArrowDown');
    expect(await colIdx(page)).toEqual({ col: 0, idx: 0 });
  });

  test('Left/Right cycle the 5-column ring: projects + 4 board columns', async ({ page }) => {
    const projectId = await getProjectId(page);
    const ipTask = await createTaskViaApi(page, projectId, `nav-ip-${Date.now()}`);
    const doneTask = await createTaskViaApi(page, projectId, `nav-done-${Date.now()}`);
    await moveTask(page, projectId, ipTask, 'in_progress');
    await moveTask(page, projectId, doneTask, 'done');
    await page.reload();
    await page.waitForSelector('.board .column', { timeout: 10000 });

    // Only one project at this point — the active project item
    const projItem = page.locator('nav.project-list button.project-item').first();
    const firstCol = page.locator('.board .column');

    await firstCol.nth(0).locator('.task-wrapper > .card').first().focus();
    // backlog -> in_progress
    await page.keyboard.press('ArrowRight');
    expect(await colIdx(page)).toEqual({ col: 1, idx: 0 });
    // in_progress -> done (review is empty -> skipped)
    await page.keyboard.press('ArrowRight');
    expect(await colIdx(page)).toEqual({ col: 3, idx: 0 });
    // done -> projects (active project item focused)
    await page.keyboard.press('ArrowRight');
    await expect(projItem).toBeFocused();
    // projects -> first backlog card (ring closes)
    await page.keyboard.press('ArrowRight');
    expect(await colIdx(page)).toEqual({ col: 0, idx: 0 });
    // backlog -> projects
    await page.keyboard.press('ArrowLeft');
    await expect(projItem).toBeFocused();
    // projects -> first done card (left edge wraps to right edge)
    await page.keyboard.press('ArrowLeft');
    expect(await colIdx(page)).toEqual({ col: 3, idx: 0 });
  });

  test('Enter opens the focused card; Escape returns to the board', async ({ page }) => {
    await page.locator('.board .column').first().locator('.task-wrapper > .card').first().focus();
    await page.keyboard.press('Enter');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await page.keyboard.press('Escape');
    await expect(page.locator('.task-detail-page')).not.toBeVisible({ timeout: 5000 });
  });

  test('Alt+Arrow still moves the focused task (regression)', async ({ page }) => {
    const projectId = await getProjectId(page);
    const prompt = `nav-alt-${Date.now()}`;
    await createTaskViaApi(page, projectId, prompt);
    const card = page.locator('.card', { hasText: prompt }).first();
    await expect(card).toBeVisible({ timeout: 10000 });
    await card.focus();

    await page.keyboard.press('Alt+ArrowRight');
    await expect(page.locator('.board .column').nth(1).locator('.card', { hasText: prompt })).toBeVisible({ timeout: 10000 });

    await page.keyboard.press('Alt+ArrowLeft');
    await expect(page.locator('.board .column').first().locator('.card', { hasText: prompt })).toBeVisible({ timeout: 10000 });
  });

  test('project list: Down/Up select prev/next project and wrap', async ({ page }) => {
    const res = await csrfFetch(page, '/api/projects', {
      method: 'POST',
      body: { path: '/tmp/kanban-test-repo-2' },
    });
    if (!res.ok) throw new Error(`create project: ${JSON.stringify(res.data)}`);
    await page.reload();
    const emptyItem = page.locator('nav.project-list button.project-item', { hasText: 'kanban-test-repo-2' });
    const mainItem = page.locator('nav.project-list button.project-item', { hasText: 'test-project' });
    const emptyWrap = page.locator('nav.project-list .project-item-wrapper', { hasText: 'kanban-test-repo-2' });
    const mainWrap = page.locator('nav.project-list .project-item-wrapper', { hasText: 'test-project' });
    await expect(emptyItem).toBeVisible({ timeout: 10000 });

    // Down selects the next project; the board follows
    await emptyItem.focus();
    await page.keyboard.press('ArrowDown');
    await expect(mainItem).toBeFocused();
    await expect(mainWrap).toHaveClass(/active/);
    // Focused-project ring (poll: .project-item transitions over 150ms)
    await expect.poll(async () => page.evaluate(() => {
      const probe = document.createElement('span');
      probe.style.color = 'var(--color-accent)';
      document.body.appendChild(probe);
      const accent = getComputedStyle(probe).color;
      probe.remove();
      const shadow = getComputedStyle(document.activeElement).boxShadow;
      return shadow.includes('inset') && shadow.includes(accent) && shadow.includes('2px');
    })).toBe(true);
    await expect(page.locator('.board .card').first()).toBeVisible({ timeout: 10000 });

    // Down on the last project wraps back to the first (empty) one
    await page.keyboard.press('ArrowDown');
    await expect(emptyItem).toBeFocused();
    await expect(emptyWrap).toHaveClass(/active/);
    await expect(page.locator('.board .card')).toHaveCount(0, { timeout: 10000 });
  });

  test('Right from a project focuses the board\'s first card', async ({ page }) => {
    const mainItem = page.locator('nav.project-list button.project-item', { hasText: 'test-project' });
    await mainItem.click();
    await expect(page.locator('.board .card').first()).toBeVisible({ timeout: 10000 });
    await mainItem.focus();
    await page.keyboard.press('ArrowRight');
    const inBoardCard = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null;
      return !!el?.classList.contains('card') && !!el.closest('.board');
    });
    expect(inBoardCard).toBe(true);
  });

  test('arrows are inactive while focus is in an editable inside a modal', async ({ page }) => {
    await page.evaluate(() => document.body.focus());
    await page.keyboard.press('c');
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });
    await page.locator('#task-description').click();
    await page.keyboard.press('ArrowDown');
    await expect(page.locator('#task-description')).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
  });
});
