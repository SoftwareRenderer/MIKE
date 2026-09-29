import { test, expect, type Page } from '@playwright/test';
import {
    isolatedProject, gotoProject, getProjectId, csrfFetch, navigateToTask, waitForChatFormReady,
    waitForChatStill, waitForStable,
} from './helpers';

// What a reader feels as jitter while scrolling up is one measurable thing: the viewport moving in a
// direction nobody asked for. jsdom has no layout and no kinetic scroll, so this is a browser question.
//
// The recorder tags every `scrollTop` write — a write through the accessor is the app's, a position
// change without one is the browser's (its kinetic scroll or its scroll anchoring) — which is what
// separates "the app moved me" from "my own fling carried me".
//
// The gesture is scripted rather than synthesized: `Input.synthesizeScrollGesture` with
// `gestureSourceType: 'touch'` moves this list 0px in headless Chromium (measured, both signs), so the
// only faithful kinetic scroll available here is a decaying ramp driven from the page — one `touchmove`
// to declare intent, then the viewport coasting with no further input events. That is exactly what a
// fling looks like to a listener, and it is why gesture-type listeners cannot see it.
//
// The anchor arithmetic (`resolveAnchor`, `anchorScrollDelta`) is unit-tested in
// `src/lib/utils/__tests__/chat-window.unit.ts`; `chat-window-scroll.spec.ts` answers the wheel case.
isolatedProject();

// A phone: touch input, and a viewport short enough that many entries cross the `content-visibility`
// threshold per frame, which is where a reader notices layout cost.
test.use({ viewport: { width: 412, height: 732 }, deviceScaleFactor: 2, hasTouch: true, isMobile: true });

const SEED_ROWS = 200;

interface ScrollRecord {
    writes: Array<{ t: number; from: number; to: number; by: string }>;
    frames: Array<{ t: number; top: number; anchorTop: number | null; h: number }>;
    rectReads: number;
    scans: number;
    landedAt: number;
    fetchAt: number;
    coastStartedAt: number;
}

/**
 * Instrument the chat scroller: tag `scrollTop` writes by who made them (`app`, or `harness` for this
 * spec's own gesture), sample the position once per frame, count the transcript measurements the scroll
 * path performs, and stamp when the request for older rows went out and when those rows entered the
 * document — the reader cannot see the request, only the landing.
 *
 * `__followRow(id)` starts sampling one entry's offset from the top edge of the viewport. That is what
 * the reader calls jitter: the row they were reading moving, whatever the viewport did to keep it there.
 */
