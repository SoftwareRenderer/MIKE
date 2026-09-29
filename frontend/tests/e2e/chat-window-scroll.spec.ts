import { test, expect } from '@playwright/test';
import { atTopOfList } from '../../src/lib/utils/chat-window';
import {
  isolatedProject, gotoProject, getProjectId, csrfFetch, navigateToTask, waitForChatFormReady,
  waitForChatStill,
} from './helpers';

// A task opens on the newest window and older rows arrive a page at a time. Scrolling is the only
// affordance for that — nothing is rendered asking the reader to click. Whether a page pushed the reader
// down the screen is a layout question, so it is measured here; the reducer behind it is unit-tested in
// `src/lib/utils/__tests__/chat-window.unit.ts`.
isolatedProject();

const SEED_ROWS = 200;

/** Seed a conversation one row at a time, oldest first, from inside the page. */
async function seedConversation(page: any, projectId: string, taskId: string, rows: number): Promise<void> {
  const csrf = await page.evaluate(async () => (await (await fetch('/api/auth/session')).json()).csrf_token || '');
  const seeded = await page.evaluate(async ({ url, count, token }) => {
    let made = 0;
    for (let i = 0; i < count; i++) {
      const res = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(token ? { 'X-CSRF-Token': token } : {}) },
        credentials: 'include',
        body: JSON.stringify({
          role: 'user',
          content: `seed row ${i}\n${'filler text that gives the entry some height '.repeat(3)}`,
        }),
      });
      if (!res.ok) return { made, status: res.status, text: await res.text() };
      made++;
    }
    return { made, status: 200, text: '' };
  }, { url: `/api/projects/${projectId}/tasks/${taskId}/messages`, count: rows, token: csrf });
  expect(seeded.status, `seeding stopped at row ${seeded.made}: ${seeded.text}`).toBe(200);
}

/** Where the reader is: the first entry with pixels below the top edge of the scroll viewport. */
function viewportAnchor(page: any): Promise<{ id: string; top: number } | null> {
  return page.evaluate(() => {
    const list = document.querySelector('.chat-messages') as HTMLElement;
    const top = list.getBoundingClientRect().top;
    for (const node of Array.from(list.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'))) {
      const box = node.getBoundingClientRect();
      if (box.bottom > top) return { id: node.dataset.entryId as string, top: box.top - top };
    }
    return null;
  });
}

/** A named entry's top, in offsets from the top edge of the scroll viewport; null once it is unrendered. */
function entryTop(page: any, id: string): Promise<number | null> {
  return page.evaluate((entryId) => {
    const list = document.querySelector('.chat-messages') as HTMLElement;
    const node = list.querySelector<HTMLElement>(`.chat-entry[data-entry-id="${entryId}"]`);
    return node ? node.getBoundingClientRect().top - list.getBoundingClientRect().top : null;
  }, id);
}

/** The ids rendered, oldest first — a repeat is one row held by two pages. */
function renderedIds(page: any): Promise<string[]> {
  // `.chat-entry` is the row; a user row nests a `.sticky-user-message` carrying the SAME
  // data-entry-id, which is the pinned label, not a second copy of the row.
  return page.evaluate(() =>
    Array.from(document.querySelectorAll<HTMLElement>('.chat-messages .chat-entry[data-entry-id]')).map(n => n.dataset.entryId as string));
}

/**
 * One wheel notch. Entries render lazily, so their real heights appear as the reader approaches and the
 * browser re-anchors them — a small net loss per notch that a short one never overtakes.
 */
const WHEEL_NOTCH = 1500;

/** Park the pointer on the list, since a wheel goes where the pointer is. */
async function parkOnList(page: any): Promise<void> {
  const box = await page.locator('.chat-messages').boundingBox();
  expect(box, 'no chat list to scroll').not.toBeNull();
  await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);
}

/** Where the reader is in the list and how tall their view of it is, as the app's own edge rule needs. */
function listPosition(page: any): Promise<{ scrollTop: number; viewportHeight: number }> {
  return page.evaluate(() => {
    const el = document.querySelector('.chat-messages') as HTMLElement;
    return { scrollTop: el.scrollTop, viewportHeight: el.clientHeight };
  });
}

/**
 * Scroll the way a reader does. Writing `scrollTop` is not a gesture: the follow tracker reads an
 * unexplained move as the document shifting under the reader and pins it back to the bottom, so the
 * reader never reaches the edge older rows are fetched from.
 */
