import { describe, it, expect, vi } from 'vitest';
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
        checkpointIndex: withCheckpoint ? 1 : -1,
        isLastUserMessage: false,
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
function mountGeometry(container: HTMLElement, follow: ChatFollow, geometry?: Map<number, number[]>) {
    const el = container.querySelector<HTMLElement>('.chat-messages')!;
    let top = 0;
    Object.defineProperty(el, 'scrollTop', {
        configurable: true,
        get: () => top,
        set: (value: number) => {
            top = Math.max(0, Math.min(value, SCROLL_HEIGHT - CONTAINER_HEIGHT));
        },
    });
    Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => SCROLL_HEIGHT });
    Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => CONTAINER_HEIGHT });
    stubRect(el, () => ({ top: CONTAINER_TOP, bottom: CONTAINER_TOP + CONTAINER_HEIGHT }));

    const geom = geometry ?? new Map([[0, [A_TOP, A_BOTTOM]], [2, [B_TOP, B_BOTTOM]]]);
    el.querySelectorAll<HTMLElement>('.sticky-user-message').forEach((node) => {
        const [msgTop, msgBottom] = geom.get(Number(node.dataset.entryIdx)) ?? [0, 0];
        stubRect(node, () => ({
            top: CONTAINER_TOP + msgTop - top,
            bottom: CONTAINER_TOP + msgBottom - top,
        }));
        // A checkpoint divider above the message lives in the wrapper entry, so the
        // entry starts DIVIDER_HEIGHT earlier than the message.
        const entry = node.closest<HTMLElement>('.chat-entry')!;
        stubRect(entry, () => ({
            top: CONTAINER_TOP + msgTop - DIVIDER_HEIGHT - top,
            bottom: CONTAINER_TOP + msgBottom - top,
        }));
    });

    // The overlay is a sibling of the scroller, pinned to its top edge.
    const header = container.querySelector<HTMLElement>('.chat-sticky-header');
    if (header) stubRect(header, () => ({ top: CONTAINER_TOP, bottom: CONTAINER_TOP + HEADER_HEIGHT }));

    return {
        /** The user scrolled. The gesture is declared because a viewport gaining a
         *  scrollable height otherwise reads to the follow tracker as a growing
         *  document, which it would then pin back to the bottom. */
        async to(scrollTop: number) {
            follow.noteUserIntent();
            top = scrollTop;
            el.dispatchEvent(new Event('scroll'));
            await flush();
        },
        /** The view moved on its own: scroll anchoring, or content above changing height. */
        async nudge(delta: number) {
            top = Math.max(0, Math.min(top + delta, SCROLL_HEIGHT - CONTAINER_HEIGHT));
            el.dispatchEvent(new Event('scroll'));
            await flush();
        },
        at: () => top,
        el,
    };
}

/** Click the header, then let the component react to the scroll it caused. */
async function tapHeader(container: HTMLElement, view: ReturnType<typeof mountGeometry>) {
    container.querySelector<HTMLElement>('.chat-sticky-bubble')!.click();
    await flush();
    view.el.dispatchEvent(new Event('scroll')); // jsdom sends none for a scrollTop write
    await flush();
}

function headerText(container: HTMLElement) {
    return container.querySelector<HTMLElement>('.chat-sticky-bubble')?.textContent;
}

async function renderChat(metas?: ReturnType<typeof makeMeta>[], list: ReturnType<typeof makeEntry>[] = entries) {
    const follow = createChatFollow();
    const { container } = render(ChatEntries, {
        ...baseProps,
        entries: list,
        entryMetas: metas ?? list.map(() => makeMeta()),
        lastEntryIdx: list.length - 1,
        follow,
    });
    await flush();
    return { container, follow };
}

describe('ChatEntries — sticky user-message header', () => {
    it('hides the header while the first user message is in view', async () => {
        const { container, follow } = await renderChat();
        const view = mountGeometry(container, follow);
        await view.to(0);
        expect(container.querySelector('.chat-sticky-header')).toBeNull();
    });

    it('shows the user message above the sticky line (measured, not estimated)', async () => {
        const { container, follow } = await renderChat();
        const view = mountGeometry(container, follow);
        // At 420 A (bottom 400) is out of view and B (top 500) is still clear of the
        // overlay. An estimate of A's height would name B.
        await view.to(420);
        expect(headerText(container)).toBe('Fix the bug');
        expect(container.querySelector('.chat-sticky-header')).toBeTruthy();
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
        expect(container.querySelector('.chat-sticky-header')).toBeNull();
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
        expect(container.querySelector('.chat-sticky-header')).toBeTruthy();

        await tapHeader(container, view);

        expect(view.at()).toBe(0);
        expect(container.querySelector('.chat-sticky-header')).toBeNull();
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
        expect(container.querySelector('.chat-sticky-header')).toBeNull();
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
});
