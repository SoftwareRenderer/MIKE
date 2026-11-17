import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, gotoProject, getProjectId, csrfFetch, navigateToTask, waitForChatFormReady,
  waitForSendButton, sendMessage, seedChatRows,
} from './helpers';

// Whether the reader is being followed is a browser question: the wheel, the pin frame and the scroll event
// land in an order only a real layout pass produces. The rule it measures — new content re-pins the reader
// only while the reader is being followed — has two halves, and both are asked of one tall transcript:
//
//   1. a reader who left the bottom while an answer streamed is left where they stopped, and
//   2. a reader still at the bottom keeps following what arrives, even while the app's own read for the rows
//      above is out (that read is the app asking, not the reader leaving).
//
// A reader travelling up is `chat-scroll-jitter.spec.ts` (touch) and `chat-scroll-takeover.spec.ts`
// (a position change and the keys); the page-join itself is `chat-window-scroll.spec.ts`.
isolatedProject();

const SEED_ROWS = 40;
/** Pinned mock fixture with the mock's 3s warm-up: the turn is verifiably in flight before the reader is
 *  asked to leave the bottom, and the 16 chunks at 100ms that follow it are what arrives behind them. */
const STREAMING_PROMPT = 'slow-start llm-fixture:text_slow';

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

interface Sample {
  t: number;
  fromBottom: number;
  height: number;
  /** Whether the turn still held the stop button. "Streaming" is observable as the stop button up and the
   *  awaiting indicator gone, and neither of them survives the turn to be read afterwards. */
  stop: boolean;
}

/** Sample how far the viewport sits above the bottom every 50ms until stopped. */
async function startSampler(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as any;
    w.__samples = [] as Sample[];
    w.__stop = () => clearInterval(w.__id);
    w.__id = setInterval(() => {
      const el = document.querySelector('.chat-messages') as HTMLElement | null;
      if (!el) return;
      w.__samples.push({
        t: performance.now(),
        fromBottom: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
        height: el.scrollHeight,
        stop: !!document.querySelector('.btn-stop'),
      });
    }, 50);
  });
}

async function stopSampler(page: Page): Promise<Sample[]> {
  return page.evaluate(() => {
    const w = window as any;
    w.__stop();
    return w.__samples;
  });
}

/** A task with a long transcript. It is written, never started, so the seed rows are its whole content until
 *  the spec's own send. */
async function createConversation(page: Page, label: string): Promise<{ projectId: string; taskId: string }> {
  await gotoProject(page);
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: { title: `${label}-${Date.now()}`, prompt: `${label} fixture`, status: 'in_progress' },
  });
  expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
  const taskId: string = created.data.id;
  await seedChatRows(page, projectId, taskId, SEED_ROWS, label);
  return { projectId, taskId };
}

/** Open it and rest at the bottom of it. */
async function openAtBottom(page: Page, projectId: string, taskId: string): Promise<void> {
  await navigateToTask(page, taskId, projectId);
  await waitForChatFormReady(page);
  await expect(page.locator('.chat-messages [data-entry-id]').first()).toBeVisible({ timeout: 15000 });
  await waitForSendButton(page);
}

/** The middle of the chat scroller, measured before the answer starts arriving. The pane's box does not move
 *  while its content grows inside it, and a coordinate read after the send is a round trip taken out of the
 *  stream the test is trying to interrupt. */
