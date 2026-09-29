import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, gotoProject, getProjectId, csrfFetch, navigateToTask, waitForChatFormReady,
  waitForSendButton, sendMessage, waitForChatGrowth, waitForChatStill,
} from './helpers';

// Whether a reader who scrolled away mid-answer is left where they stopped is a browser question: the
// wheel, the pin frame and the scroll event land in an order only a real layout pass produces.
// `chat-scroll-jitter.spec.ts` covers a reader travelling up; this is the answer finishing under one.
isolatedProject();

const SEED_ROWS = 40;
/** Pinned mock fixture: 16 chunks at 100ms, so the turn streams and ends without any tool call. */
const STREAMING_PROMPT = 'llm-fixture:text_slow';

interface Rec {
  writes: Array<{ t: number; to: number; height: number; view: number }>;
  firstWheelUp: number;
}

/** Note every `scrollTop` write on the chat scroller and the first wheel that asks for what is above. */
async function installRecorder(page: Page): Promise<void> {
  await page.evaluate(() => {
    const rec: any = { writes: [], firstWheelUp: 0 };
    (window as any).__rec = rec;
    const list = document.querySelector('.chat-messages') as HTMLElement;
    const proto = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop')!;
    Object.defineProperty(list, 'scrollTop', {
      configurable: true,
      get() { return proto.get!.call(this); },
      set(v: number) {
        rec.writes.push({
          t: performance.now(), to: Number(v), height: list.scrollHeight, view: list.clientHeight,
        });
        proto.set!.call(this, v);
      },
    });
    list.addEventListener('wheel', (e: WheelEvent) => {
      if (e.deltaY < 0 && !rec.firstWheelUp) rec.firstWheelUp = performance.now();
    }, { capture: true });
  });
}

/** Seed a conversation so “up here” and “at the bottom” are hundreds of pixels apart. */
async function seedConversation(page: Page, projectId: string, taskId: string, rows: number): Promise<void> {
  const csrf = await page.evaluate(async () => (await (await fetch('/api/auth/session')).json()).csrf_token || '');
  const seeded = await page.evaluate(async ({ url, count, token }) => {
    let made = 0;
    for (let i = 0; i < count; i++) {
      const res = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(token ? { 'X-CSRF-Token': token } : {}) },
        credentials: 'include',
        body: JSON.stringify({ role: 'user', content: `seed row ${i}\n${'filler text that gives the entry some height '.repeat(3)}` }),
      });
      if (!res.ok) return { made, status: res.status, text: await res.text() };
      made++;
    }
    return { made, status: 200, text: '' };
  }, { url: `/api/projects/${projectId}/tasks/${taskId}/messages`, count: rows, token: csrf });
  expect(seeded.status, `seeding stopped at row ${seeded.made}: ${seeded.text}`).toBe(200);
}

/** How far the viewport sits above the very bottom, and how tall the transcript is. */
function position(page: Page): Promise<{ fromBottom: number; scrollTop: number; scrollHeight: number }> {
  return page.evaluate(() => {
    const el = document.querySelector('.chat-messages') as HTMLElement;
    return {
      fromBottom: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
      scrollTop: Math.round(el.scrollTop),
      scrollHeight: el.scrollHeight,
    };
  });
}

/** Open a task with a tall transcript, resting at the bottom of it. */
async function openConversation(page: Page): Promise<{ projectId: string; taskId: string }> {
  await gotoProject(page);
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: { title: `stream-follow-${Date.now()}`, prompt: 'stream follow fixture', status: 'in_progress' },
  });
  expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
  const taskId: string = created.data.id;
  await seedConversation(page, projectId, taskId, SEED_ROWS);
  await navigateToTask(page, taskId, projectId);
  await waitForChatFormReady(page);
  await expect(page.locator('.chat-messages [data-entry-id]').first()).toBeVisible({ timeout: 15000 });
  await waitForSendButton(page);
  // Let the opening pin finish, so the gesture is not measured against it.
  await waitForChatStill(page);
  return { projectId, taskId };
}

test.describe('Following a streamed answer', () => {
  test('scrolling up during a response is not undone when that response completes', async ({ page }) => {
    test.setTimeout(120000);
    // The ordering this locks out is a race across one frame, and a fast machine wins it every time.
    // Throttled, the settle loop's frames stretch out and the race is reachable.
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 8 });
    await openConversation(page);
    // Nothing may land above the reader during the measurement: an older page arriving is answered
    // with its own settle, which writes scrollTop.
    await page.route(url => url.searchParams.has('before'), () => { /* held for the whole test */ });
    await installRecorder(page);

    // Send, then wait for the answer to be actually streaming: the send button is visible before the
    // turn starts and again after it ends, so it cannot mark the middle.
    await sendMessage(page, `stream-follow ${Date.now()} ${STREAMING_PROMPT}`);
    await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 20000 });
    await waitForChatGrowth(page); // tokens are arriving: the wheel has to land mid-stream, not before the first chunk

    // The reader leaves the bottom while tokens are arriving.
    const box = await page.locator('.chat-messages').boundingBox();
    expect(box, 'no chat list to scroll').not.toBeNull();
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);
    for (let i = 0; i < 8; i++) await page.mouse.wheel(0, -200);
    const left = await position(page);

    // The answer finishes, with the last layout passes it draws (the stats line, the review bar).
    await waitForSendButton(page, 30000);
    await waitForChatStill(page); // the last layout passes have landed; a pin that arrives later is still counted
    const end = await position(page);
    const rec = await page.evaluate(() => JSON.parse(JSON.stringify((window as any).__rec))) as Rec;
    expect(rec.firstWheelUp, 'the wheel never reached the chat').toBeGreaterThan(0);
    // A write at the very bottom is the follow tracker pinning the reader; a page of older rows is
    // answered with a write partway down the document.
    const snaps = rec.writes
      .filter(w => w.t >= rec.firstWheelUp)
      .map(w => ({ ...w, fromBottom: Math.round(w.height - w.view - w.to) }))
      .filter(w => w.fromBottom <= 0);

    expect(
      snaps,
      `the app pinned the reader to the bottom ${snaps.length}× after they wheeled up mid-response `
      + `(they were ${left.fromBottom}px above the bottom, and ended ${end.fromBottom}px above the `
      + `bottom of a ${end.scrollHeight}px transcript; writes ${JSON.stringify(snaps)})`,
    ).toHaveLength(0);
    expect(
      end.fromBottom,
      `the reader ended ${end.fromBottom}px above the bottom of a ${end.scrollHeight}px transcript, `
      + `having scrolled to ${left.fromBottom}px above it`,
    ).toBeGreaterThan(300);
  });
});
