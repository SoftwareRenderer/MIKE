import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';

// Mock all sub-components that ChatEntries depends on.
vi.mock('$lib/components/Chat/ChatMessage.svelte', () => ({
    default: () => '<div class="mock-chat-message"></div>',
}));
vi.mock('$lib/components/Chat/CheckpointDivider.svelte', () => ({
    default: () => '<div class="mock-checkpoint-divider"></div>',
}));
vi.mock('$lib/components/Chat/QueueIndicator.svelte', () => ({
    default: () => '<div class="mock-queue-indicator"></div>',
}));
vi.mock('$lib/utils/toolArgs', () => ({
}));
vi.mock('$lib/constants', () => ({
    SCROLL_BOTTOM_THRESHOLD: 100,
}));

import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import { createChatFollow, type ChatFollow } from '$lib/composables/use-chat-follow.svelte';

// jsdom has no ResizeObserver, and layout changing without a scroll event is half of what the
// sticky header has to survive. Controllable, installed before the component is created.
let roCallbacks: Array<() => void>;
class FakeResizeObserver {
    constructor(cb: () => void) { roCallbacks.push(cb); }
    observe() {}
    unobserve() {}
    disconnect() {}
}
function fireResize() {
    for (const cb of [...roCallbacks]) cb();
}

const realRO = (globalThis as { ResizeObserver?: unknown }).ResizeObserver;
beforeEach(() => {
    roCallbacks = [];
    (globalThis as { ResizeObserver?: unknown }).ResizeObserver = FakeResizeObserver;
});
afterEach(() => {
    (globalThis as { ResizeObserver?: unknown }).ResizeObserver = realRO;
});

// Effects flush on microtasks and the mount effect pins on the next frame; drain
// both so nothing moves scrollTop after the test set it.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
    await new Promise((r) => requestAnimationFrame(r));
    await new Promise((r) => requestAnimationFrame(r));
}

function makeEntry(id: string, role: 'user' | 'assistant', content: string) {
    return { id, role, content, isStreaming: false, createdAt: new Date().toISOString() };
}

function makeMeta(withCheckpoint = false) {
    return {
        cpState: withCheckpoint ? { isPending: false, commitLabel: 'checkpoint', commitHash: 'abc' } : null,
        cpThread: null,
        isLastUserMessage: false,
        isFirstUserMessage: false,
    };
}

const baseProps = {
    lastEntryIdx: 0,
    activeThreadId: 'main',
    threads: [],
    awaitingResponse: false,
    connected: true,
    queuePosition: 0,
    queueEstimatedWait: 0,
    promptProgress: 0,
    retryCount: 0,
    retryError: '',
    isAgentSelected: () => false,
    taskStatus: 'in_progress',
    taskExecutionState: '',
    projectId: 'test-project',
    taskId: 'test-task',
    onDismissError: vi.fn(),
    onRetryConnect: vi.fn(),
    onThreadSwitch: vi.fn(),
    onMsgDelete: vi.fn(),
    onMsgFork: vi.fn(),
    onMsgSend: vi.fn(),
    onRegenerate: vi.fn(),
    onNavigateTask: vi.fn(),
    hasEarlier: false,
    loadingEarlier: false,
    onLoadEarlier: () => Promise.resolve(),
};

// The two user messages share a first line on purpose: only the scroll position a
// click produces can tell them apart.
const entries = [
    makeEntry('u-a', 'user', 'Fix the bug\nplease'),
    makeEntry('a-1', 'assistant', 'On it'),
    makeEntry('u-b', 'user', 'Fix the bug\ntoo'),
    makeEntry('a-2', 'assistant', 'Done'),
];

// The same conversation with distinct first lines, to read what the header says.
const namedEntries = [
    makeEntry('u-a', 'user', 'Alpha request\nmore'),
    makeEntry('a-1', 'assistant', 'On it'),
    makeEntry('u-b', 'user', 'Bravo request\nmore'),
    makeEntry('a-2', 'assistant', 'Done'),
];

const namedEntries3 = [
    ...namedEntries,
    makeEntry('u-c', 'user', 'Charlie request\nmore'),
    makeEntry('a-3', 'assistant', 'Shipped'),
];