async function scrollerCentre(page: Page): Promise<{ x: number; y: number }> {
  const box = await page.locator('.chat-messages').boundingBox();
  if (!box) throw new Error('the chat scroller has no box to scroll');
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

test.describe('Following a streamed answer', () => {
  test('scrolling up during a response is not undone when that response completes', async ({ page }) => {
    test.setTimeout(120000);
    // The ordering this locks out is a race across one frame, and a fast machine wins it every time.
    // Throttled, the settle loop's frames stretch out and the race is reachable.
    const cdp = await page.context().newCDPSession(page);
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 8 });
    const { projectId, taskId } = await createConversation(page, 'stream-follow');
    await openAtBottom(page, projectId, taskId);
    // Nothing may land above the reader during the measurement: an older page arriving is answered
    // with its own settle, which writes scrollTop.
    await page.route(url => url.searchParams.has('before'), () => { /* held for the whole test */ });
    await installRecorder(page);
    await startSampler(page);

    // Type and send, and keep the round trips between the keystroke and the wheel to the one this needs.
    // The pinned answer is ~1.6s of wall time (16 chunks at 100ms) and every assertion spent before the
    // wheel takes a slice of it: a live wait for the stop button was once asked after the turn had already
    // finished. Whether the answer was still arriving when the reader left is read out of the recording
    // below, which the sampler started before the send.
    const typed = `stream-follow ${Date.now()} ${STREAMING_PROMPT}`;
    const centre = await scrollerCentre(page);
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(typed);
    await chatTextarea.press('Enter');

    // The send draws the reader's own message and pins them to the bottom; the wheel has to come after that
    // pin lands, or the pin lands over it and reads as the answer re-pinning a reader who left. The indicator
    // is "request sent, nothing back yet", and `slow-start` holds the first chunk for 3s, so this is a
    // window in seconds rather than the milliseconds a 1ms fixture would leave.
    await expect(page.locator('.awaiting-llm'), 'the turn is in flight').toBeVisible({ timeout: 20000 });

    // The reader leaves the bottom while tokens are arriving. The wheel follows the pointer, not the
    // selector, so the pointer goes where the recorder measured the scroller; three strokes of -300 clear the
    // 300px this measures without costing the stream nine more round trips.
    await page.mouse.move(centre.x, centre.y);
    for (let i = 0; i < 3; i++) await page.mouse.wheel(0, -300);
    const left = await position(page);

    // The answer finishes, with the last layout passes it draws (the stats line, the review bar).
    await waitForSendButton(page, 30000);
    await page.waitForTimeout(1500);
    const end = await position(page);
    const rec = await page.evaluate(() => JSON.parse(JSON.stringify((window as any).__rec))) as Rec;
    const samples = await stopSampler(page);
    await expect(
      page.locator('.chat-messages .chat-message.user', { hasText: typed }).first(),
      'the message this typed reached the chat',
    ).toBeVisible();
    expect(rec.firstWheelUp, 'the wheel never reached the chat').toBeGreaterThan(0);

    // The premise, asserted. The reader was away from the bottom while the turn still held the stop button,
    // and the transcript grew under them; without this the spec would measure a turn that was already over.
    const away = samples.filter(s => s.t >= rec.firstWheelUp);
    expect(
      away.some(s => s.stop),
      `the answer was no longer arriving when the reader left the bottom (${samples.length} samples, `
      + `${away.length} after the wheel, ${away.filter(s => s.stop).length} of them with the stop button up)`,
    ).toBe(true);
    const tallest = away.reduce((m, s) => Math.max(m, s.height), 0);
    expect(
      tallest,
      `the transcript stayed ${tallest}px tall after the wheel, so the answer arrived before the reader `
      + `left the bottom of a ${left.scrollHeight}px window`,
    ).toBeGreaterThan(left.scrollHeight);

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

  test('the app asking for the rows above does not stop it following what arrives', async ({ page }) => {
    test.setTimeout(180000);
    await page.setViewportSize({ width: 1280, height: 900 });
    const { projectId, taskId } = await createConversation(page, 'page-arrival');

    // Hold the page of older rows: the reader waits in the state that read put them in, with the
    // answer still arriving.
    let paged = false;
    await page.route(url => url.searchParams.has('before'), () => { paged = true; });
    await openAtBottom(page, projectId, taskId);
    // The newest window is shorter than the chat, so the app went looking for the rows above it.
    await expect.poll(() => paged, 'the app asked for a page of older rows to fill the chat').toBe(true);
    await page.waitForTimeout(600);

    // A second tab works the task; this one only watches, so nothing here pins itself.
    const worker = await page.context().newPage();
    await navigateToTask(worker, taskId, projectId);
    await waitForChatFormReady(worker);
    await waitForSendButton(worker);
    await startSampler(page);
    const turn = `answered-${Date.now()} ${STREAMING_PROMPT}`;
    await sendMessage(worker, turn);
    await waitForSendButton(worker, 60000);
    await waitForSendButton(page, 60000);
    await page.waitForTimeout(800);
    const samples = await stopSampler(page);
    const end = await position(page);
    const worst = samples.reduce((m, s) => Math.max(m, s.fromBottom), 0);
    // The premise, asserted from this tab's own samples: the turn it watched really ran here, whatever a
    // starved render did with the stop button on this tab.
    expect(
      samples.some(s => s.stop),
      `the watching tab never held a running turn, so there was nothing for it to follow `
      + `(${samples.length} samples)`,
    ).toBe(true);

    await expect(page.locator('.chat-messages .chat-message.assistant').last(), 'the watching tab rendered the answer')
      .toContainText(/[a-z]/i);

    expect(
      worst,
      `the watching tab drifted ${worst}px above the bottom of a ${end.scrollHeight}px transcript while `
      + `an answer arrived (${samples.length} samples, ending ${end.fromBottom}px above the bottom)`,
    ).toBeLessThan(300);
    expect(end.fromBottom, `the watching tab ended ${end.fromBottom}px above the bottom`).toBeLessThan(40);

    await worker.close();
  });
});
