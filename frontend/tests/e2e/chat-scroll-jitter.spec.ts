import { test, expect, type Page } from '@playwright/test';
import { chatFixture, navigateToTask, waitForChatFormReady } from './helpers';

// What a reader feels as jitter while scrolling up is one measurable thing: the viewport moving in a
// direction nobody asked for. jsdom has no layout and no kinetic scroll, so this is a browser question.
//
// The recorder tags every `scrollTop` write: a write through the accessor is the app's, a position change
// without one is the browser's (its kinetic scroll or its scroll anchoring).
//
// The gesture is scripted rather than synthesized: `Input.synthesizeScrollGesture` moves this list 0px in
// headless Chromium, so the coast is a decaying ramp driven from the page — one `touchmove` to declare intent,
// then no further input events, which is what a fling looks like to a scroll listener.
//
// Two rules keep the numbers meaning the same thing on a busy machine as on an idle one: a measurement window
// is counted in frames rather than milliseconds, because a frame is one opportunity for the app to move the
// viewport; and the transcript comes from `chatFixture()`, built by global setup, not from POSTs in a test.
//
// The anchor arithmetic (`resolveAnchor`, `anchorScrollDelta`) is unit-tested in
// `src/lib/utils/__tests__/chat-window.unit.ts`; `chat-window-scroll.spec.ts` answers the wheel case.

// A phone: touch input, and a viewport short enough that many entries cross the `content-visibility`
// threshold per frame, which is where a reader notices layout cost.
test.use({ viewport: { width: 412, height: 732 }, deviceScaleFactor: 2, hasTouch: true, isMobile: true });

/** Frames measured after a page lands. 60 is about a second at 60 Hz and two when the machine is busy. */
const LANDING_WINDOW_FRAMES = 60;
/** Below this the window proved nothing, and the run says so rather than reporting a quiet number. */
const MIN_WINDOW_FRAMES = 20;
/** Frames of no app write that mean the pin settle is over and a reader's gesture can be judged. */
const QUIESCENT_FRAMES = 3;
/** The ramp asks for older rows inside ~32 frames on an idle machine; this is the generous ceiling. */
const MAX_FRAMES_TO_ASK = 400;

interface ScrollRecord {
    writes: Array<{ t: number; from: number; to: number; by: string }>;
    frames: Array<{ t: number; top: number; anchorTop: number | null; h: number }>;
    /** Every childList change of the transcript, so a landing is the first one after a given moment. */
    mutations: number[];
    /** When this spec let the held older page through, in the page's own clock. */
    releasedAt: number;
    scrollEvents: number;
    rectReads: number;
    scans: number;
    fetchAt: number;
    coastStartedAt: number;
}

/**
 * Instrument the chat scroller: tag `scrollTop` writes by who made them (`app`, or `harness` for this spec's
 * own gesture), sample the position once per frame, and count the transcript's measurements, scroll events and
 * DOM changes next to the moment this spec released the held older page.
 *
 * `__followRow(id)` starts sampling one entry's offset from the top edge of the viewport, which is what the
 * reader calls jitter.
 */