// Rendered geometry in content space (px from the content top):
//   user A [0, 400) · assistant [400, 500) · user B [500, 600) · assistant [600, 700)
const A_TOP = 0, A_BOTTOM = 400, B_TOP = 500, B_BOTTOM = 600;
const CONTAINER_TOP = 100, CONTAINER_HEIGHT = 500;
/** The header overlay's box — a tapped message lands on its bottom edge. */
const HEADER_HEIGHT = 46;
/** A checkpoint divider drawn above a user message. */
const DIVIDER_HEIGHT = 34;
const SCROLL_HEIGHT = 1200;

// Same conversation, one more message. Scroll positions stay under 600 so the chat is
// never near the bottom, where it would follow the latest message again.
const THREE_A_TOP = 0, THREE_A_BOTTOM = 140, THREE_B_TOP = 200, THREE_B_BOTTOM = 350;
const THREE_C_TOP = 400, THREE_C_BOTTOM = 550;
const THREE_GEOMETRY = new Map<number, number[]>([
    [0, [THREE_A_TOP, THREE_A_BOTTOM]],
    [2, [THREE_B_TOP, THREE_B_BOTTOM]],
    [4, [THREE_C_TOP, THREE_C_BOTTOM]],
]);

function rect(top: number, bottom: number) {
    return { top, bottom, left: 0, right: 0, width: 0, height: bottom - top, x: 0, y: top, toJSON: () => ({}) };
}

function stubRect(el: Element, value: () => { top: number; bottom: number }) {
    Object.defineProperty(el, 'getBoundingClientRect', { configurable: true, value: () => rect(value().top, value().bottom) });
}

/**
 * Live geometry derived from `scrollTop`, so every rect read sees the effect of the
 * previous scroll write and the settle loop converges here as it does on screen.
 */
function mountGeometry(container: HTMLElement, follow: ChatFollow, geometry?: Map<number, number[]>, scrollHeight = SCROLL_HEIGHT) {
    const el = container.querySelector<HTMLElement>('.chat-messages')!;
    let top = 0;
    // The mobile tab switch leaves this component mounted with the column `display: none`,
    // where every rect the browser reports is a zero.
    let laidOut = true;
    let scrollEvents = 0;
    el.addEventListener('scroll', () => { scrollEvents++; });
    Object.defineProperty(el, 'scrollTop', {
        configurable: true,
        get: () => top,
        set: (value: number) => {
            top = Math.max(0, Math.min(value, scrollHeight - CONTAINER_HEIGHT));
        },
    });
    Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => scrollHeight });
    Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => (laidOut ? CONTAINER_HEIGHT : 0) });
    stubRect(el, () => (laidOut ? { top: CONTAINER_TOP, bottom: CONTAINER_TOP + CONTAINER_HEIGHT } : { top: 0, bottom: 0 }));

    let geom = geometry ?? new Map([[0, [A_TOP, A_BOTTOM]], [2, [B_TOP, B_BOTTOM]]]);

    // The overlay is a sibling of the scroller, pinned to its top edge — and mounted whenever
    // there are entries, so the sticky line is always its measured bottom edge.
    const header = container.querySelector<HTMLElement>('.chat-sticky-header')!;
    stubRect(header, () => (laidOut ? { top: CONTAINER_TOP, bottom: CONTAINER_TOP + HEADER_HEIGHT } : { top: 0, bottom: 0 }));

    /** Apply the geometry map to the user messages in the document now. */
    function stubMessages() {
        el.querySelectorAll<HTMLElement>('.sticky-user-message').forEach((node) => {
            const idx = Number(node.dataset.entryIdx);
            stubRect(node, () => {
                if (!laidOut) return { top: 0, bottom: 0 };
                const [msgTop, msgBottom] = geom.get(idx) ?? [0, 0];
                return { top: CONTAINER_TOP + msgTop - top, bottom: CONTAINER_TOP + msgBottom - top };
            });
            // A checkpoint divider above the message lives in the wrapper entry, so the
            // entry starts DIVIDER_HEIGHT earlier than the message.
            const entry = node.closest<HTMLElement>('.chat-entry')!;
            stubRect(entry, () => {
                if (!laidOut) return { top: 0, bottom: 0 };
                const [msgTop, msgBottom] = geom.get(idx) ?? [0, 0];
                return { top: CONTAINER_TOP + msgTop - DIVIDER_HEIGHT - top, bottom: CONTAINER_TOP + msgBottom - top };
            });
        });
    }
    stubMessages();

    return {
        /** The user scrolled, so the tracker is told the reader asked for what is above. */
        async to(scrollTop: number) {
            follow.stopFollowing();
            top = scrollTop;
            el.dispatchEvent(new Event('scroll'));
            await flush();
        },
        /** The view moved on its own: scroll anchoring, or content above changing height. */
        async nudge(delta: number) {
            top = Math.max(0, Math.min(top + delta, scrollHeight - CONTAINER_HEIGHT));
            el.dispatchEvent(new Event('scroll'));
            await flush();
        },
        /** The tab switch: no scroll event, only the layout appearing and disappearing. */
        async hidden(on: boolean) {
            laidOut = !on;
            fireResize();
            await flush();
        },
        /** A page landed above the reader: rows the component had never seen, at new offsets. */
        async relaidOut(next: Map<number, number[]>) {
            geom = next;
            stubMessages();
            fireResize();
            await flush();
        },
        at: () => top,
        /** How many scroll events have reached the component — a layout change sends none. */
        scrollEvents: () => scrollEvents,
        el,
    };
}

