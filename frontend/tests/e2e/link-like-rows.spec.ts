import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, waitForTaskState, csrfFetch } from './helpers';


// ============================================================
// LINK-LIKE ROWS — sidebar projects and task cards
// ============================================================
// Both are anchors, so the browser offers its link actions (right-click, middle click) while an
// unmodified click stays inside the SPA, using the same ?project= / ?task= params the app writes.

/** Stamp the current document, so a real (re)navigation can be detected. */
async function markDocument(page: Page) {
  await page.evaluate(() => { (window as any).__docMark = 'alive'; });
}

/** True while the same document is still loaded (no full page navigation). */
async function sameDocument(page: Page): Promise<boolean> {
  return await page.evaluate(() => (window as any).__docMark) === 'alive';
}

async function openBoard(page: Page) {
  await page.goto('/');
  await page.waitForSelector('.project-item', { timeout: 10000 });
  await page.waitForSelector('.card', { timeout: 10000 });
}

test.describe('Sidebar project rows are links', () => {
  test('the row is an anchor whose href deep-links to the project', async ({ page }) => {
    await openBoard(page);

    const row = page.locator('.project-item').first();
    // The sidebar sorts by name while /api/projects returns creation order, so resolve
    // the expected id by the row's own label instead of assuming projects[0].
    const expectedId = await row.evaluate(async (el) => {
      const res = await fetch('/api/projects');
      const projects: Array<{ id: string; name: string }> = await res.json();
      const label = el.querySelector('.project-name')?.textContent?.trim() ?? '';
      return projects.find(p => p.name === label)?.id ?? '';
    });
    expect(expectedId).not.toBe('');

    await expect(row).toHaveAttribute('href', new RegExp(`^/\\?project=${expectedId}$`));
    // Still a button-role row for assistive tech.
    await expect(row).toHaveAttribute('role', 'button');
    await expect(row).toHaveAttribute('tabindex', '0');
  });

  test('an unmodified click selects the project without leaving the document', async ({ page }) => {
    await openBoard(page);
    await markDocument(page);

    await page.locator('.project-item').first().click();

    await expect(page.locator('.project-item-wrapper.active')).toBeVisible({ timeout: 5000 });
    expect(await sameDocument(page)).toBe(true);
  });

  test('opening the project link in another tab lands on that project', async ({ page, context }) => {
    await openBoard(page);
    const firstRow = page.locator('.project-item').first();
    const name = (await firstRow.locator('.project-name').textContent())?.trim();
    const href = await firstRow.getAttribute('href');
    expect(href).toBeTruthy();

    const other = await context.newPage();
    try {
      await other.goto(href!);
      await other.waitForSelector('.project-item-wrapper.active', { timeout: 10000 });
      const activeName = (await other.locator('.project-item-wrapper.active .project-name').textContent())?.trim();
      expect(activeName).toBe(name);
    } finally {
      await other.close();
    }
  });
});