async function installRecorder(page: Page): Promise<void> {
    await page.evaluate(() => {
        const rec = {
            writes: [] as Array<{ t: number; from: number; to: number; by: string }>,
            frames: [] as Array<{ t: number; top: number; anchorTop: number | null; h: number }>,
            mutations: [] as number[],
            releasedAt: 0, landedFrame: 0, scrollEvents: 0, rectReads: 0, scans: 0, fetchAt: 0, coastStartedAt: 0, coastFrame: 0,
            tag: 'app', anchorId: '',
        };
        (window as any).__rec = rec;
        (window as any).__followRow = (id: string) => { rec.anchorId = id; };

        const proto = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollTop')!;
        const list = document.querySelector('.chat-messages') as HTMLElement;
        Object.defineProperty(list, 'scrollTop', {
            configurable: true,
            get() { return proto.get!.call(this); },
            set(v: number) {
                rec.writes.push({ t: performance.now(), from: proto.get!.call(this), to: Number(v), by: rec.tag });
                proto.set!.call(this, v);
            },
        });

        const top = () => proto.get!.call(list);
        // `offsetTop`, not a client rect: this file counts rects, and those must stay the app's cost.
        const anchorTop = () => {
            if (!rec.anchorId) return null;
            const node = list.querySelector<HTMLElement>(`[data-entry-id="${rec.anchorId}"]`);
            return node ? node.offsetTop - top() : null;
        };
        rec.frames.push({ t: performance.now(), top: top(), anchorTop: anchorTop(), h: list.scrollHeight });
        const sample = () => {
            rec.frames.push({ t: performance.now(), top: top(), anchorTop: anchorTop(), h: list.scrollHeight });
            requestAnimationFrame(sample);
        };
        requestAnimationFrame(sample);

        const rect = Element.prototype.getBoundingClientRect;
        Element.prototype.getBoundingClientRect = function (this: Element) {
            rec.rectReads++;
            return rect.call(this);
        };
        const qa = Element.prototype.querySelectorAll;
        Element.prototype.querySelectorAll = function <S extends string>(this: Element, selectors: S) {
            // The sticky header's transcript scan is what this counts.
            if (selectors === '.sticky-user-message') rec.scans++;
            return qa.call(this, selectors as any) as any;
        };
        list.addEventListener('scroll', () => { rec.scrollEvents++; });

        new MutationObserver(() => {
            const now = performance.now();
            rec.mutations.push(now);
            if (!rec.landedFrame && rec.releasedAt && now >= rec.releasedAt) rec.landedFrame = rec.frames.length;
        }).observe(document.querySelector('.chat-content')!, { childList: true });

        const real = window.fetch;
        (window as any).fetch = (input: any, init?: any) => {
            if (typeof input === 'string' && input.includes('before=')) rec.fetchAt = performance.now();
            return real(input as any, init as any);
        };
    });
}

async function readRecord(page: Page): Promise<ScrollRecord> {
    return page.evaluate(() => JSON.parse(JSON.stringify((window as any).__rec)));
}

/**
 * When the released page entered the transcript: the first change after the release. A change before it is not
 * the reader's landing.
 */
function landingTime(rec: ScrollRecord): number {
    return rec.mutations.find(t => t >= rec.releasedAt) ?? 0;
}

/**
 * The next `count` frames from `from` on. A window is a number of opportunities, not of milliseconds, so a
 * busy run measures the same claim over a longer stretch of wall clock.
 */
function framesFrom(rec: ScrollRecord, from: number, count: number): ScrollRecord['frames'] {
    return rec.frames.filter(f => f.t >= from).slice(0, count);
}

/** Fail as "this run could not measure it", which is a different fact from "the app did it". */
function requireFrames(frames: ScrollRecord['frames'], what: string): void {
    expect(frames.length, `${what}: only ${frames.length} frames were sampled, ${MIN_WINDOW_FRAMES} are needed to judge it`)
        .toBeGreaterThanOrEqual(MIN_WINDOW_FRAMES);
}

/** Wait until the release is stamped in the page, so nothing released afterwards can be mistaken for it. */
async function markReleased(page: Page): Promise<void> {
    await page.evaluate(() => { (window as any).__rec.releasedAt = performance.now(); });
}

/**
 * Wait out the landing in frames: `count` samples of the viewport after the released page arrived. A busy run
 * gets a longer stretch of seconds for the same number of opportunities.
 */
async function waitForLandingWindow(page: Page, count: number): Promise<void> {
    await page.waitForFunction((n) => {
        const rec = (window as any).__rec;
        return rec.landedFrame > 0 && rec.frames.length - rec.landedFrame >= n;
    }, count, { timeout: 60000 });
}

/**
 * Wait for the frame the position stopped changing on because the app stopped writing to it. The window is
 * anchored at the moment this is called: "no app write in the last three frames" is already true the instant
 * you ask.
 */
async function waitUntilQuiet(page: Page): Promise<void> {
    const from = await page.evaluate(() => (window as any).__rec.frames.length as number);
    await page.waitForFunction((state) => {
        const rec = (window as any).__rec;
        const frames = rec.frames as Array<{ t: number }>;
        if (frames.length - state.from < state.quiet + 2) return false;
        const edge = frames[frames.length - 1 - state.quiet].t;
        const writes = rec.writes as Array<{ t: number; by: string }>;
        for (let i = writes.length - 1; i >= 0; i--) {
            if (writes[i].by === 'app') return writes[i].t < edge;
        }
        return true;
    }, { from, quiet: QUIESCENT_FRAMES }, { timeout: 30000 });
}