async function installRecorder(page: Page): Promise<void> {
    await page.evaluate(() => {
        const rec = {
            writes: [] as Array<{ t: number; from: number; to: number; by: string }>,
            frames: [] as Array<{ t: number; top: number; anchorTop: number | null; h: number }>,
            rectReads: 0, scans: 0, landedAt: 0, fetchAt: 0, coastStartedAt: 0, tag: 'app', anchorId: '',
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
        // `offsetTop` rather than a client rect: it is one layout read per frame and this file also
        // counts rects, which must stay the app's cost and not the harness's.
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

        new MutationObserver(() => { if (!rec.landedAt) rec.landedAt = performance.now(); })
            .observe(document.querySelector('.chat-content')!, { childList: true });

        const real = window.fetch;
        (window as any).fetch = (input: any, init?: any) => {
            if (typeof input === 'string' && input.includes('before=')) rec.fetchAt = performance.now();
            return real(input as any, init as any);
        };
    });
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
}

/** Summarise the log in `[from, to)`: what the app did to the viewport, and how often against the reader. */
function jitterIn(rec: ScrollRecord, from: number, to: number): Jitter {
    const app = rec.writes.filter(w => w.t >= from && w.t < to && w.by === 'app');
    // A write that lands below where it started moves the reader away from the top they are heading for.
    const downward = app.map(w => Math.max(0, w.to - w.from));
    const frames = rec.frames.filter(f => f.t >= from && f.t < to);
    const tops = frames.map(f => f.top);
    let reversingFrames = 0;
    let dir = 0;
    for (let i = 1; i < tops.length; i++) {
        const d = tops[i] - tops[i - 1];
        const sign = d < -1 ? -1 : d > 1 ? 1 : 0;
        if (sign !== 0 && dir !== 0 && sign !== dir) reversingFrames++;
        if (sign !== 0) dir = sign;
    }
    return {
        appWrites: app.length,
        downwardPx: Math.round(downward.reduce((a, b) => a + b, 0)),
        maxDownwardPx: Math.round(downward.reduce((a, b) => Math.max(a, b), 0)),
        reversingFrames,
        frames: frames.length,
        scans: rec.scans,
        rectReads: rec.rectReads,
    };
}

async function readRecord(page: Page): Promise<ScrollRecord> {
    return page.evaluate(() => JSON.parse(JSON.stringify((window as any).__rec)));
}

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

/**
 * One conversation per worker, reused by every test in this file. Each test reloads the page, so nothing
 * a previous test fetched is left on screen, and the fetches are read-only — but seeding is 200 sequential
 * POSTs against the lane server every worker already queues its executor turns on.
 */
let conversation: { projectId: string; taskId: string } | null = null;

async function openConversation(page: Page): Promise<{ projectId: string; taskId: string }> {
    if (!conversation) {
        await gotoProject(page);
        await page.waitForSelector('.project-item', { timeout: 10000 });
        const projectId = await getProjectId(page);
        const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
            method: 'POST',
            body: { title: `coast-${Date.now()}`, prompt: 'coast scroll fixture', status: 'in_progress' },
        });
        expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
        conversation = { projectId, taskId: created.data.id };
        await seedConversation(page, conversation.projectId, conversation.taskId, SEED_ROWS);
    }
    await navigateToTask(page, conversation.taskId, conversation.projectId);
    await waitForChatFormReady(page);
    await expect(page.locator('.chat-messages [data-entry-id]').first()).toBeVisible({ timeout: 15000 });
    // The bottom, laid out: the pin settle writes the scroller on animation frames, so the wait is that
    // the position and height stop changing — what the realize-heights pass keeps moving under the reader.
    await waitForChatStill(page);
    return conversation;
}

/**
 * The reader's row has stopped moving, and its settled offset is what the landing gets measured against.
 * A resting `scrollTop` still hides a document whose rows above the fold are being realized, which would
 * make the baseline somebody else's measurement, so the wait is on the recorder's own row sample.
 */
async function settledAnchorTop(page: Page): Promise<number> {
    const top = await waitForStable(async () => (await readRecord(page)).frames.at(-1)?.anchorTop ?? null, {
        what: "the reader's row with the coast stopped",
        until: (t) => t !== null,
        timeout: 15_000,
    });
    return Math.round(top as number);
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
        requestAnimationFrame(step);
    });
}

async function stopCoast(page: Page): Promise<void> {
    await page.evaluate(() => { (window as any).__coasting = false; });
}

/** Coasts until the app has asked for older rows, which it does 1.5 viewports before the reader gets there. */
async function coastUntilOlderRowsAreWanted(page: Page): Promise<void> {
    await startCoast(page);
    await expect(async () => {
        expect((await readRecord(page)).fetchAt).toBeGreaterThan(0);
    }).toPass({ timeout: 20000 });
}

/** Hold the next-older window until the test hands back the release. */
async function holdOlderWindow(page: Page): Promise<() => void> {
    let release: () => void = () => {};
    const gate = new Promise<void>(resolve => { release = resolve; });
    await page.route(url => url.searchParams.has('before'), async route => {
        await gate;
        await route.continue();
    });
    return release;
}

