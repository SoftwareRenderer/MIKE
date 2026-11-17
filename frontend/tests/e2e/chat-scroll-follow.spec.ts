import { test, expect, type Disposable, type Page } from '@playwright/test';
import {
    isolatedProject, gotoProject, getProjectId, csrfFetch, navigateToTask, waitForChatFormReady,
    waitForSendButton, sendMessage, seedChatRows,
} from './helpers';

// Whether the reader is being followed is a browser question: the wheel, the pin frame and the scroll event
// land in an order only a real layout pass produces. Four things a headless unit render cannot answer, asked
// of one tall transcript and one recorder:
//
//   1. a position change no gesture explains is the reader leaving the bottom, and the app leaves them there;
//   2. the scroll keys move the transcript from a control beside the scroller, not only from inside it;
//   3. a reader who left the bottom while an answer streamed is left where they stopped, and
//   4. a reader still at the bottom keeps following what arrives, even while the app's own read for the rows
//      above is out (that read is the app asking, not the reader leaving).
//
// Gestures are deliberately mixed. A scrollbar gesture reaches the app as a position change with no travel
// behind it — measured in Chromium with a scrollbar it paints: a thumb drag is pointerdown, ten mouse moves,
// ZERO pointermove, twelve scroll events; a track click is pointerdown, no pointermove, ~30 scroll events of
// one animated page-up. So the takeover is judged from the position alone, the two numbers every browser
// reports for one (`scrollTop`, `scrollHeight`), which is what keeps the rule browser-independent. Headless
// Chromium paints no scrollbar, so the first test *writes* the position, which is what a thumb drag looks
// like to the app. The keyboard cases come from a control in the chat column that is not the scroller: the
// jump-latest button is a sibling of `.chat-messages`, so the tracker's own keydown listener never hears the
// key while `CardDetail`'s window listener is the one that scrolls.
//
// A reader travelling up by touch (a fling coasting) is `chat-scroll-jitter.spec.ts`, and the page-join
// itself is `chat-window-scroll.spec.ts`; the in-band notch among the gestures the tracker can see is
// `chat-follow.unit.svelte.ts`.
isolatedProject();

/** A page up has to be hundreds of pixels from the bottom, and one gesture has to stay clear of the band. */
const TALL_SEED_ROWS = 120;
/** Tall enough to scroll and to grow under the reader; the streaming tests never travel a page up. */
const SEED_ROWS = 40;
/** `SCROLL_BOTTOM_THRESHOLD`: how far above the bottom still counts as being at it. */
const BOTTOM_BAND = 32;
/** Pinned mock fixture with the mock's 3s warm-up: the turn is verifiably in flight before the reader is
 *  asked to leave the bottom, and the 16 chunks at 100ms that follow it are what arrives behind them. */
const STREAMING_PROMPT = 'slow-start llm-fixture:text_slow';

interface Write { t: number; to: number; height: number; view: number; by: string }

/**
 * Note every `scrollTop` write on the chat scroller, tagged by who made it, plus every scroll event, the
 * first wheel that asks for what is above, where each key landed and where focus went. A write through this
 * accessor is the harness's or the app's; a scroll event with no write behind it is the browser's own — its
 * scrollbar, a key, its anchoring — which is the distinction every assertion here is made of.
 */
async function installRecorder(page: Page): Promise<void> {
    await page.evaluate(() => {
        const rec: any = {
            writes: [], scrolls: 0, scrollsAtGesture: 0, gestureAt: 0, landedAt: 0,
            firstWheelUp: 0, lastKey: '', focusTrail: [] as string[], tag: 'app',
        };
        (window as any).__rec = rec;
        const list = document.querySelector('.chat-messages') as HTMLElement;
        const proto = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop')!;
        Object.defineProperty(list, 'scrollTop', {
            configurable: true,
            get() { return proto.get!.call(this); },
            set(v: number) {
                rec.writes.push({
                    t: performance.now(), to: Number(v), height: list.scrollHeight,
                    view: list.clientHeight, by: rec.tag,
                });
                proto.set!.call(this, v);
            },
        });
        list.addEventListener('scroll', () => { rec.scrolls++; }, { capture: true, passive: true });
        // The gesture's own clock. A gesture arrives some milliseconds after the harness decides to send
        // one, and until it lands the reader is still at the bottom, so a pin in that window is the app
        // following a reader who has not asked for anything yet.
        const landed = () => { if (rec.gestureAt && !rec.landedAt) rec.landedAt = performance.now(); };
        list.addEventListener('pointerdown', landed, { capture: true, passive: true });
        document.addEventListener('wheel', (e: WheelEvent) => {
            landed();
            if (e.deltaY < 0 && !rec.firstWheelUp) rec.firstWheelUp = performance.now();
        }, { capture: true, passive: true });
        document.addEventListener('keydown', landed, { capture: true });
        // Where a key actually landed: a scroll key only moves the transcript through the focus chain.
        const name = (n: EventTarget | null) => {
            const t = n as HTMLElement | null;
            return t ? `${t.tagName.toLowerCase()}.${String(t.className || '').split(' ')[0]}` : 'null';
        };
        document.addEventListener('keydown', (e: KeyboardEvent) => {
            rec.lastKey = `${e.key} on ${name(e.target)} (active ${name(document.activeElement)})`;
        }, { capture: true });
        document.addEventListener('focusin', (e: FocusEvent) => {
            rec.focusTrail.push(name(e.target));
        }, { capture: true });
    });
}