/** The entry under the top edge of the viewport right now — the row the reader is looking at. */
async function anchorRowAtTopEdge(page: Page): Promise<string> {
    const id = await page.evaluate(() => {
        const list = document.querySelector('.chat-messages') as HTMLElement;
        const edge = list.getBoundingClientRect().top;
        for (const node of Array.from(list.querySelectorAll<HTMLElement>('[data-entry-id]'))) {
            if (node.getBoundingClientRect().bottom > edge) return node.dataset.entryId!;
        }
        return '';
    });
    expect(id, 'no entry under the top edge to follow').not.toBe('');
    await page.evaluate((row) => (window as any).__followRow(row), id);
    return id;
}

interface Jitter {
    appWrites: number;
    downwardPx: number;
    maxDownwardPx: number;
    reversingFrames: number;
    frames: number;
    scans: number;
    rectReads: number;
    scrollEvents: number;
}

/** What the app did to the viewport across these frames, and how often against the reader. */
function jitterIn(rec: ScrollRecord, frames: ScrollRecord['frames']): Jitter {
    if (frames.length === 0) {
        return { appWrites: 0, downwardPx: 0, maxDownwardPx: 0, reversingFrames: 0, frames: 0, scans: 0, rectReads: 0, scrollEvents: 0 };
    }
    const from = frames[0].t;
    const to = frames[frames.length - 1].t + 1;
    const app = rec.writes.filter(w => w.t >= from && w.t < to && w.by === 'app');
    // A write that lands below where it started moves the reader away from the top they are heading for.
    const downward = app.map(w => Math.max(0, w.to - w.from));
    const tops = frames.map(f => f.top);
    let reversingFrames = 0;
    let dir = 0;
    for (let i = 1; i < tops.length; i++) {
        const d = tops[i] - tops[i - 1];
        const sign = d < -1 ? -1 : d > 1 ? 1 : 0;
        if (sign !== 0 && dir !== 0 && sign !== dir) reversingFrames++;
        if (sign !== 0) dir = sign;
    }
    const inWindow = rec.frames.filter(f => f.t >= from && f.t < to).length;
    return {
        appWrites: app.length,
        downwardPx: Math.round(downward.reduce((a, b) => a + b, 0)),
        maxDownwardPx: Math.round(downward.reduce((a, b) => Math.max(a, b), 0)),
        reversingFrames,
        frames: inWindow,
        scans: rec.scans,
        rectReads: rec.rectReads,
        scrollEvents: rec.scrollEvents,
    };
}

async function openConversation(page: Page): Promise<void> {
    const fx = chatFixture();
    await navigateToTask(page, fx.taskId, fx.projectId);
    await waitForChatFormReady(page);
    await expect(page.locator('.chat-messages [data-entry-id]').first()).toBeVisible({ timeout: 15000 });
    // The reader needs somewhere to go: a transcript shorter than the viewport has no top edge to coast to.
    await expect.poll(() => page.evaluate(() => {
        const el = document.querySelector('.chat-messages') as HTMLElement;
        return +(el.scrollHeight / el.clientHeight).toFixed(2);
    }), { timeout: 20000, message: 'the transcript never grew past three viewports to read through' }).toBeGreaterThan(3);
    // The one-shot pin settle, while lazily rendered rows find their height, must be over before a gesture is
    // judged — the app writing during a coast is the thing being asked about.
    await installRecorder(page);
    await waitUntilQuiet(page);
}

/**
 * Let go of the bottom and coast upward: one `touchmove` to declare the reader's intent, then a decaying
 * ramp with no further input events, which is what a fling looks like to a scroll listener.
 */
