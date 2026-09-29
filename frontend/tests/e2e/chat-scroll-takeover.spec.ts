import { test, expect, type Page } from '@playwright/test';
import {
    isolatedProject, gotoProject, getProjectId, csrfFetch, navigateToTask, waitForChatFormReady,
    waitForSendButton, sendMessage, waitForChatGrowth, waitForStable,
} from './helpers';

// A reader leaving the bottom is the follow tracker's whole job, and not every gesture that asks for what
// is above is a gesture a listener can see. Measured in Chromium with a scrollbar it paints, in one window:
// `xvfb-run -a npx playwright test chat-scroll-takeover --headed --workers=1` — several headed windows on
// one X display make a real thumb drag move nothing, so those two cases need the single window.
//
//   thumb drag    pointerdown, ten mouse moves, ZERO pointermove, twelve scroll events
//   track click   pointerdown, no pointermove, ~30 scroll events of one animated page-up
//
// So a scrollbar gesture reaches the app as a position change with no travel behind it, and the takeover is
// judged from the position alone — the two numbers every browser reports for one (`scrollTop`,
// `scrollHeight`), which is what keeps the rule browser-independent. Headless Chromium reserves no pixels
// for a scrollbar, so those two cases skip there and the first test drives the position directly.
// `chat-window-scroll.spec.ts` had to say of its own `wheelUp` that writing `scrollTop` is not a gesture:
// that write is precisely what a reader's scrollbar looks like to the app.
//
// The keyboard cases come from a control in the chat column that is not the scroller: the jump-latest
// button is a sibling of `.chat-messages`, so the tracker's own keydown listener never hears the key while
// `CardDetail`'s window listener is the one that scrolls.
//
// Gestures the tracker can see are covered elsewhere: `chat-stream-follow.spec.ts` (a large wheel-up
// during a response) and `chat-scroll-jitter.spec.ts` (a touch fling coasting).
isolatedProject();

const SEED_ROWS = 120;
/** Pinned mock fixture: 16 chunks at 100ms, so the answer streams while the reader works the wheel. */
const STREAMING_PROMPT = 'llm-fixture:text_slow';
/** `SCROLL_BOTTOM_THRESHOLD`: how far above the bottom still counts as being at it. */
const BOTTOM_BAND = 32;

interface Write { t: number; to: number; height: number; view: number; by: string }

/**
 * Note every `scrollTop` write on the chat scroller, tagged by who made it, plus every scroll event and the
 * first wheel that asks for what is above. A write through this accessor is the harness's or the app's; a
 * scroll event with no write behind it is the browser's own — its scrollbar, a key, its anchoring — which
 * is the distinction every assertion here is made of.
 */
async function installRecorder(page: Page): Promise<void> {
    await page.evaluate(() => {
        const rec: any = { writes: [], scrolls: 0, scrollsAtGesture: 0, gestureAt: 0, landedAt: 0, firstWheelUp: 0, lastKey: '', focusTrail: [] as string[], tag: 'app' };
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
        document.addEventListener('wheel', landed, { capture: true, passive: true });
        document.addEventListener('keydown', landed, { capture: true });
        list.addEventListener('wheel', (e: WheelEvent) => {
            if (e.deltaY < 0 && !rec.firstWheelUp) rec.firstWheelUp = performance.now();
        }, { capture: true });
        // Where a key actually landed: a scroll key only moves the transcript through the focus chain.
        const name = (n: EventTarget | null) => {
            const t = n as HTMLElement | null;
            return t ? `${t.tagName.toLowerCase()}.${String(t.className || '').split(' ')[0]}` : 'null';
        };
        rec.focusTrail = [] as string[];
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

interface Since { pins: Write[]; scrolls: number; wheelUp: number; lastKey: string; focusTrail: string[] }

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
            wheelUp: rec.firstWheelUp,
            lastKey: rec.lastKey,
            focusTrail: [...(rec.focusTrail as string[])],
        };
    });
}

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

/** Seed a conversation long enough that one page up is hundreds of pixels from the bottom. */
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
 * Open a task with a tall transcript resting at the bottom, with nothing else moving under the measurement:
 * the page of older rows that fills a short chat is answered first, then held.
 */
async function openConversation(page: Page): Promise<void> {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
        method: 'POST',
        body: { title: `scroll-takeover-${Date.now()}`, prompt: 'scroll takeover fixture', status: 'in_progress' },
    });
    expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
    const taskId: string = created.data.id;
    await seedConversation(page, projectId, taskId, SEED_ROWS);
    await navigateToTask(page, taskId, projectId);
    await waitForChatFormReady(page);
    await expect(page.locator('.chat-messages [data-entry-id]').first()).toBeVisible({ timeout: 15000 });
    await waitForSendButton(page);
    await page.waitForFunction(() => {
        const el = document.querySelector('.chat-messages') as HTMLElement;
        return el.scrollHeight - el.scrollTop - el.clientHeight < 4;
    }, undefined, { timeout: 15000 });
    await page.route(url => url.searchParams.has('before'), () => { /* held for the rest of the test */ });
    await installRecorder(page);
}

/**
 * Where the reader ended up once every frame the takeover could provoke has run. An immediate read proves
 * nothing: a pin answers the scroll event within one round-trip, so the position that matters is the one
 * that survives the settle.
 */
