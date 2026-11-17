import { test, expect, type Page } from '@playwright/test';
import { atTopOfList } from '../../src/lib/utils/chat-window';
import {
  isolatedProject, gotoProject, getProjectId, csrfFetch, navigateToTask, waitForChatFormReady,
  seedChatRows,
} from './helpers';

// A task opens on the newest window and older rows arrive a page at a time. Scrolling is the only
// affordance for that — nothing is rendered asking the reader to click. Whether a page pushed the reader
// down the screen is a layout question, so it is measured here; the reducer behind it is unit-tested in
// `src/lib/utils/__tests__/chat-window.unit.ts`.
isolatedProject();

const SEED_ROWS = 200;

/** Where the reader is: the first entry with pixels below the top edge of the scroll viewport. */
function viewportAnchor(page: Page): Promise<{ id: string; top: number } | null> {
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
function entryTop(page: Page, id: string): Promise<number | null> {
  return page.evaluate((entryId) => {
    const list = document.querySelector('.chat-messages') as HTMLElement;
    const node = list.querySelector<HTMLElement>(`.chat-entry[data-entry-id="${entryId}"]`);
    return node ? node.getBoundingClientRect().top - list.getBoundingClientRect().top : null;
  }, id);
}

/** The ids rendered, oldest first — a repeat is one row held by two pages. */
function renderedIds(page: Page): Promise<string[]> {
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
async function parkOnList(page: Page): Promise<void> {
  const box = await page.locator('.chat-messages').boundingBox();
  expect(box, 'no chat list to scroll').not.toBeNull();
  await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);
}

/** Where the reader is in the list and how tall their view of it is, as the app's own edge rule needs. */
function listPosition(page: Page): Promise<{ scrollTop: number; viewportHeight: number }> {
  return page.evaluate(() => {
    const el = document.querySelector('.chat-messages') as HTMLElement;
    return { scrollTop: el.scrollTop, viewportHeight: el.clientHeight };
  });
}

/**
 * The edge older rows are fetched from, however many notches the realized heights take. Scrolling is the way
 * a reader does it: writing `scrollTop` is not a gesture, and the follow tracker reads an unexplained move as
 * the document shifting under the reader and pins it back to the bottom, so the reader never reaches the edge.
 */
async function wheelToTopEdge(page: Page): Promise<void> {
  await parkOnList(page);
  let at = await listPosition(page);
  for (let i = 0; i < 60 && !atTopOfList(at.scrollTop, at.viewportHeight); i++) {
    await page.mouse.wheel(0, -WHEEL_NOTCH);
    await page.waitForTimeout(40);
    at = await listPosition(page);
  }
  expect(atTopOfList(at.scrollTop, at.viewportHeight),
    `the wheel never reached the edge older rows are fetched from (scrollTop ${at.scrollTop} of a `
    + `${at.viewportHeight}-pixel view)`).toBe(true);
}

/** The reader has stopped travelling: three reads a beat apart see the same row under the same offset.
 *  The anchoring loop holds a row in place only while nobody is still driving (`readerScrolledUp`,
 *  `src/lib/utils/chat-window.ts`), so a page released while the last notch is still coasting is answered
 *  with the reader left at the edge — the app's own rule, not a slide this spec is about. */
async function settledAnchor(page: Page): Promise<{ id: string; top: number } | null> {
  let previous = await viewportAnchor(page);
  let repeats = 0;
  await expect.poll(async () => {
    const now = await viewportAnchor(page);
    const still = !!now && !!previous && now.id === previous.id && now.top === previous.top;
    repeats = still ? repeats + 1 : 0;
    previous = now;
    return repeats;
  }, { timeout: 10000, intervals: [250] }).toBeGreaterThanOrEqual(2);
  return previous;
}

/**
 * Hold the first older window in flight until the test releases it, and every page after it for the rest of
 * the test. The reader's position has to be read while the first page is still out — a timed stall is a
 * guess at how long the round trip takes — and a second page landing mid-measurement is a whole page of
 * slide that has nothing to do with the one this reads.
 */
function olderWindowGate(): { arm: (page: Page) => Promise<void>; release: () => void } {
  let asked = false;
  let releaseFirst = () => {};
  return {
    arm: async (page: Page) => {
      await page.route(url => url.searchParams.has('before'), async route => {
        if (asked) return new Promise<void>(() => { /* held for the rest of the test */ });
        asked = true;
        await new Promise<void>(resolve => { releaseFirst = resolve; });
        await route.continue();
      });
    },
    release: () => releaseFirst(),
  };
}

/** The reader's row has stopped moving: three reads a beat apart see the same offset. Entries realize their
 *  heights over several frames, and an offset sampled in the middle of that is not where the reader is. */
async function settledEntryTop(page: Page, id: string): Promise<number | null> {
  let previous = await entryTop(page, id);
  let repeats = 0;
  await expect.poll(async () => {
    const now = await entryTop(page, id);
    repeats = now === previous ? repeats + 1 : 0;
    previous = now;
    return repeats;
  }, { timeout: 10000, intervals: [250] }).toBeGreaterThanOrEqual(2);
  return previous;
}

async function openConversation(page: Page): Promise<{ projectId: string; taskId: string }> {
  await gotoProject(page);
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: { title: `windowed-${Date.now()}`, prompt: 'windowed history fixture', status: 'in_progress' },
  });
  expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
  const taskId: string = created.data.id;
  await seedChatRows(page, projectId, taskId, SEED_ROWS, 'windowed');
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
    const gate = olderWindowGate();
    await gate.arm(page);

    await wheelToTopEdge(page);
    // Come back down inside the rows the reader has reached. The edge is where the fetch is asked for, not
    // where a reader reads: parked at `scrollTop` 0 the anchor competes with the browser clamping the
    // viewport there, and the state this measures — a reader on a row of their own — is the one below it.
    await parkOnList(page);
    await page.mouse.wheel(0, 400);
    // The first page is in the gate's hands: this is the row the reader will still want when it lands, read
    // only once the reader has come to rest.
    const before = await settledAnchor(page);
    expect(before, 'no entry in the viewport to anchor on').not.toBeNull();
    const beforeIds = await renderedIds(page);
    gate.release();

    await expect(async () => {
      expect((await renderedIds(page)).length).toBeGreaterThan(beforeIds.length);
    }).toPass({ timeout: 15000 });

    const after = await settledEntryTop(page, before!.id);
    expect(after, 'the row the reader was reading is no longer rendered').not.toBeNull();
    expect(Math.abs(after! - before!.top), 'older rows slid the reader off their row').toBeLessThan(60);

    const afterIds = await renderedIds(page);
    expect(new Set(afterIds).size, 'a row on two pages is rendered twice').toBe(afterIds.length);
  });
});