async function startCoast(page: Page): Promise<void> {
    await page.evaluate(() => {
        const rec = (window as any).__rec;
        const el = document.querySelector('.chat-messages') as HTMLElement;
        el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true, cancelable: true }));
        el.dispatchEvent(new TouchEvent('touchmove', { bubbles: true, cancelable: true }));
        (window as any).__coasting = true;
        let speed = 46;
        const step = () => {
            if (!(window as any).__coasting) return;
            const from = el.scrollTop;
            rec.tag = 'harness';
            el.scrollTop = Math.max(0, from - speed);
            rec.tag = 'app';
            speed = Math.max(7, speed * 0.985);
            if (from - el.scrollTop <= 0) { (window as any).__coasting = false; return; }
            requestAnimationFrame(step);
        };
        rec.coastStartedAt = performance.now();
        rec.coastFrame = rec.frames.length;
        requestAnimationFrame(step);
    });
}

async function stopCoast(page: Page): Promise<void> {
    await page.evaluate(() => { (window as any).__coasting = false; });
}

/** `ANCHOR_SETTLE_FRAMES` in `TaskChat.svelte`: the settle's own bounded re-reads of the reader's row. */
const ANCHOR_SETTLE_FRAMES = 20;

/** The frames a coast covers: `count`, or the coast's own end if it ran out of document first. */
async function coastFrames(page: Page, count: number): Promise<void> {
    await page.waitForFunction((n) => {
        const rec = (window as any).__rec;
        return rec.frames.length - rec.coastFrame >= n || !(window as any).__coasting;
    }, count, { timeout: 60000 });
}

/** Coasts until the app has asked for older rows, which it does 1.5 viewports before the reader gets there. */
async function coastUntilOlderRowsAreWanted(page: Page): Promise<void> {
    await startCoast(page);
    const asked = await expect.poll(() => page.evaluate(() => ({
        asked: (window as any).__rec.fetchAt > 0,
        frames: (window as any).__rec.frames.length - (window as any).__rec.coastFrame,
    })), { timeout: 60000, message: 'the coast never made the app ask for the older rows' })
        .toMatchObject({ asked: true })
        .then(() => page.evaluate(() => (window as any).__rec.frames.length - (window as any).__rec.coastFrame));
    console.log(`older rows asked for after ${asked} coast frames`);
    expect(asked, 'the app should ask for older rows long before the reader runs out of document')
        .toBeLessThanOrEqual(MAX_FRAMES_TO_ASK);
}

/**
 * Hold the next-older message window until the test hands back the release. Only the message page: `before=`
 * names two reads, and holding both makes one wait stand for two landings.
 */
async function holdOlderWindow(page: Page): Promise<() => void> {
    let release: () => void = () => {};
    const gate = new Promise<void>(resolve => { release = resolve; });
    await page.route(url => url.pathname.endsWith('/messages') && url.searchParams.has('before'), async route => {
        await gate;
        await route.continue();
    });
    return () => release();
}