async function settledPosition(page: Page) {
    // A pin answers the scroll event within one round-trip, so the position that matters is the one that
    // survives the settle: wait for the scroller to stop moving, then read it once.
    await waitForStable(() => position(page), {
        what: 'the chat scroller after the gesture',
        stableReads: 4,
        intervalMs: 80,
        timeout: 3_000,
    });
    return position(page);
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

/**
 * Where this browser paints the chat scroller's scrollbar, or null when it paints none to aim at —
 * headless Chromium reserves no pixels for one, so a drag there would drag the text instead. The reader
 * is at the bottom of the transcript, which is also where the thumb is, so the grab point is the bottom of
 * the track and does not depend on estimating where the thumb slid to.
 */
function scrollbarGeometry(page: Page): Promise<{ grabY: number; trackTop: number; trackBottom: number; x: number } | null> {
    return page.evaluate(() => {
        const el = document.querySelector('.chat-messages') as HTMLElement;
        const width = el.offsetWidth - el.clientWidth;
        if (width <= 0) return null;
        const cs = getComputedStyle(el);
        const box = el.getBoundingClientRect();
        // A horizontal scrollbar eats the bottom of the vertical track — the corner belongs to neither.
        const gutter = el.offsetHeight - el.clientHeight;
        const top = box.top + parseFloat(cs.borderTopWidth);
        const bottom = box.bottom - parseFloat(cs.borderBottomWidth) - gutter;
        // A reader at the bottom has the thumb flush with the track's end; press its middle, since a press
        // a few pixels off that end lands on the track below it, where a page-down is already clamped.
        const thumb = Math.max(30, (bottom - top) * el.clientHeight / el.scrollHeight);
        return {
            x: box.right - parseFloat(cs.borderRightWidth) - width / 2,
            trackTop: top,
            trackBottom: bottom,
            grabY: bottom - Math.max(20, thumb / 2),
        };
    });
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

    test('dragging the scrollbar thumb up leaves the reader there', async ({ page }) => {
        await openConversation(page);
        const geometry = await scrollbarGeometry(page);
        test.skip(geometry === null, 'this browser paints no scrollbar to drag — run it headed');
        const g = geometry!;
        const before = await position(page);
        expect(before.fromBottom, 'the reader is not at the bottom, where the thumb is').toBeLessThanOrEqual(4);

        await page.mouse.move(g.x, g.grabY);
        await page.waitForTimeout(150); // drag settle: headless is slow, and the press needs the thumb under it
        await beginGesture(page);
        await page.mouse.down();
        for (let i = 1; i <= 12; i++) {
            await page.mouse.move(g.x, g.grabY - i * 6);
            await page.waitForTimeout(20); // drag pacing: one move per frame is what a thumb drag looks like
        }
        await page.mouse.up();

        await waitForScrollEvent(page);
        const dragged = await sinceGesture(page);
        expect(
            dragged.scrolls,
            `the drag moved nothing (${JSON.stringify(g)}); the app wrote `
            + `${JSON.stringify(dragged.pins)} in reply`,
        ).toBeGreaterThan(0);
        expect((await position(page)).scrollTop, 'the drag left the viewport where it started')
            .toBeLessThan(before.scrollTop);
        await expectLeftAlone(page, 'a scrollbar thumb drag');
    });

    test('clicking the scrollbar track above the thumb leaves the reader there', async ({ page }) => {
        await openConversation(page);
        const geometry = await scrollbarGeometry(page);
        test.skip(geometry === null, 'this browser paints no scrollbar to click — run it headed');
        const g = geometry!;
        const before = await position(page);

        // Near the top of the track: the reader is at the bottom, so the thumb is at the other end.
        const y = g.trackTop + (g.trackBottom - g.trackTop) * 0.15;
        await page.mouse.move(g.x, y);
        await page.waitForTimeout(150); // drag settle: headless is slow, and the press needs the pointer parked first
        await beginGesture(page);
        await page.mouse.down();
        await page.waitForTimeout(300); // a track click is one animated page-up: releasing early cancels it
        await page.mouse.up();

        await waitForScrollEvent(page);
        const clicked = await sinceGesture(page);
        expect(
            clicked.scrolls,
            `the track click moved nothing (${JSON.stringify(g)}); the app wrote `
            + `${JSON.stringify(clicked.pins)} in reply`,
        ).toBeGreaterThan(0);
        expect((await position(page)).scrollTop, 'the track click left the viewport where it started')
            .toBeLessThan(before.scrollTop);
        await expectLeftAlone(page, 'a scrollbar track click');
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

    test('a wheel-up inside the bottom band is not undone by the next growth pin', async ({ page }) => {
        test.setTimeout(120000);
        await openConversation(page);

        // Tokens must be arriving: the pin this measures is the one growth triggers.
        await sendMessage(page, `takeover-wheel ${Date.now()} ${STREAMING_PROMPT}`);
        await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 20000 });
        await waitForChatGrowth(page); // the pin this measures is the one growth triggers
        const box = await page.locator('.chat-messages').boundingBox();
        expect(box, 'no chat list to park the pointer on').not.toBeNull();
        await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);

        // One short notch: it lands inside the band that still counts as being at the bottom, so nothing
        // about the resulting position says "the reader left" — only the gesture does.
        await beginGesture(page);
        await page.mouse.wheel(0, -BOTTOM_BAND / 2);
        const since = await sinceGesture(page);
        expect(since.wheelUp, 'the wheel never reached the chat').toBeGreaterThan(0);

        await waitForSendButton(page, 30000);
        await expectLeftAlone(page, 'a wheel-up inside the bottom band during a streaming answer');
    });
});