test.describe('Chat scroll on touch', () => {
    test('a coast upward is left alone: nothing pulls the reader back to the bottom', async ({ page }) => {
        test.setTimeout(120000);
        await openConversation(page);
        // Nothing may land during this window: the question is the coast alone, not a page arriving.
        await page.route(url => url.searchParams.has('before'), () => { /* held for the whole test */ });
        await installRecorder(page);

        await startCoast(page);
        await page.waitForTimeout(2500); // measurement window: this test judges the coast over its first 2.5s
        await stopCoast(page);

        const rec = await readRecord(page);
        const m = jitterIn(rec, rec.coastStartedAt, rec.coastStartedAt + 2500);
        console.log('coast-only jitter', JSON.stringify(m));
        expect(m.appWrites, `the app wrote scrollTop ${m.appWrites}× while the reader was coasting upward `
            + `(pushed them ${m.downwardPx}px down, worst single write ${m.maxDownwardPx}px)`).toBe(0);
    });

    test('an older page arriving mid-coast does not keep writing at the reader', async ({ page }) => {
        test.setTimeout(120000);
        await openConversation(page);
        const release = await holdOlderWindow(page);
        await installRecorder(page);

        await coastUntilOlderRowsAreWanted(page);
        await release();
        await expect(async () => {
            expect((await readRecord(page)).landedAt).toBeGreaterThan(0);
        }).toPass({ timeout: 20000 });
        await page.waitForTimeout(1500); // measurement window: this test judges the post-landing settle over 1.5s
        const rec = await readRecord(page);
        await stopCoast(page);

        // The reader's own travel is in these numbers too, so only the app's writes are read here: the
        // settle's gesture listeners (wheel/touchstart/keydown) cannot see a kinetic scroll, so on touch
        // it writes for every one of its frames instead of stopping at the first unexplained move.
        const m = jitterIn(rec, rec.landedAt, rec.landedAt + 1500);
        console.log('mid-coast landing jitter', JSON.stringify(m));
        expect(m.appWrites, `the settle wrote scrollTop ${m.appWrites} frames running against the coast`)
            .toBeLessThanOrEqual(4);
    });

    test('an older page arriving while the reader is still leaves them on their row', async ({ page }) => {
        test.setTimeout(120000);
        await openConversation(page);
        const release = await holdOlderWindow(page);
        await installRecorder(page);

        await coastUntilOlderRowsAreWanted(page);
        const row = await anchorRowAtTopEdge(page);
        // Nothing moves before the landing: a row that drifts now drifted because of the page.
        await stopCoast(page);
        const before = await settledAnchorTop(page);
        await release();
        await expect(async () => {
            expect((await readRecord(page)).landedAt).toBeGreaterThan(0);
        }).toPass({ timeout: 20000 });
        await page.waitForTimeout(1500); // measurement window: this test follows the reader's row over 1.5s past the landing
        const rec = await readRecord(page);

        const landing = rec.frames.filter(f => f.t >= rec.landedAt - 50 && f.anchorTop !== null);
        const offsets = landing.map(f => f.anchorTop as number);
        const excursion = offsets.length ? Math.round(Math.max(...offsets.map(o => Math.abs(o - before)))) : 0;
        const drift = offsets.length ? Math.round(Math.abs(offsets[offsets.length - 1] - before)) : 0;
        const writes = rec.writes.filter(w => w.by === 'app' && w.t >= rec.landedAt - 50).map(w => Math.round(w.to - w.from));
        console.log('still landing jitter', JSON.stringify({
            row, before, drift, excursion, writes,
            // Where the row sat and how tall the document was, every 5th frame: a row that slides while
            // the height is still growing means the settle stopped before the layout did.
            trace: landing.filter((_, i) => i % 5 === 0).map(f => `${Math.round(f.t - rec.landedAt)}:row=${Math.round(f.anchorTop as number)}:h=${f.h}`),
        }));
        expect(drift, `the page above slid the reader ${drift}px off the row they were reading`)
            .toBeLessThanOrEqual(60);
        expect(excursion, `the reader's row wandered ${excursion}px before it settled`)
            .toBeLessThanOrEqual(160);
    });

    test('the transcript is measured at most once per frame', async ({ page }) => {
        test.setTimeout(120000);
        await openConversation(page);
        await installRecorder(page);
        await coastUntilOlderRowsAreWanted(page);
        // Frames to count rather than seconds to sit: the measurement is scans per frame, so what this
        // needs is the recorder having sampled the coast for a while past the fetch.
        await expect.poll(async () => {
            const rec = await readRecord(page);
            return rec.frames.filter(f => f.t >= rec.fetchAt).length;
        }, { timeout: 15_000, message: 'the recorder never sampled the coast past the fetch' })
            .toBeGreaterThanOrEqual(20);
        await stopCoast(page);

        const rec = await readRecord(page);
        const m = jitterIn(rec, rec.frames[0].t, (rec.frames.at(-1)?.t ?? 0) + 1);
        console.log('measurement cost', JSON.stringify({ frames: m.frames, scans: m.scans, rectReads: m.rectReads }));
        // Scroll events are already one per frame, so this is the budget that matters: a handler that
        // scans the transcript per event instead of per frame would multiply it by the event count.
        expect(m.scans, `${m.scans} transcript scans over ${m.frames} frames`).toBeLessThanOrEqual(m.frames);
    });
});