test.describe('Chat scroll on touch', () => {
    test('a coast upward is left alone: nothing pulls the reader back to the bottom', async ({ page }) => {
        test.setTimeout(120000);
        await openConversation(page);
        // Nothing may land during this window: the question is the coast alone, not a page arriving.
        await page.route(url => url.searchParams.has('before'), () => { /* held for the whole test */ });

        await startCoast(page);
        await coastFrames(page, LANDING_WINDOW_FRAMES);
        await stopCoast(page);

        const rec = await readRecord(page);
        const window = framesFrom(rec, rec.coastStartedAt, LANDING_WINDOW_FRAMES);
        requireFrames(window, 'the coast');
        const m = jitterIn(rec, window);
        console.log('coast-only jitter', JSON.stringify(m));
        expect(m.appWrites, `the app wrote scrollTop ${m.appWrites}× while the reader was coasting upward `
            + `(pushed them ${m.downwardPx}px down, worst single write ${m.maxDownwardPx}px)`).toBe(0);
    });

    test('an older page arriving mid-coast does not keep writing at the reader', async ({ page }) => {
        test.setTimeout(120000);
        await openConversation(page);
        const release = await holdOlderWindow(page);

        await coastUntilOlderRowsAreWanted(page);
        await markReleased(page);
        release();
        await expect.poll(() => page.evaluate(() => {
            const rec = (window as any).__rec;
            return rec.mutations.some(t => t >= rec.releasedAt);
        }), { timeout: 60000, message: 'the released older page never entered the transcript' }).toBe(true);
        await waitForLandingWindow(page, LANDING_WINDOW_FRAMES);

        const rec = await readRecord(page);
        await stopCoast(page);
        const window = framesFrom(rec, landingTime(rec), LANDING_WINDOW_FRAMES);
        requireFrames(window, 'the landing');

        // The reader's own travel is in these numbers too, so only the app's writes are read here: on touch
        // the settle's gesture listeners cannot see a kinetic scroll and write for every one of its frames.
        const m = jitterIn(rec, window);
        console.log('mid-coast landing jitter', JSON.stringify(m));
        expect(m.appWrites, `the settle wrote scrollTop ${m.appWrites} frames running against the coast`)
            .toBeLessThanOrEqual(4);
    });

    test('an older page arriving while the reader is still leaves them on their row', async ({ page }) => {
        test.setTimeout(120000);
        await openConversation(page);
        const release = await holdOlderWindow(page);

        await coastUntilOlderRowsAreWanted(page);
        const row = await anchorRowAtTopEdge(page);
        // The reader is still and the app is quiet from here on, so any drift after this is the page's.
        await stopCoast(page);
        await waitUntilQuiet(page);
        const still = await readRecord(page);
        const last = still.frames.at(-1);
        expect(last?.anchorTop, `the anchor row ${row} was not measurable before the page arrived`).not.toBeNull();
        const before = last!.anchorTop as number;
        await markReleased(page);
        release();
        await expect.poll(() => page.evaluate(() => {
            const rec = (window as any).__rec;
            return rec.mutations.some(t => t >= rec.releasedAt);
        }), { timeout: 60000, message: 'the released older page never entered the transcript' }).toBe(true);
        await waitForLandingWindow(page, LANDING_WINDOW_FRAMES);

        const rec = await readRecord(page);
        const landing = framesFrom(rec, landingTime(rec), LANDING_WINDOW_FRAMES)
            .filter(f => f.anchorTop !== null);
        requireFrames(landing, 'the landing');
        const offsets = landing.map(f => f.anchorTop as number);
        const excursion = Math.round(Math.max(...offsets.map(o => Math.abs(o - before))));
        const drift = Math.round(Math.abs(offsets[offsets.length - 1] - before));
        const writes = rec.writes.filter(w => w.by === 'app' && w.t >= landingTime(rec)).map(w => Math.round(w.to - w.from));
        console.log('still landing jitter', JSON.stringify({
            row, before, drift, excursion, writes,
            // Where the row sat and how tall the document was, every 5th frame.
            trace: landing.filter((_, i) => i % 5 === 0).map(f => `${Math.round(f.t - landingTime(rec))}:row=${Math.round(f.anchorTop as number)}:h=${f.h}`),
        }));
        expect(drift, `the page above slid the reader ${drift}px off the row they were reading`)
            .toBeLessThanOrEqual(60);
        expect(excursion, `the reader's row wandered ${excursion}px before it settled`)
            .toBeLessThanOrEqual(160);
    });

    test('the transcript is measured at most once per frame', async ({ page }) => {
        test.setTimeout(120000);
        await openConversation(page);

        await coastUntilOlderRowsAreWanted(page);
        await coastFrames(page, 30);
        await stopCoast(page);

        const rec = await readRecord(page);
        const window = framesFrom(rec, rec.coastStartedAt, LANDING_WINDOW_FRAMES);
        requireFrames(window, 'the coast');
        const m = jitterIn(rec, window);
        console.log('measurement cost', JSON.stringify({ frames: m.frames, scans: m.scans, rectReads: m.rectReads, scrollEvents: m.scrollEvents }));
        // One scan per opportunity — a frame or a scroll event, whichever the run produced more of — plus the
        // settle's bounded re-reads of the reader's row. A scan per event would scale with the count.
        const opportunities = Math.max(m.frames, m.scrollEvents) + ANCHOR_SETTLE_FRAMES;
        expect(m.scans, `${m.scans} transcript scans for ${m.frames} frames and ${m.scrollEvents} scroll events, `
            + `past one per opportunity plus the settle's ${ANCHOR_SETTLE_FRAMES}`)
            .toBeLessThanOrEqual(opportunities);
    });
});