/** The ring the header wears while the prompt it named is being fetched. */
function stickySpinner(container: HTMLElement) {
    return container.querySelector('.chat-sticky-header .spinner');
}

/** Click the header, then let the component react to the scroll it caused. */
async function tapHeader(container: HTMLElement, view: ReturnType<typeof mountGeometry>) {
    container.querySelector<HTMLElement>('.chat-sticky-bubble')!.click();
    await flush();
    view.el.dispatchEvent(new Event('scroll')); // jsdom sends none for a scrollTop write
    await flush();
}

function headerText(container: HTMLElement) {
    return container.querySelector<HTMLElement>('.chat-sticky-header')?.textContent?.trim();
}

/** The overlay stays mounted so its box can be measured; hidden is a class, not an unmount. */
function headerHidden(container: HTMLElement) {
    return container.querySelector<HTMLElement>('.chat-sticky-header')?.classList.contains('hidden-header') ?? true;
}

/** The Latest button — a viewport move that carries no gesture with it. */
async function clickJumpLatest(container: HTMLElement, view: ReturnType<typeof mountGeometry>) {
    container.querySelector<HTMLElement>('.chat-jump-latest')!.click();
    await flush();
    view.el.dispatchEvent(new Event('scroll')); // jsdom sends none for a scrollTop write
    await flush();
}

async function renderChat(
    metas?: ReturnType<typeof makeMeta>[],
    list: ReturnType<typeof makeEntry>[] = entries,
    extra: Record<string, unknown> = {},
) {
    const follow = createChatFollow();
    const { container, rerender } = render(ChatEntries, {
        ...baseProps,
        entries: list,
        entryMetas: metas ?? list.map(() => makeMeta()),
        lastEntryIdx: list.length - 1,
        follow,
        ...extra,
    });
    await flush();
    return { container, follow, rerender };
}