async function wheelUp(page: any, notches: number): Promise<void> {
  await parkOnList(page);
  for (let i = 0; i < notches; i++) {
    await page.mouse.wheel(0, -WHEEL_NOTCH);
    await page.waitForTimeout(40); // notch pacing: two wheels inside one frame coalesce into one scroll event
  }
  await waitForChatStill(page); // the scroll events behind the notches have all been reported
}

/** The edge older rows are fetched from, however many notches the realized heights take. */
async function wheelToTopEdge(page: any): Promise<void> {
  await parkOnList(page);
  let at = await listPosition(page);
  for (let i = 0; i < 60 && !atTopOfList(at.scrollTop, at.viewportHeight); i++) {
    await page.mouse.wheel(0, -WHEEL_NOTCH);
    await page.waitForTimeout(40); // notch pacing: two wheels inside one frame coalesce into one scroll event
    at = await listPosition(page);
  }
  expect(atTopOfList(at.scrollTop, at.viewportHeight),
    `the wheel never reached the edge older rows are fetched from (scrollTop ${at.scrollTop} of a `
    + `${at.viewportHeight}-pixel view)`).toBe(true);
}

/** Hold the next-older window in flight, so the reader's position can be read before it lands. */
async function stallOlderWindow(page: any, ms: number): Promise<void> {
  await page.route(url => url.searchParams.has('before'), async route => {
    await new Promise(resolve => setTimeout(resolve, ms));
    await route.continue();
  });
}

async function openConversation(page: any): Promise<{ projectId: string; taskId: string }> {
  await gotoProject(page);
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: { title: `windowed-${Date.now()}`, prompt: 'windowed history fixture', status: 'in_progress' },
  });
  expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
  const taskId: string = created.data.id;
  await seedConversation(page, projectId, taskId, SEED_ROWS);
  await navigateToTask(page, taskId, projectId);
  await waitForChatFormReady(page);
  await expect(page.locator('.chat-messages [data-entry-id]').first()).toBeVisible({ timeout: 15000 });
  return { projectId, taskId };
}

test.describe('Chat history window', () => {
  test('opens on the newest rows, and scrolling up is the only way to the older ones', async ({ page }) => {
    test.setTimeout(120000);
    await openConversation(page);

    const loaded = await renderedIds(page);
    expect(loaded.length, 'the first paint holds a window, not the thread').toBeLessThan(SEED_ROWS);
    expect(new Set(loaded).size, 'one row rendered twice').toBe(loaded.length);
    await expect(page.getByText('Load earlier messages')).toHaveCount(0);
  });

  test('scrolling to the top of the list brings the older rows, unasked', async ({ page }) => {
    test.setTimeout(120000);
    await openConversation(page);
    const beforeIds = await renderedIds(page);

    await wheelToTopEdge(page);
    await expect(async () => {
      expect((await renderedIds(page)).length).toBeGreaterThan(beforeIds.length);
    }).toPass({ timeout: 15000 });

    const afterIds = await renderedIds(page);
    expect(new Set(afterIds).size, 'a row on two pages is rendered twice').toBe(afterIds.length);
    expect(afterIds.slice(-beforeIds.length), 'the rows held before the page are no longer in the list')
        .toEqual(beforeIds);
  });

  test('older rows arriving leave the reader on the row they were reading', async ({ page }) => {
    test.setTimeout(120000);
    await openConversation(page);
    await stallOlderWindow(page, 800);

    await wheelToTopEdge(page);
    // The request is in flight: this is the row the reader will still want when the page lands.
    const before = await viewportAnchor(page);
    expect(before, 'no entry in the viewport to anchor on').not.toBeNull();
    const beforeIds = await renderedIds(page);

    await expect(async () => {
      expect((await renderedIds(page)).length).toBeGreaterThan(beforeIds.length);
    }).toPass({ timeout: 15000 });
    await waitForChatStill(page); // the arriving rows have finished moving the reader before their offset is read

    const after = await entryTop(page, before!.id);
    expect(after, 'the row the reader was reading is no longer rendered').not.toBeNull();
    expect(Math.abs(after! - before!.top), 'older rows slid the reader off their row').toBeLessThan(60);

    const afterIds = await renderedIds(page);
    expect(new Set(afterIds).size, 'a row on two pages is rendered twice').toBe(afterIds.length);
  });
});
