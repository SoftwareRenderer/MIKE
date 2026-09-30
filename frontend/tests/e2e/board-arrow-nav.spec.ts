import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch, isolatedProject, gotoProject, clearBoardExcept, apiGetJson } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


// ============================================================
// BOARD ARROW-KEY NAVIGATION (cards + project list)
// ============================================================
// Arrows move real DOM focus around the 5-column ring [Projects][Backlog][In Progress][Review]
// [Done]; Down/Up wrap within a column, the first Down anchors to the first backlog card, Enter
// opens the focused card, Alt+Arrow still moves tasks. Enter on a control *inside* the card belongs
// to that control — only a browser activates a button from a keypress, so that lives here, not in
// a unit test.

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
    await gotoProject(page);
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
    // The ring skips EMPTY columns, and this worker's project carries tasks from
    // whichever spec ran on it before — so start from a board this test owns.
    await clearBoardExcept(page, projectId, ['backlog']);
    // The empty column is IN PROGRESS, not review: moving a task to in_progress starts a turn,
    // and the mock answer can carry it on into review, changing the ring mid-test.
    const reviewTask = await createTaskViaApi(page, projectId, `nav-review-${Date.now()}`);
    const doneTask = await createTaskViaApi(page, projectId, `nav-done-${Date.now()}`);
    await moveTask(page, projectId, reviewTask, 'review');
    await moveTask(page, projectId, doneTask, 'done');
    await page.reload();
    await page.waitForSelector('.board .column', { timeout: 10000 });

    // Only one project at this point — the active project item
    // the ring's project step focuses the ACTIVE project — address ours, not `.first()`
    const projItem = page.locator(`nav.project-list a.project-item[href*="project=${projectId}"]`);
    const firstCol = page.locator('.board .column');

    await firstCol.nth(0).locator('.task-wrapper > .card').first().focus();
    // backlog -> review (in_progress is empty -> skipped)
    await page.keyboard.press('ArrowRight');
    expect(await colIdx(page)).toEqual({ col: 2, idx: 0 });
    // review -> done
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

  test('Tab reaches a card action icon and Enter runs that icon, not the card', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `kbd-action-${Date.now()}`);
    await page.reload();
    await page.waitForSelector('.board .column', { timeout: 10000 });
    const card = page.locator(`.board .card[data-task-id="${taskId}"]`);
    await expect(card).toBeVisible();

    await card.focus();
    // Walk the card's own tab stops until the edit icon holds focus, so the dialog asserted below
    // is the one this icon owns.
    const onEditIcon = () => page.evaluate(() =>
        document.activeElement?.matches('.action-icon[aria-label="Edit task"]') ?? false);
    let tabs = 0;
    while (tabs < 6 && !(await onEditIcon())) {
      await page.keyboard.press('Tab');
      tabs++;
    }
    expect(await onEditIcon(), `the edit icon is not within ${tabs} Tab presses of the card`).toBe(true);

    // The icons are painted only for a keyboard that can reach them: focus-visible must draw on the
    // button, and the card must reveal the row without a hover. The reveal fades over 150ms, so
    // poll it instead of measuring the frame the focus landed in.
    await expect.poll(() => page.evaluate(() => {
      const el = document.activeElement as HTMLElement;
      const cs = getComputedStyle(el);
      return {
        focusVisible: el.matches(':focus-visible'),
        ring: cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) > 0,
        actionsOpacity: getComputedStyle(el.closest('.card-actions')!).opacity,
      };
    }), { timeout: 3000 }).toEqual({
      focusVisible: true, ring: true, actionsOpacity: '1',
    });

    // Enter is the icon's own activation: the edit dialog opens and the task stays closed.
    await page.keyboard.press('Enter');
    const editHeading = page.getByRole('heading', { name: 'Edit Task' });
    await expect(editHeading).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    await page.keyboard.press('Escape');
    await expect(editHeading).not.toBeVisible({ timeout: 5000 });
  });

  test('Alt+Arrow still moves the focused task (regression)', async ({ page }) => {
    const projectId = await getProjectId(page);
    const prompt = `nav-alt-${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, prompt);
    // Seeded through review by API, for the reason the ring test states: a move into
    // in_progress starts a turn, and that turn's completion moves the task onward while this
    // test is still asserting.
    await moveTask(page, projectId, taskId, 'review');
    await page.reload();
    await page.waitForSelector('.board .column', { timeout: 10000 });

    // The task's status as the server has it. Not visibility of a column locator: a move
    // repaints the card through the columns it passes, and the board store and the render
    // disagree for a frame or two, so `expect(col.locator('.card')).toBeVisible()` also passed
    // with Alt+ArrowLeft handled as Alt+ArrowRight (measured, not assumed).
    const statusOf = () => expect.poll(async () => {
      const board = await apiGetJson<{ tasks?: any[] }>(page, `/api/projects/${projectId}/board`);
      return board?.tasks?.find((t: any) => t.id === taskId)?.status;
    }, { timeout: 10000 });

    await page.locator('.board .column').nth(2).locator('.card', { hasText: prompt }).focus();
    await page.keyboard.press('Alt+ArrowRight');
    await statusOf().toBe('done');

    // Focus is on the card element, and the move recreates it: the second press needs its own
    // focus or it lands on body and moves nothing.
    await page.locator('.board .column').nth(3).locator('.card', { hasText: prompt }).focus();
    await page.keyboard.press('Alt+ArrowLeft');
    await statusOf().toBe('review');

    // And the board this page holds agrees with the server about where the card sits.
    await expect.poll(async () => page.evaluate((p) => {
      const cols = Array.from(document.querySelectorAll('.board .column'));
      const card = (Array.from(document.querySelectorAll('.board .card')) as HTMLElement[])
        .find((c) => (c.textContent || '').includes(p));
      return card ? cols.indexOf(card.closest('.column') as Element) : -1;
    }, prompt), { timeout: 10000 }).toBe(2);
  });

  test('project list: Down/Up walk the project ring and wrap', async ({ page }) => {
    // A ring walk, not a fixed pair of rows: other workers create projects on this
    // same server, so the test may not assume which project is next to which. What it
    // can assert is the ring's contract — Down/Up move to the adjacent row in DOM
    // order, the active project follows the focus, and the ends wrap.
    const items = page.locator('nav.project-list .project-item');
    await expect(items.first()).toBeVisible({ timeout: 10000 });
    const wrappers = page.locator('nav.project-list .project-item-wrapper');
    const count = await items.count();
    expect(count).toBeGreaterThanOrEqual(2);

    await items.first().focus();
    for (let step = 1; step <= count; step++) {
      await page.keyboard.press('ArrowDown');
      const idx = step % count;
      await expect(items.nth(idx)).toBeFocused({ timeout: 5000 });
      // The board follows the focus: the active row is always the focused one.
      await expect(wrappers.nth(idx)).toHaveClass(/active/, { timeout: 5000 });
    }
    // The count-th press wrapped from the last row back to the first.
    await expect(items.first()).toBeFocused();

    // The focus ring is an inset box-shadow on the row itself (ancestors clip outlines).
    await expect.poll(async () => page.evaluate(() => {
      const probe = document.createElement('span');
      probe.style.color = 'var(--color-accent)';
      document.body.appendChild(probe);
      const accent = getComputedStyle(probe).color;
      probe.remove();
      const shadow = getComputedStyle(document.activeElement).boxShadow;
      return shadow.includes('inset') && shadow.includes(accent) && shadow.includes('2px');
    })).toBe(true);

    // Up walks back.
    await page.keyboard.press('ArrowUp');
    await expect(items.nth(count - 1)).toBeFocused({ timeout: 5000 });
  });

  test('Right from a project focuses the board\'s first card', async ({ page }) => {
    // The row of this worker's own project, by id like the other resolution here: matching the fixture project
    // by name would switch projects.
    const projectId = await getProjectId(page);
    const mainItem = page.locator(`nav.project-list a.project-item[href*="project=${projectId}"]`);
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