test.describe.serial('Task cards are links', () => {
  let projectId = '';
  let taskId = '';

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    try {
      await page.goto('/');
      await page.waitForSelector('.project-item', { timeout: 10000 });
      projectId = await getProjectId(page);
      taskId = await createTaskViaApi(page, projectId, `link-card-${Date.now()}`);
    } finally {
      await page.close();
    }
  });

  // The board is shared with other specs — leave no task behind.
  test.afterAll(async ({ browser }) => {
    if (!taskId) return;
    const page = await browser.newPage();
    try {
      await page.goto('/');
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
    } finally {
      await page.close();
    }
  });

  test.beforeEach(async ({ page }) => {
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector(`.card[data-task-id="${taskId}"]`, { timeout: 10000 });
  });

  test('the card carries a link to itself and stays a draggable button', async ({ page }) => {
    const card = page.locator(`.card[data-task-id="${taskId}"]`);
    const link = card.locator('a.card-open-link');

    await expect(link).toHaveAttribute('href', `/?project=${projectId}&task=${taskId}`);
    // The overlay is decorative for assistive tech and never the drag source.
    await expect(link).toHaveAttribute('aria-hidden', 'true');
    await expect(link).toHaveAttribute('tabindex', '-1');
    await expect(link).toHaveAttribute('draggable', 'false');
    await expect(card).toHaveAttribute('role', 'button');
    await expect(card).toHaveAttribute('draggable', 'true');
  });

  test('clicking the card title opens the task inside the same document', async ({ page }) => {
    await markDocument(page);
    // The overlay covers the title, so this click lands on the link.
    await page.locator(`.card[data-task-id="${taskId}"] .card-title-text`).click();

    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    expect(await page.url()).toContain(`task=${taskId}`);
    expect(await sameDocument(page)).toBe(true);
  });

  test('the bound modifier (Ctrl+Click here) multi-selects and never opens a tab', async ({ page, context }) => {
    await markDocument(page);
    const pagesBefore = context.pages().length;

    // Chromium here is not a Mac, so the default binding is Ctrl+Click.
    await page.locator(`.card[data-task-id="${taskId}"] .card-title-text`).click({ modifiers: ['Control'] });

    await expect(page.locator(`.card[data-task-id="${taskId}"].multi-selected`)).toBeVisible({ timeout: 5000 });
    expect(await sameDocument(page)).toBe(true);
    expect(context.pages().length).toBe(pagesBefore);
    expect(await page.url()).not.toContain(`task=${taskId}`);
  });

  test('middle-clicking the card opens the task in a new tab', async ({ page, context }) => {
    // A locator click, not boundingBox + mouse.click: the board re-renders underneath
    // (diff stats arrive, heights change), and coordinates taken before the click then
    // hit a neighbouring card — no popup, timeout.
    const popupPromise = context.waitForEvent('page', { timeout: 20000 });
    await page.locator(`.card[data-task-id="${taskId}"] .card-title-text`).click({ button: 'middle' });

    const tab = await popupPromise;
    try {
      await tab.waitForSelector('.task-detail-page', { timeout: 15000 });
      expect(tab.url()).toContain(`task=${taskId}`);
    } finally {
      await tab.close();
    }
  });

  test('the overlay covers the card body but never its buttons', async ({ page }) => {
    const card = page.locator(`.card[data-task-id="${taskId}"]`);
    // Action buttons only take pointer events while the card is hovered.
    await card.hover();

    const hitClass = async (locator: ReturnType<typeof page.locator>) => {
      const box = await locator.boundingBox();
      expect(box).toBeTruthy();
      // getAttribute, not .className: an SVG hit element's className is an object.
      return page.evaluate(
        ([x, y]) => document.elementFromPoint(x, y)?.getAttribute('class') ?? '',
        [box!.x + box!.width / 2, box!.y + box!.height / 2],
      );
    };

    // The title sits UNDER the overlay — a right-click there must be a link click.
    expect(await hitClass(card.locator('.card-title-text'))).toContain('card-open-link');
    // The card's own controls stay on top of it.
    expect(await hitClass(card.locator('button[aria-label="Edit task"]'))).not.toContain('card-open-link');
  });

  test('a card is still dragged by its body — the link does not hijack the drag', async ({ page }) => {
    // Record which element became the HTML5 drag source.
    await page.evaluate(() => {
      (window as any).__dragSource = null;
      document.addEventListener('dragstart', (e) => {
        (window as any).__dragSource = (e.target as HTMLElement).className;
      }, true);
    });

    const card = page.locator(`.card[data-task-id="${taskId}"]`);
    // Review, not In Progress: a start would run the mock LLM and change the shared board.
    const target = page.locator('[aria-label="Review column"] .card-list');
    const from = await card.locator('.card-title-text').boundingBox();
    const to = await target.boundingBox();
    expect(from && to).toBeTruthy();

    await page.mouse.move(from!.x + from!.width / 2, from!.y + from!.height / 2);
    await page.mouse.down();
    await page.mouse.move(from!.x + from!.width / 2, from!.y + from!.height / 2 + 20, { steps: 5 });
    await page.mouse.move(to!.x + to!.width / 2, to!.y + Math.min(to!.height - 10, to!.height / 2), { steps: 15 });
    await page.mouse.up();

    // The card element dragged, not its link overlay.
    await expect.poll(async () => {
      const cls: string | null = await page.evaluate(() => (window as any).__dragSource);
      return cls !== null && !cls!.includes('card-open-link');
    }, { timeout: 5000 }).toBe(true);

    // …and the drop really moved the task.
    await waitForTaskState(page, projectId, taskId, t => t.status === 'review', 20000);
  });
});