/** Declare the reader's gesture. What counts as a reply is measured from where it lands, not here. */
function beginGesture(page: Page): Promise<void> {
    return page.evaluate(() => {
        const rec = (window as any).__rec as any;
        rec.gestureAt = performance.now();
        rec.landedAt = 0;
        rec.scrollsAtGesture = rec.scrolls;
    });
}

/**
 * Let the browser report the move before the count is read. A `scroll` event is dispatched on a later
 * frame than the position change behind it, so a count read straight after the gesture can be the count
 * from before it. Best-effort: the assertion that follows says what broke.
 */
async function waitForScrollEvent(page: Page): Promise<void> {
    await page.waitForFunction(() => {
        const rec = (window as any).__rec as any;
        return rec.scrolls > rec.scrollsAtGesture;
    }, undefined, { timeout: 5000 }).catch(() => undefined);
}

interface Since { pins: Write[]; scrolls: number; lastKey: string; focusTrail: string[] }

/** What the app wrote, and what the browser moved, since the gesture began. */
function sinceGesture(page: Page): Promise<Since> {
    return page.evaluate(() => {
        const rec = (window as any).__rec as any;
        const writes = rec.writes as Write[];
        // Pins are counted from where the gesture landed, not from where it was declared; a harness write
        // is its own arrival, so with no input event the declaration is the moment.
        const from = rec.landedAt || rec.gestureAt;
        const after = writes.filter(w => w.t >= rec.gestureAt);
        return {
            // A write to the very bottom is the follow tracker pinning the reader back to it.
            pins: after.filter(w => w.by === 'app' && w.height - w.view - w.to <= 0 && w.t >= from),
            scrolls: rec.scrolls - rec.scrollsAtGesture,
            lastKey: rec.lastKey,
            focusTrail: [...(rec.focusTrail as string[])],
        };
    });
}

/** How far the viewport sits above the very bottom, and how tall the transcript is. */
function position(page: Page): Promise<{ fromBottom: number; scrollTop: number; viewport: number; height: number }> {
    return page.evaluate(() => {
        const el = document.querySelector('.chat-messages') as HTMLElement;
        return {
            fromBottom: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
            scrollTop: Math.round(el.scrollTop),
            viewport: el.clientHeight,
            height: el.scrollHeight,
        };
    });
}