describe('ChatEntries — sticky user-message header', () => {
    it('hides the header while the first user message is in view', async () => {
        const { container, follow } = await renderChat();
        const view = mountGeometry(container, follow);
        await view.to(0);
        expect(headerHidden(container)).toBe(true);
    });

    it('shows the user message above the sticky line (measured, not estimated)', async () => {
        const { container, follow } = await renderChat();
        const view = mountGeometry(container, follow);
        // At 420 A (bottom 400) is out of view and B (top 500) is still clear of the
        // overlay. An estimate of A's height would name B.
        await view.to(420);
        expect(headerText(container)).toBe('Fix the bug');
        expect(headerHidden(container)).toBe(false);
    });

    it('lands the named message below the overlay, not behind it', async () => {
        const { container, follow } = await renderChat();
        const view = mountGeometry(container, follow);
        await view.to(550);

        await tapHeader(container, view);

        // B's content top (500) rests HEADER_HEIGHT below the container's top edge.
        // Aligning it with the raw edge instead — what scrollIntoView does — puts its
        // first line behind the overlay.
        expect(view.at()).toBe(B_TOP - HEADER_HEIGHT);
    });

    it('walks back one message per tap, the header always holding the next one', async () => {
        const { container, follow } = await renderChat(undefined, namedEntries);
        const view = mountGeometry(container, follow);
        await view.to(550);
        expect(headerText(container)).toBe('Bravo request');

        await tapHeader(container, view);
        expect(view.at()).toBe(B_TOP - HEADER_HEIGHT);
        // B is now the message at the top, so the header offers the step back.
        expect(headerText(container)).toBe('Alpha request');

        await tapHeader(container, view);
        // A is the first message: clamped at the top, and the header retires rather
        // than sit over the message the user asked to read.
        expect(view.at()).toBe(0);
        expect(headerHidden(container)).toBe(true);
    });

    it('targets the message the header names, not its checkpoint divider', async () => {
        const { container, follow } = await renderChat(entries.map((entry, i) => makeMeta(entry.role === 'user' && i === 2)));
        const view = mountGeometry(container, follow);
        await view.to(550);

        await tapHeader(container, view);

        expect(view.at()).toBe(B_TOP - HEADER_HEIGHT);
    });

    it('clamps at the top of the conversation and drops the header', async () => {
        const { container, follow } = await renderChat();
        const view = mountGeometry(container, follow);
        await view.to(420);
        expect(headerHidden(container)).toBe(false);

        await tapHeader(container, view);

        expect(view.at()).toBe(0);
        expect(headerHidden(container)).toBe(true);
    });

    // A message can come to rest more than 12px above the line, past the tolerance,
    // when the content above it changes height as it is drawn.
    it('shows the previous message when the jump stops above the sticky line', async () => {
        const { container, follow } = await renderChat(undefined, namedEntries3);
        const view = mountGeometry(container, follow, THREE_GEOMETRY);
        await view.to(560);
        expect(headerText(container)).toBe('Charlie request');

        await tapHeader(container, view);
        expect(view.at()).toBe(THREE_C_TOP - HEADER_HEIGHT);
        expect(headerText(container)).toBe('Bravo request');

        // The view drifts 24px further, past the tolerance.
        await view.nudge(24);
        expect(headerText(container)).toBe('Bravo request');

        await tapHeader(container, view);
        expect(view.at()).toBe(THREE_B_TOP - HEADER_HEIGHT);
        expect(headerText(container)).toBe('Alpha request');

        await tapHeader(container, view);
        expect(view.at()).toBe(0);
        expect(headerHidden(container)).toBe(true);
    });

    it('names the message it jumped to once the reader scrolls by hand', async () => {
        const { container, follow } = await renderChat(undefined, namedEntries3);
        const view = mountGeometry(container, follow, THREE_GEOMETRY);
        await view.to(560);
        await tapHeader(container, view);
        await view.nudge(24);

        // The reader scrolled by hand, so the header names the message above the line again.
        await view.to(430);
        expect(headerText(container)).toBe('Charlie request');
    });

    it('names the message above the line again after the Latest button', async () => {
        const { container, follow } = await renderChat(undefined, namedEntries3);
        const view = mountGeometry(container, follow, THREE_GEOMETRY);
        await view.to(560);
        await tapHeader(container, view);
        await view.nudge(24);
        expect(headerText(container)).toBe('Bravo request');

        // The Latest button moves the viewport without a gesture, so only reaching the latest turn
        // ends the handover.
        await clickJumpLatest(container, view);
        expect(headerText(container)).toBe('Charlie request');
    });

    it('ends the tap handover when the entry list changes', async () => {
        const { container, follow, rerender } = await renderChat(undefined, namedEntries3);
        const view = mountGeometry(container, follow, THREE_GEOMETRY);
        await view.to(560);
        await tapHeader(container, view);
        await view.nudge(24);
        expect(headerText(container)).toBe('Bravo request');

        const withNewTurn = [...namedEntries3, makeEntry('a-4', 'assistant', 'Noted')];
        rerender({ entries: withNewTurn, entryMetas: withNewTurn.map(() => makeMeta()) });
        await flush();
        expect(headerText(container)).toBe('Charlie request');
    });

    it('hides rather than guessing while the column has no layout, and recovers with no scroll event', async () => {
        const { container, follow } = await renderChat(undefined, namedEntries);
        const view = mountGeometry(container, follow);
        await view.to(420);
        expect(headerText(container)).toBe('Alpha request');
        const scrolls = view.scrollEvents();

        // With every rect zero, every message reads as above the line, so the choice would land on
        // the newest one.
        await view.hidden(true);
        expect(headerHidden(container)).toBe(true);
        expect(headerText(container)).toBe('');

        await view.hidden(false);
        expect(headerHidden(container)).toBe(false);
        expect(headerText(container)).toBe('Alpha request');
        // The layout changed and nothing scrolled: the observer is what noticed.
        expect(view.scrollEvents()).toBe(scrolls);
    });

    // One user message in the loaded window, and the prompt read naming what lies above it.
    const oneTurn = [
        makeEntry('u-a', 'user', 'Alpha request\nmore'),
        makeEntry('a-1', 'assistant', 'On it'),
    ];
    const TALL_A = new Map<number, number[]>([[0, [300, 900]]]);

    it('shows the oldest message it knows without fetching it into view', async () => {
        const onLoadEarlier = vi.fn(() => Promise.resolve());
        const { container, follow } = await renderChat(undefined, oneTurn, { hasEarlier: true, onLoadEarlier });
        // 900 is past the top-edge trigger (1.5 viewports = 750), so a request here would be the
        // header's own.
        const view = mountGeometry(container, follow, TALL_A, 4000);
        await view.to(900);
        expect(headerText(container)).toBe('Alpha request');
        expect(headerHidden(container)).toBe(false);
        // The list edge is the fetch affordance; naming a prompt is not a request for its rows.
        expect(onLoadEarlier).not.toHaveBeenCalled();
    });

    it('still hides at the top when nothing older exists', async () => {
        const onLoadEarlier = vi.fn(() => Promise.resolve());
        const { container, follow } = await renderChat(undefined, oneTurn, { hasEarlier: false, onLoadEarlier });
        const view = mountGeometry(container, follow, TALL_A);
        await view.to(500);
        expect(headerHidden(container)).toBe(true);
        expect(onLoadEarlier).not.toHaveBeenCalled();
    });

    // A window counted in answers holds no prompt at all: the read that answers prompts is what the
    // header shows then, and its row is fetched only when the reader asks for it.
    const midTurn = [makeEntry('a-1', 'assistant', 'Reading the tree')];
    const openedTurn = { id: 'u-open', created_at: '2026-01-01T00:00:00Z', text: 'work through the tree\nfile by file' };

    it('names the prompt the window left out', async () => {
        const { container, follow } = await renderChat(undefined, midTurn, { turns: [openedTurn] });
        const view = mountGeometry(container, follow, new Map());
        await view.to(500);
        expect(headerText(container)).toBe('work through the tree');
        expect(headerHidden(container)).toBe(false);
    });

    it('fetches the prompt it names, then lands it on the line', async () => {
        let landed: (value: boolean) => void = () => {};
        const onJumpToTurn = vi.fn(() => new Promise<boolean>(resolve => { landed = resolve; }));
        const { container, follow, rerender } = await renderChat(undefined, midTurn, { turns: [openedTurn], onJumpToTurn });
        const view = mountGeometry(container, follow, new Map());
        await view.to(500);

        container.querySelector<HTMLElement>('.chat-sticky-bubble')!.click();
        await flush();
        expect(onJumpToTurn).toHaveBeenCalledWith('u-open');
        expect(view.at()).toBe(500);

        // The rows arrived: the prompt's row is on screen now, at a measured offset.
        const withPrompt = [makeEntry('u-open', 'user', 'work through the tree\nfile by file'), ...midTurn];
        rerender({ entries: withPrompt, entryMetas: withPrompt.map(() => makeMeta()) });
        landed(true);
        await view.relaidOut(new Map([[0, [600, 700]]]));
        await flush();

        expect(view.at()).toBe(600 - HEADER_HEIGHT);
        // It rests on the line, so it is the message at the top and there is nothing older to offer.
        expect(headerHidden(container)).toBe(true);
    });

    it('leaves the view alone when the prompt cannot be reached', async () => {
        const onJumpToTurn = vi.fn(() => Promise.resolve(false));
        const { container, follow } = await renderChat(undefined, midTurn, { turns: [openedTurn], onJumpToTurn });
        const view = mountGeometry(container, follow, new Map());
        await view.to(500);

        container.querySelector<HTMLElement>('.chat-sticky-bubble')!.click();
        await flush();

        expect(onJumpToTurn).toHaveBeenCalledWith('u-open');
        expect(view.at()).toBe(500);
    });

    // Reaching a prompt older than every row held can take several pages, so the header says the wait
    // is happening instead of looking like a click that did nothing.
    it('wears a ring while the named prompt is being fetched', async () => {
        let landed: (value: boolean) => void = () => {};
        const onJumpToTurn = vi.fn(() => new Promise<boolean>(resolve => { landed = resolve; }));
        const { container, follow, rerender } = await renderChat(undefined, midTurn, { turns: [openedTurn], onJumpToTurn });
        const view = mountGeometry(container, follow, new Map());
        await view.to(500);
        expect(stickySpinner(container)).toBeNull();

        container.querySelector<HTMLElement>('.chat-sticky-bubble')!.click();
        await flush();
        expect(stickySpinner(container)).toBeTruthy();
        expect(headerText(container)).toBe('work through the tree');
        expect(container.querySelector('.chat-sticky-header')?.getAttribute('aria-busy')).toBe('true');

        const withPrompt = [makeEntry('u-open', 'user', 'work through the tree\nfile by file'), ...midTurn];
        rerender({ entries: withPrompt, entryMetas: withPrompt.map(() => makeMeta()) });
        landed(true);
        await view.relaidOut(new Map([[0, [600, 700]]]));
        await flush();

        expect(stickySpinner(container)).toBeNull();
    });

    it('ignores a second tap while the fetch is in flight', async () => {
        const onJumpToTurn = vi.fn(() => new Promise<boolean>(() => {}));
        const { container, follow } = await renderChat(undefined, midTurn, { turns: [openedTurn], onJumpToTurn });
        const view = mountGeometry(container, follow, new Map());
        await view.to(500);

        const bubble = container.querySelector<HTMLElement>('.chat-sticky-bubble')!;
        bubble.click();
        await flush();
        bubble.click();
        bubble.click();
        await flush();

        expect(onJumpToTurn).toHaveBeenCalledTimes(1);
    });

    it('wears no ring for a jump to a row already rendered', async () => {
        const { container, follow } = await renderChat();
        const view = mountGeometry(container, follow);
        await view.to(550);

        await tapHeader(container, view);

        expect(view.at()).toBe(B_TOP - HEADER_HEIGHT);
        expect(stickySpinner(container)).toBeNull();
    });

    it('names the older message once the page lands', async () => {
        const onLoadEarlier = vi.fn(() => Promise.resolve());
        const { container, follow, rerender } = await renderChat(undefined, oneTurn, { hasEarlier: true, onLoadEarlier });
        const view = mountGeometry(container, follow, TALL_A);
        await view.to(500);
        expect(headerText(container)).toBe('Alpha request');

        const olderTurn = [
            makeEntry('u-0', 'user', 'Zulu request\nmore'),
            makeEntry('a-0', 'assistant', 'First reply'),
            ...oneTurn,
        ];
        rerender({ entries: olderTurn, entryMetas: olderTurn.map(() => makeMeta()) });
        await flush();
        // The reader is settled back onto their own row, so the page that landed above is what the
        // header has to name now.
        await view.relaidOut(new Map([[0, [0, 120]], [2, [400, 1000]]]));
        await view.nudge(-146);
        expect(headerText(container)).toBe('Zulu request');
    });
});