/** Where the focus actually is — a scroll key only reaches the transcript through the focus chain. */
function activeElement(page: Page): Promise<string> {
    return page.evaluate(() => {
        const el = document.activeElement as HTMLElement | null;
        return el ? `${el.tagName.toLowerCase()}${el.className ? '.' + String(el.className).split(' ')[0] : ''}` : 'none';
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

/** Nothing may land above the reader during a measurement: an older page arriving is answered with the
 *  app's own settle, which writes `scrollTop` partway down the document. */
function holdOlderPages(page: Page): Promise<Disposable> {
    return page.route(url => url.searchParams.has('before'), () => { /* held for the rest of the test */ });
}

/** A task with a long transcript. It is written, never started, so the seed rows are its whole content
 *  until the spec's own send. */
async function createConversation(page: Page, label: string, rows: number): Promise<{ projectId: string; taskId: string }> {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
        method: 'POST',
        body: { title: `${label}-${Date.now()}`, prompt: `${label} fixture`, status: 'in_progress' },
    });
    expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
    const taskId: string = created.data.id;
    await seedChatRows(page, projectId, taskId, rows, label);
    return { projectId, taskId };
}

/** Open it and rest at the bottom of it. */
async function openAtBottom(page: Page, projectId: string, taskId: string): Promise<void> {
    await navigateToTask(page, taskId, projectId);
    await waitForChatFormReady(page);
    await expect(page.locator('.chat-messages [data-entry-id]').first()).toBeVisible({ timeout: 15000 });
    await waitForSendButton(page);
}

/**
 * Open a task with a tall transcript resting at the bottom, with nothing else moving under the measurement:
 * the page of older rows that fills a short chat is answered first, then held.
 */
async function openConversation(page: Page): Promise<void> {
    const { projectId, taskId } = await createConversation(page, 'takeover', TALL_SEED_ROWS);
    await openAtBottom(page, projectId, taskId);
    await page.waitForFunction(() => {
        const el = document.querySelector('.chat-messages') as HTMLElement;
        return el.scrollHeight - el.scrollTop - el.clientHeight < 4;
    }, undefined, { timeout: 15000 });
    await holdOlderPages(page);
    await installRecorder(page);
}

/**
 * Where the reader ended up once every frame the takeover could provoke has run. An immediate read proves
 * nothing: a pin answers the scroll event within one round-trip, so the position that matters is the one
 * that survives the settle.
 */
async function settledPosition(page: Page) {
    let prev = await position(page);
    for (let i = 0; i < 14; i++) {
        await page.waitForTimeout(150);
        const now = await position(page);
        if (now.scrollTop === prev.scrollTop && now.height === prev.height) return now;
        prev = now;
    }
    return prev;
}

/** The reader left the bottom, and nothing in the app put them back. */
async function expectLeftAlone(page: Page, note: string): Promise<void> {
    const end = await settledPosition(page);
    const since = await sinceGesture(page);
    expect(
        since.pins,
        `${note}: the app pinned the reader back to the bottom ${since.pins.length}× `
        + `(${JSON.stringify(since.pins)}; they ended ${end.fromBottom}px above the bottom of a `
        + `${end.height}px transcript after ${since.scrolls} scroll events)`,
    ).toHaveLength(0);
    expect(
        end.fromBottom,
        `${note}: the reader ended ${end.fromBottom}px above the bottom of a ${end.height}px transcript, `
        + `inside the ${BOTTOM_BAND}px band that counts as at it (${since.scrolls} scroll events, `
        + `writes ${JSON.stringify(since.pins)})`,
    ).toBeGreaterThan(BOTTOM_BAND);
}

/** Move the viewport the way the scrollbar does: the position changes, no gesture listener hears it. */
async function moveViewport(page: Page, delta: number): Promise<void> {
    await page.evaluate((d) => {
        const rec = (window as any).__rec as any;
        const el = document.querySelector('.chat-messages') as HTMLElement;
        rec.tag = 'harness';
        try {
            // A harness write is the gesture's arrival: a reader's scrollbar is a position change and
            // nothing else, so this is what one looks like.
            if (!rec.landedAt) rec.landedAt = performance.now();
            el.scrollTop = el.scrollTop + d;
        } finally { rec.tag = 'app'; }
    }, delta);
}

/** The middle of the chat scroller, measured before the answer starts arriving. The pane's box does not move
 *  while its content grows inside it, and a coordinate read after the send is a round trip taken out of the
 *  stream the test is trying to interrupt. */
async function scrollerCentre(page: Page): Promise<{ x: number; y: number }> {
    const box = await page.locator('.chat-messages').boundingBox();
    if (!box) throw new Error('the chat scroller has no box to scroll');
    return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

test.describe('Gestures that ask for what is above', () => {
    test('a position change no gesture explains is the reader leaving the bottom', async ({ page }) => {
        await openConversation(page);
        const before = await position(page);
        expect(before.fromBottom, 'the chat did not open at the bottom').toBeLessThanOrEqual(BOTTOM_BAND);

        await beginGesture(page);
        await moveViewport(page, -2 * before.viewport);
        await waitForScrollEvent(page);
        const since = await sinceGesture(page);
        expect(since.scrolls, 'the write did not even move the document').toBeGreaterThan(0);
        await expectLeftAlone(page, 'a scrollbar-shaped position change');
        // Still up where the reader stopped, not merely somewhere above the bottom: entries realize their
        // real heights as the reader travels, so the exact offset is the layout's, the direction is the
        // reader's.
        const end = await settledPosition(page);
        expect(end.scrollTop, 'the reader drifted back down toward the bottom').toBeLessThan(before.scrollTop);
    });

    test('the scroll keys move the transcript from anywhere in the chat column', async ({ page }) => {
        await openConversation(page);
        // A reader who clicked in the transcript: that click is what puts focus inside the chat column,
        // and where it lands is the browser's — the transcript is not a tab stop, so the keys cannot be
        // left to the focus chain. The click is at the top-left, where the sticky header lets it through.
        await page.locator('.chat-messages').click({ position: { x: 16, y: 24 } });
        const focused = await activeElement(page);

        const step = async (key: string, from: number) => {
            await beginGesture(page);
            await page.keyboard.press(key);
            const to = (await settledPosition(page)).scrollTop;
            const since = await sinceGesture(page);
            expect(
                to,
                `${key} left the transcript at ${from} with ${focused} focused: the key went to `
                + `${since.lastKey || 'nothing'}, focus trail ${JSON.stringify(since.focusTrail)}, `
                + `${since.scrolls} scroll events, writes ${JSON.stringify(since.pins)}`,
            ).toBeLessThan(from);
            await expectLeftAlone(page, `${key} with ${focused} focused`);
            return to;
        };

        const paged = await step('PageUp', (await settledPosition(page)).scrollTop);
        const arrowed = await step('ArrowUp', paged);

        // And from a control beside the scroller — the jump-latest button, whose keydown never reaches
        // the tracker's own keydown listener at all.
        const jump = page.locator('.chat-jump-latest');
        await expect(jump, 'leaving the bottom did not offer the way back to it').toBeVisible();
        await jump.focus();
        await step('PageUp', arrowed);
    });
});

test.describe('Following a streamed answer', () => {
    test('scrolling up during a response is not undone when that response completes', async ({ page }) => {
        test.setTimeout(180000);
        // The ordering this locks out is a race across one frame, and a fast machine wins it every time.
        // Throttled, the settle loop's frames stretch out and the race is reachable.
        const cdp = await page.context().newCDPSession(page);
        await cdp.send('Emulation.setCPUThrottlingRate', { rate: 8 });
        const { projectId, taskId } = await createConversation(page, 'stream-follow', SEED_ROWS);
        await openAtBottom(page, projectId, taskId);
        // Nothing may land above the reader during the measurement: an older page arriving is answered
        // with its own settle, which writes scrollTop.
        await holdOlderPages(page);
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
        await expect(page.locator('.awaiting-llm'), 'the turn is in flight').toBeVisible({ timeout: 60000 });

        // The reader leaves the bottom while tokens are arriving. The wheel follows the pointer, not the
        // selector, so the pointer goes where the recorder measured the scroller; three strokes of -300 clear the
        // 300px this measures without costing the stream nine more round trips.
        await page.mouse.move(centre.x, centre.y);
        for (let i = 0; i < 3; i++) await page.mouse.wheel(0, -300);
        const left = await position(page);

        // The answer finishes, with the last layout passes it draws (the stats line, the review bar). Four
        // workers queue turns on one server, so this is a turn budget, not a DOM budget.
        await waitForSendButton(page, 90000);
        await page.waitForTimeout(1500);
        const end = await position(page);
        const rec = await page.evaluate(() => JSON.parse(JSON.stringify((window as any).__rec))) as any;
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
        const leftHeight = away.reduce((m, s) => Math.min(m, s.height), Number.MAX_SAFE_INTEGER);
        const tallest = away.reduce((m, s) => Math.max(m, s.height), 0);
        expect(
            tallest,
            `the transcript stayed ${tallest}px tall after the wheel, so the answer arrived before the reader `
            + `left the bottom of a ${leftHeight}px window`,
        ).toBeGreaterThan(leftHeight);

        // A write at the very bottom is the follow tracker pinning the reader; a page of older rows is
        // answered with a write partway down the document.
        //
        // Only a pin carrying a taller document than the one the reader left is the rule this measures. The
        // send's own pin (`pin(true)`, `use-chat-follow.svelte.ts`) re-engages following and writes across
        // several settle frames, so a wheel landing inside that sequence is answered by a write of the height
        // the reader left with — the answer had grown nothing yet. A pin made by content arriving is made
        // after that content, and its height cannot hide.
        const snaps = (rec.writes as Write[])
            .filter(w => w.t >= rec.firstWheelUp && w.height > leftHeight + 40)
            .map(w => ({ ...w, fromBottom: Math.round(w.height - w.view - w.to) }))
            .filter(w => w.fromBottom <= 0);

        expect(
            snaps,
            `the app pinned the reader to the bottom ${snaps.length}× after they wheeled up mid-response `
            + `(they were ${left.fromBottom}px above the bottom, and ended ${end.fromBottom}px above the `
            + `bottom of a ${end.height}px transcript; writes ${JSON.stringify(snaps)})`,
        ).toHaveLength(0);
        expect(
            end.fromBottom,
            `the reader ended ${end.fromBottom}px above the bottom of a ${end.height}px transcript, `
            + `having wheeled to ${left.fromBottom}px above it`,
        ).toBeGreaterThan(300);
    });

    test('the app asking for the rows above does not stop it following what arrives', async ({ page }) => {
        test.setTimeout(180000);
        await page.setViewportSize({ width: 1280, height: 900 });
        const { projectId, taskId } = await createConversation(page, 'page-arrival', SEED_ROWS);

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
            `the watching tab drifted ${worst}px above the bottom of a ${end.height}px transcript while `
            + `an answer arrived (${samples.length} samples, ending ${end.fromBottom}px above the bottom)`,
        ).toBeLessThan(300);
        expect(end.fromBottom, `the watching tab ended ${end.fromBottom}px above the bottom`).toBeLessThan(40);

        await worker.close();
    });
});
